package websocket

// Contract tests for the WebSocket protocol (docs/WEBSOCKET.md). The gorilla client is used
// here only as a test peer; production code reaches gorilla through the infrastructure adapter.

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/bruno-mesquita/Craftkeeper/internal/application/auth"
	"github.com/bruno-mesquita/Craftkeeper/internal/application/console"
	"github.com/bruno-mesquita/Craftkeeper/internal/application/servers"
	"github.com/bruno-mesquita/Craftkeeper/internal/domain"
)

const (
	sid      = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	unknown  = "ZZZZZZZZZZZZZZZZZZZZZZZZZZ"
	stopped  = "STOPPEDSTOPPEDSTOPPEDSTOPP"
	channel  = "server:" + sid
	goodTok  = "good-token"
	testUser = "X-Test-User"
)

type fakeAuth struct {
	mu      sync.Mutex
	revoked bool
}

func (a *fakeAuth) Authenticate(_ context.Context, token string) (auth.Identity, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.revoked || token != goodTok {
		return auth.Identity{}, domain.ErrUnauthenticated
	}
	return auth.Identity{}, nil
}

func (a *fakeAuth) revoke() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.revoked = true
}

type command struct {
	serverID, text string
	actor          servers.Actor
}

type fakeConsole struct {
	mu       sync.Mutex
	sinks    map[string]console.Sink
	subs     int
	unsubs   int
	commands []command
}

func (f *fakeConsole) Subscribe(_ context.Context, serverID string, sink console.Sink) (func(), error) {
	if serverID == unknown {
		return nil, domain.ErrNotFound
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	sink(console.Event{Kind: console.EventHistory, Lines: []domain.ConsoleLine{
		{Time: time.Date(2026, 11, 17, 10, 0, 0, 500, time.UTC), Stream: domain.StreamStdout, Text: "Done (3.2s)!"},
	}})
	sink(console.Event{Kind: console.EventStatus, Status: domain.StatusRunning})
	f.sinks[serverID] = sink
	f.subs++
	return func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		delete(f.sinks, serverID)
		f.unsubs++
	}, nil
}

func (f *fakeConsole) emit(serverID string, e console.Event) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s := f.sinks[serverID]; s != nil {
		s(e)
	}
}

func (f *fakeConsole) counts() (subs, unsubs int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.subs, f.unsubs
}

func (f *fakeConsole) SendCommand(_ context.Context, serverID, text string, actor servers.Actor) error {
	if err := domain.ValidateConsoleCommand(text); err != nil {
		return err
	}
	if serverID == stopped {
		return domain.ErrServerNotRunning
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commands = append(f.commands, command{serverID, text, actor})
	return nil
}

type env struct {
	hub     *Hub
	auth    *fakeAuth
	console *fakeConsole
	url     string
	origin  string
}

func newEnv(t *testing.T, tune func(*Hub)) *env {
	t.Helper()
	e := &env{auth: &fakeAuth{}, console: &fakeConsole{sinks: map[string]console.Sink{}}}
	e.hub = NewHub(e.auth, e.console, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if tune != nil {
		tune(e.hub)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := r.Header.Get(testUser)
		if user == "" {
			user = "U1"
		}
		e.hub.Serve(w, r, auth.Identity{User: domain.User{ID: user}}, goodTok, "100.64.0.9")
	}))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		e.hub.Shutdown(ctx)
		srv.Close()
	})
	e.url, e.origin = "ws"+strings.TrimPrefix(srv.URL, "http"), srv.URL
	return e
}

func (e *env) dialAs(t *testing.T, user string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	h := http.Header{"Origin": {e.origin}, testUser: {user}}
	c, resp, err := websocket.DefaultDialer.Dial(e.url, h)
	if c != nil {
		t.Cleanup(func() { c.Close() })
	}
	return c, resp, err
}

func (e *env) dial(t *testing.T) *websocket.Conn {
	t.Helper()
	c, _, err := e.dialAs(t, "U1")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func send(t *testing.T, c *websocket.Conn, msg string) {
	t.Helper()
	if err := c.WriteMessage(websocket.TextMessage, []byte(msg)); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, c *websocket.Conn) map[string]any {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	var m map[string]any
	if err := c.ReadJSON(&m); err != nil {
		t.Fatalf("read: %v", err)
	}
	if m["version"] != float64(1) {
		t.Fatalf("message without version 1: %v", m)
	}
	return m
}

func expectType(t *testing.T, m map[string]any, typ, id string) map[string]any {
	t.Helper()
	if m["type"] != typ || (id != "" && m["id"] != id) {
		t.Fatalf("message = %v, want type %q id %q", m, typ, id)
	}
	p, _ := m["payload"].(map[string]any)
	if p == nil {
		t.Fatalf("message without payload object: %v", m)
	}
	return p
}

func expectError(t *testing.T, c *websocket.Conn, code string) {
	t.Helper()
	m := read(t, c)
	if p := expectType(t, m, "error", ""); p["code"] != code || p["message"] == "" {
		t.Fatalf("error = %v, want code %q", p, code)
	}
}

func expectClose(t *testing.T, c *websocket.Conn, code int) {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(10 * time.Second))
	for {
		_, _, err := c.ReadMessage()
		if err == nil {
			continue // messages queued before the close
		}
		var ce *websocket.CloseError
		if !errors.As(err, &ce) || ce.Code != code {
			t.Fatalf("closed with %v, want code %d", err, code)
		}
		return
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSubscribeStreamsHistoryOutputAndStatus(t *testing.T) {
	e := newEnv(t, nil)
	c := e.dial(t)
	send(t, c, `{"version":1,"type":"subscribe","channel":"`+channel+`","id":"s1"}`)

	m := read(t, c)
	expectType(t, m, "subscribed", "s1")
	if m["channel"] != channel {
		t.Fatalf("channel = %v", m["channel"])
	}
	p := expectType(t, read(t, c), "console.history", "")
	lines, _ := p["lines"].([]any)
	if len(lines) != 1 {
		t.Fatalf("history = %v", p)
	}
	if l := lines[0].(map[string]any); l["data"] != "Done (3.2s)!" || l["stream"] != "stdout" || l["time"] != "2026-11-17T10:00:00.0000005Z" {
		t.Fatalf("history line = %v", l)
	}
	if p := expectType(t, read(t, c), "server.status", ""); p["status"] != "running" {
		t.Fatalf("status = %v", p)
	}

	e.console.emit(sid, console.Event{Kind: console.EventLine, Line: domain.ConsoleLine{Stream: domain.StreamStderr, Text: "<b>warn</b>"}})
	m = read(t, c)
	p = expectType(t, m, "console.output", "")
	if m["channel"] != channel || p["data"] != "<b>warn</b>" || p["stream"] != "stderr" {
		t.Fatalf("output = %v", m)
	}
	if _, hasTime := p["time"]; hasTime {
		t.Fatalf("zero time must be omitted: %v", p)
	}
	e.console.emit(sid, console.Event{Kind: console.EventStatus, Status: domain.StatusStopped})
	if p := expectType(t, read(t, c), "server.status", ""); p["status"] != "stopped" {
		t.Fatalf("status = %v", p)
	}

	// Subscribing twice is idempotent.
	send(t, c, `{"version":1,"type":"subscribe","channel":"`+channel+`","id":"s2"}`)
	expectType(t, read(t, c), "subscribed", "s2")
	if subs, _ := e.console.counts(); subs != 1 {
		t.Fatalf("console subscriptions = %d", subs)
	}

	send(t, c, `{"version":1,"type":"unsubscribe","channel":"`+channel+`","id":"u1"}`)
	expectType(t, read(t, c), "unsubscribed", "u1")
	if _, unsubs := e.console.counts(); unsubs != 1 {
		t.Fatalf("unsubscribes = %d", unsubs)
	}
}

func TestProtocolErrorsKeepTheConnection(t *testing.T) {
	e := newEnv(t, nil)
	c := e.dial(t)
	cases := []struct{ msg, code string }{
		{`not json`, "invalid_message"},
		{`{"version":1,"type":"subscribe","channel":"` + channel + `","id":"` + strings.Repeat("i", 65) + `"}`, "invalid_message"},
		{`{"version":2,"type":"subscribe","channel":"` + channel + `"}`, "unsupported_version"},
		{`{"type":"subscribe","channel":"` + channel + `"}`, "unsupported_version"},
		{`{"version":1,"type":"publish","channel":"` + channel + `"}`, "unknown_type"},
		{`{"version":1,"type":"subscribe","channel":"server:../../etc"}`, "invalid_channel"},
		{`{"version":1,"type":"subscribe","channel":"metrics"}`, "invalid_channel"},
		{`{"version":1,"type":"subscribe","channel":"server:` + unknown + `"}`, "not_found"},
		{`{"version":1,"type":"unsubscribe","channel":"` + channel + `"}`, "not_subscribed"},
		{`{"version":1,"type":"console.command","channel":"` + channel + `","payload":"op me"}`, "invalid_message"},
		{`{"version":1,"type":"console.command","channel":"` + channel + `","payload":{"command":"say a\nop me"}}`, "invalid_command"},
		{`{"version":1,"type":"console.command","channel":"server:` + stopped + `","payload":{"command":"list"}}`, "server_not_running"},
	}
	for _, tc := range cases {
		send(t, c, tc.msg)
		expectError(t, c, tc.code)
	}
	if len(e.console.commands) != 0 {
		t.Fatalf("rejected commands reached the console: %v", e.console.commands)
	}
}

func TestCommandIsAcknowledged(t *testing.T) {
	e := newEnv(t, nil)
	c := e.dial(t)
	send(t, c, `{"version":1,"type":"console.command","channel":"`+channel+`","id":"c1","payload":{"command":"say olá"}}`)
	m := read(t, c)
	expectType(t, m, "ack", "c1")
	if m["channel"] != channel {
		t.Fatalf("ack = %v", m)
	}
	e.console.mu.Lock()
	defer e.console.mu.Unlock()
	want := command{sid, "say olá", servers.Actor{UserID: "U1", IP: "100.64.0.9"}}
	if len(e.console.commands) != 1 || e.console.commands[0] != want {
		t.Fatalf("commands = %+v", e.console.commands)
	}
}

func TestOversizedMessageCloses1009(t *testing.T) {
	e := newEnv(t, nil)
	c := e.dial(t)
	send(t, c, `{"version":1,"type":"subscribe","channel":"`+strings.Repeat("x", 5000)+`"}`)
	expectClose(t, c, websocket.CloseMessageTooBig)
}

func TestBinaryMessageCloses1003(t *testing.T) {
	e := newEnv(t, nil)
	c := e.dial(t)
	c.WriteMessage(websocket.BinaryMessage, []byte(`{}`))
	expectClose(t, c, websocket.CloseUnsupportedData)
}

func TestSlowClientCloses1013(t *testing.T) {
	e := newEnv(t, func(h *Hub) { h.queueSize = 2 })
	c := e.dial(t)
	send(t, c, `{"version":1,"type":"subscribe","channel":"`+channel+`"}`)
	waitFor(t, "subscription", func() bool { s, _ := e.console.counts(); return s == 1 })
	for i := 0; i < 2000; i++ {
		e.console.emit(sid, console.Event{Kind: console.EventLine, Line: domain.ConsoleLine{Stream: domain.StreamStdout, Text: "spam"}})
	}
	expectClose(t, c, websocket.CloseTryAgainLater)
	waitFor(t, "cleanup", func() bool { _, u := e.console.counts(); return u == 1 && e.hub.Connections() == 0 })
}

func TestConnectionLimits(t *testing.T) {
	e := newEnv(t, func(h *Hub) { h.maxConns, h.maxConnsByUser = 3, 2 })
	a1, _, err := e.dialAs(t, "A")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.dialAs(t, "A"); err != nil {
		t.Fatal(err)
	}
	expect429 := func(user string) {
		t.Helper()
		_, resp, err := e.dialAs(t, user)
		if err == nil || resp == nil || resp.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("user %s: dial = %v, want 429", user, resp)
		}
		b, _ := io.ReadAll(resp.Body)
		if !strings.Contains(string(b), `"code":"too_many_connections"`) {
			t.Fatalf("body = %s", b)
		}
	}
	expect429("A") // per user
	if _, _, err := e.dialAs(t, "B"); err != nil {
		t.Fatal(err)
	}
	expect429("B") // total

	a1.Close()
	waitFor(t, "slot release", func() bool { return e.hub.Connections() == 2 })
	if _, _, err := e.dialAs(t, "A"); err != nil {
		t.Fatalf("slot not released: %v", err)
	}
}

func TestRevokedSessionCloses4401(t *testing.T) {
	e := newEnv(t, func(h *Hub) { h.sessionEvery = 20 * time.Millisecond })
	c := e.dial(t)
	e.auth.revoke()
	expectClose(t, c, 4401)
}

func TestCommandWithRevokedSessionCloses4401(t *testing.T) {
	e := newEnv(t, nil)
	c := e.dial(t)
	e.auth.revoke()
	send(t, c, `{"version":1,"type":"console.command","channel":"`+channel+`","payload":{"command":"op me"}}`)
	expectClose(t, c, 4401)
	if len(e.console.commands) != 0 {
		t.Fatal("command sent with a revoked session")
	}
}

func TestShutdownClosesEveryConnection(t *testing.T) {
	e := newEnv(t, nil)
	conns := []*websocket.Conn{e.dial(t), e.dial(t)}
	for _, c := range conns {
		send(t, c, `{"version":1,"type":"subscribe","channel":"`+channel+`"}`)
		read(t, c) // subscribed
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := e.hub.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	for _, c := range conns {
		expectClose(t, c, websocket.CloseGoingAway)
	}
	if e.hub.Connections() != 0 {
		t.Fatalf("%d connections left", e.hub.Connections())
	}
	if subs, unsubs := e.console.counts(); subs != 2 || unsubs != 2 {
		t.Fatalf("subscriptions %d, unsubscribes %d", subs, unsubs)
	}
	_, resp, err := e.dialAs(t, "U1")
	if err == nil || resp == nil || resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("dial after shutdown = %v", resp)
	}
}
