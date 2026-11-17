// Package websocket adapts github.com/gorilla/websocket, which owns the protocol (handshake,
// framing, masking, control frames). It is the only package that imports gorilla: connection
// management, authorization and message routing live in internal/presentation/websocket.
package websocket

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// Fixed protocol limits (docs/WEBSOCKET.md).
const (
	// MaxMessageBytes bounds every message read from a client; larger ones close the
	// connection with 1009.
	MaxMessageBytes = 4 << 10
	// PongWait is how long a connection may stay silent (no message, no pong) before it is
	// considered dead. Peers must ping more often than this.
	PongWait = 60 * time.Second
	// WriteWait bounds every write, so a stuck client cannot block its writer forever.
	WriteWait = 10 * time.Second

	handshakeTimeout = 10 * time.Second
	bufferBytes      = 4 << 10
)

var (
	// ErrTooLarge reports a client message above MaxMessageBytes (the peer was sent 1009).
	ErrTooLarge = errors.New("websocket: message too large")
	// ErrClosed reports a connection closed by either side or by a failed deadline.
	ErrClosed = errors.New("websocket: connection closed")
	// ErrNotText reports a binary message; the protocol is JSON text only.
	ErrNotText = errors.New("websocket: binary messages are not supported")
)

// Upgrader performs handshakes. onError writes the HTTP error response for a failed
// handshake (status is 400, 403 or 405), so it follows the API's error format.
type Upgrader struct {
	u websocket.Upgrader
}

func NewUpgrader(onError func(w http.ResponseWriter, status int)) *Upgrader {
	return &Upgrader{u: websocket.Upgrader{
		HandshakeTimeout: handshakeTimeout,
		ReadBufferSize:   bufferBytes,
		WriteBufferSize:  bufferBytes,
		CheckOrigin:      SameOrigin,
		Error: func(w http.ResponseWriter, _ *http.Request, status int, _ error) {
			onError(w, status)
		},
	}}
}

// SameOrigin requires an Origin header naming the host the request was sent to. Browsers
// always send Origin on WebSocket handshakes, so a missing one is not a browser on our page.
func SameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

// Upgrade completes the handshake (Origin checked by SameOrigin). On failure the HTTP
// response has already been written.
func (up *Upgrader) Upgrade(w http.ResponseWriter, r *http.Request) (*Conn, error) {
	ws, err := up.u.Upgrade(w, r, nil)
	if err != nil {
		return nil, err
	}
	ws.SetReadLimit(MaxMessageBytes)
	// The HTTP server's read/write timeouts must not apply to a long-lived connection.
	_ = ws.NetConn().SetDeadline(time.Time{})
	_ = ws.SetReadDeadline(time.Now().Add(PongWait))
	ws.SetPongHandler(func(string) error {
		return ws.SetReadDeadline(time.Now().Add(PongWait))
	})
	return &Conn{ws: ws}, nil
}

// Conn is one WebSocket connection. Read must be called from a single goroutine and Write
// from a single (other) goroutine; Ping, CloseWith and Close are safe from any goroutine.
type Conn struct {
	ws *websocket.Conn
}

// Read returns the next text message. Any message extends the liveness deadline.
func (c *Conn) Read() ([]byte, error) {
	typ, data, err := c.ws.ReadMessage()
	if err != nil {
		return nil, mapError(err)
	}
	_ = c.ws.SetReadDeadline(time.Now().Add(PongWait))
	if typ != websocket.TextMessage {
		return nil, ErrNotText
	}
	return data, nil
}

// Write sends one text message within WriteWait.
func (c *Conn) Write(data []byte) error {
	_ = c.ws.SetWriteDeadline(time.Now().Add(WriteWait))
	return mapError(c.ws.WriteMessage(websocket.TextMessage, data))
}

// Ping sends a ping control frame; the peer's pong extends the read deadline.
func (c *Conn) Ping() error {
	return mapError(c.ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(WriteWait)))
}

// CloseWith sends a close frame with code and reason (best effort), then closes the
// connection. reason is truncated to the 123 bytes a close frame allows.
func (c *Conn) CloseWith(code int, reason string) error {
	if len(reason) > 123 {
		reason = reason[:123]
	}
	_ = c.ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, reason), time.Now().Add(WriteWait))
	return c.ws.Close()
}

// Close closes the connection without a close frame.
func (c *Conn) Close() error { return c.ws.Close() }

func mapError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, websocket.ErrReadLimit):
		return ErrTooLarge
	case errors.Is(err, websocket.ErrCloseSent), errors.Is(err, net.ErrClosed):
		return ErrClosed
	}
	var ce *websocket.CloseError
	var ne net.Error
	if errors.As(err, &ce) || (errors.As(err, &ne) && ne.Timeout()) {
		return ErrClosed
	}
	return err
}
