package websocket

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// echoServer upgrades every request and echoes text messages. The first Read error is sent
// on errs, so tests can check how the adapter classified it.
func echoServer(t *testing.T) (*httptest.Server, chan error) {
	t.Helper()
	errs := make(chan error, 1)
	up := NewUpgrader(func(w http.ResponseWriter, status int) {
		w.WriteHeader(status)
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			data, err := c.Read()
			if err != nil {
				errs <- err
				return
			}
			if string(data) == "close" {
				c.CloseWith(4401, "session expired")
				return
			}
			if err := c.Write(data); err != nil {
				errs <- err
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv, errs
}

func dial(t *testing.T, srv *httptest.Server, origin string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	h := http.Header{}
	if origin != "" {
		h.Set("Origin", origin)
	}
	c, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), h)
	if c != nil {
		t.Cleanup(func() { c.Close() })
	}
	return c, resp, err
}

func TestOriginIsRequiredAndMustMatch(t *testing.T) {
	srv, _ := echoServer(t)
	for _, origin := range []string{"", "http://evil.example", "null", "file://" + strings.TrimPrefix(srv.URL, "http://")} {
		_, resp, err := dial(t, srv, origin)
		if err == nil {
			t.Fatalf("origin %q: handshake succeeded", origin)
		}
		if resp == nil || resp.StatusCode != http.StatusForbidden {
			t.Fatalf("origin %q: got %v, want 403", origin, resp)
		}
	}
}

func TestEchoAndCloseCode(t *testing.T) {
	srv, _ := echoServer(t)
	c, _, err := dial(t, srv, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.WriteMessage(websocket.TextMessage, []byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	_, data, err := c.ReadMessage()
	if err != nil || string(data) != `{"a":1}` {
		t.Fatalf("echo = %q, %v", data, err)
	}
	c.WriteMessage(websocket.TextMessage, []byte("close"))
	_, _, err = c.ReadMessage()
	var ce *websocket.CloseError
	if !errors.As(err, &ce) || ce.Code != 4401 || ce.Text != "session expired" {
		t.Fatalf("close = %v, want 4401", err)
	}
}

func TestOversizedMessageCloses1009(t *testing.T) {
	srv, errs := echoServer(t)
	c, _, err := dial(t, srv, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	c.WriteMessage(websocket.TextMessage, []byte(strings.Repeat("x", MaxMessageBytes+1)))
	_, _, err = c.ReadMessage()
	var ce *websocket.CloseError
	if !errors.As(err, &ce) || ce.Code != websocket.CloseMessageTooBig {
		t.Fatalf("client got %v, want close 1009", err)
	}
	select {
	case err := <-errs:
		if !errors.Is(err, ErrTooLarge) {
			t.Fatalf("server Read error = %v, want ErrTooLarge", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not report the oversized message")
	}
}

func TestBinaryMessagesAreRejected(t *testing.T) {
	srv, errs := echoServer(t)
	c, _, err := dial(t, srv, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	c.WriteMessage(websocket.BinaryMessage, []byte{1, 2})
	select {
	case err := <-errs:
		if !errors.Is(err, ErrNotText) {
			t.Fatalf("Read error = %v, want ErrNotText", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no error for a binary message")
	}
}

func TestClientDisconnectIsErrClosed(t *testing.T) {
	srv, errs := echoServer(t)
	c, _, err := dial(t, srv, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	c.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
	select {
	case err := <-errs:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("Read error = %v, want ErrClosed", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no error after the client closed")
	}
}
