// Package websocket is the panel's WebSocket manager (spec §8): connection registry and
// limits, session checks, subscriptions, message routing, bounded per-connection queues and
// graceful shutdown. The protocol itself is handled by internal/infrastructure/websocket.
// The message contract is documented in docs/WEBSOCKET.md.
package websocket

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/bruno-mesquita/mesquita-panel/internal/application/auth"
	"github.com/bruno-mesquita/mesquita-panel/internal/application/console"
	"github.com/bruno-mesquita/mesquita-panel/internal/application/servers"
	"github.com/bruno-mesquita/mesquita-panel/internal/domain"
	wsconn "github.com/bruno-mesquita/mesquita-panel/internal/infrastructure/websocket"
)

// Fixed limits (docs/WEBSOCKET.md).
const (
	maxConnections       = 64
	maxConnectionsByUser = 8
	maxSubscriptions     = 16
	sendQueueSize        = 512
	pingInterval         = 30 * time.Second
	sessionCheckInterval = 60 * time.Second
)

// Close codes sent to clients.
const (
	closeGoingAway       = 1001 // the panel is shutting down
	closeUnsupportedData = 1003 // binary message
	closeTooLarge        = 1009 // message above the size limit
	closeTryAgainLater   = 1013 // the client could not keep up with its queue
	closeUnauthenticated = 4401 // the session expired or was revoked
)

// Authenticator re-checks a session (auth.Service).
type Authenticator interface {
	Authenticate(ctx context.Context, token string) (auth.Identity, error)
}

// Console is the live console (console.Service).
type Console interface {
	Subscribe(ctx context.Context, serverID string, sink console.Sink) (func(), error)
	SendCommand(ctx context.Context, serverID, command string, actor servers.Actor) error
}

type Hub struct {
	auth    Authenticator
	console Console
	log     *slog.Logger
	up      *wsconn.Upgrader

	// Limits, fields only so tests can lower them.
	maxConns, maxConnsByUser, queueSize int
	pingEvery, sessionEvery             time.Duration

	mu      sync.Mutex
	clients map[*client]struct{}
	byUser  map[string]int // connections (and reserved handshakes) per user ID
	closed  bool
	wg      sync.WaitGroup
}

func NewHub(a Authenticator, c Console, log *slog.Logger) *Hub {
	return &Hub{
		auth: a, console: c, log: log,
		up: wsconn.NewUpgrader(func(w http.ResponseWriter, status int) {
			code := "bad_handshake"
			if status == http.StatusForbidden {
				code = "origin_not_allowed"
			}
			writeHTTPError(w, status, code, "WebSocket handshake rejected")
		}),
		maxConns: maxConnections, maxConnsByUser: maxConnectionsByUser, queueSize: sendQueueSize,
		pingEvery: pingInterval, sessionEvery: sessionCheckInterval,
		clients: map[*client]struct{}{}, byUser: map[string]int{},
	}
}

// writeHTTPError answers a rejected handshake in the REST error format (docs/API.md).
func writeHTTPError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": message}})
}

// Serve upgrades an authenticated request and runs the connection until it ends. id and
// token are the session that authenticated the handshake; token is re-checked periodically.
func (h *Hub) Serve(w http.ResponseWriter, r *http.Request, id auth.Identity, token, ip string) {
	userID := id.User.ID
	if code, status := h.reserve(userID); code != "" {
		writeHTTPError(w, status, code, "cannot open another WebSocket connection")
		return
	}
	conn, err := h.up.Upgrade(w, r)
	if err != nil {
		h.release(userID, nil)
		return
	}

	ctx, cancel := context.WithCancel(context.WithoutCancel(r.Context()))
	c := &client{
		hub: h, conn: conn, ctx: ctx, cancel: cancel, token: token,
		actor: servers.Actor{UserID: userID, IP: ip},
		send:  make(chan []byte, h.queueSize), done: make(chan struct{}), subs: map[string]func(){},
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		cancel()
		conn.CloseWith(closeGoingAway, "server shutting down")
		h.release(userID, nil)
		return
	}
	h.clients[c] = struct{}{}
	h.wg.Add(1)
	h.mu.Unlock()
	defer h.wg.Done()
	h.log.Debug("websocket connected", "user_id", userID, "ip", ip)

	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		c.writeLoop()
	}()
	c.readLoop()
	c.fail(0, "")
	<-writerDone
	cancel()
	for _, unsub := range c.subs {
		unsub()
	}
	h.release(userID, c)
	h.log.Debug("websocket disconnected", "user_id", userID, "ip", ip)
}

// reserve claims a connection slot before the handshake, so limits hold under concurrency.
func (h *Hub) reserve(userID string) (code string, status int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	total := 0
	for _, n := range h.byUser {
		total += n
	}
	switch {
	case h.closed:
		return "shutting_down", http.StatusServiceUnavailable
	case total >= h.maxConns || h.byUser[userID] >= h.maxConnsByUser:
		return "too_many_connections", http.StatusTooManyRequests
	}
	h.byUser[userID]++
	return "", 0
}

func (h *Hub) release(userID string, c *client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if c != nil {
		delete(h.clients, c)
	}
	if h.byUser[userID]--; h.byUser[userID] <= 0 {
		delete(h.byUser, userID)
	}
}

// Shutdown closes every connection with 1001 and waits for them to finish, or for ctx.
// http.Server.Shutdown does not track hijacked connections, so this must be called too.
func (h *Hub) Shutdown(ctx context.Context) error {
	h.mu.Lock()
	h.closed = true
	for c := range h.clients {
		c.fail(closeGoingAway, "server shutting down")
	}
	h.mu.Unlock()
	done := make(chan struct{})
	go func() {
		h.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Connections reports the number of open connections.
func (h *Hub) Connections() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients)
}

type client struct {
	hub    *Hub
	conn   *wsconn.Conn
	ctx    context.Context // cancelled when the connection ends
	cancel context.CancelFunc
	token  string
	actor  servers.Actor

	send chan []byte // bounded; full means the client is too slow

	closeOnce   sync.Once
	done        chan struct{}
	closeCode   int // 0: close without a close frame (the peer is gone)
	closeReason string

	subs map[string]func() // channel -> unsubscribe; read loop only
}

// fail ends the connection. The first call wins; it never blocks.
func (c *client) fail(code int, reason string) {
	c.closeOnce.Do(func() {
		c.closeCode, c.closeReason = code, reason
		close(c.done)
	})
}

// enqueue queues a message without blocking. A full queue closes the connection: the client
// reconnects and gets the recent history again, instead of the panel buffering without bound.
func (c *client) enqueue(msg []byte) {
	select {
	case c.send <- msg:
	default:
		c.fail(closeTryAgainLater, "client too slow")
	}
}

func (c *client) writeLoop() {
	ping := time.NewTicker(c.hub.pingEvery)
	defer ping.Stop()
	check := time.NewTicker(c.hub.sessionEvery)
	defer check.Stop()
	for {
		select {
		case <-c.done:
			if c.closeCode != 0 {
				c.conn.CloseWith(c.closeCode, c.closeReason)
			} else {
				c.conn.Close()
			}
			return
		case msg := <-c.send:
			if err := c.conn.Write(msg); err != nil {
				c.fail(0, "")
			}
		case <-ping.C:
			if err := c.conn.Ping(); err != nil {
				c.fail(0, "")
			}
		case <-check.C:
			c.checkSession()
		}
	}
}

// checkSession closes the connection with 4401 once the session is no longer valid and
// reports whether it still is. Other errors (e.g. a busy database) keep the connection open.
func (c *client) checkSession() bool {
	_, err := c.hub.auth.Authenticate(c.ctx, c.token)
	if errors.Is(err, domain.ErrUnauthenticated) {
		c.fail(closeUnauthenticated, "session expired")
		return false
	}
	return true
}

func (c *client) readLoop() {
	for {
		data, err := c.conn.Read()
		switch {
		case errors.Is(err, wsconn.ErrTooLarge):
			c.fail(closeTooLarge, "message too large")
			return
		case errors.Is(err, wsconn.ErrNotText):
			c.fail(closeUnsupportedData, "text messages only")
			return
		case err != nil:
			c.fail(0, "")
			return
		}
		select {
		case <-c.done:
			return
		default:
		}
		c.handle(data)
	}
}
