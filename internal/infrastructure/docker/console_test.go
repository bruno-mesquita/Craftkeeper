package docker

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/bruno-mesquita/mesquita-panel/internal/domain"
)

// consoleDaemon fakes inspect, logs, attach and events. Unknown paths answer 404.
type consoleDaemon struct {
	labels   map[string]string // labels of idRunning
	logs     []byte            // multiplexed log body
	logQuery chan url.Values
	attach   string // "hijack" answers 101 and records stdin; anything else answers 200
	stdin    chan string
	events   string
}

func (d *consoleDaemon) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1.44")
	switch {
	case r.Method == http.MethodGet && path == "/containers/"+idRunning+"/json":
		io.WriteString(w, inspectJSON(idRunning, "running", d.labels))
	case r.Method == http.MethodGet && path == "/containers/"+idRunning+"/logs":
		d.logQuery <- r.URL.Query()
		w.Write(d.logs)
	case r.Method == http.MethodPost && path == "/containers/"+idRunning+"/attach":
		if d.attach != "hijack" || r.Header.Get("Upgrade") != "tcp" {
			w.WriteHeader(http.StatusOK)
			return
		}
		conn, buf, err := http.NewResponseController(w).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		buf.WriteString("HTTP/1.1 101 UPGRADED\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n")
		buf.Flush()
		b, _ := io.ReadAll(buf)
		d.stdin <- string(b)
	case r.Method == http.MethodGet && path == "/events":
		io.WriteString(w, d.events)
	default:
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"message":"No such container"}`)
	}
}

func newConsoleDaemon(t *testing.T) (*Runtime, *Client, *consoleDaemon) {
	d := &consoleDaemon{labels: ours(), logQuery: make(chan url.Values, 1), stdin: make(chan string, 1)}
	c := NewClient(startFake(t, d))
	return NewRuntime(c, 998, 997), c, d
}

func frame(kind byte, payload string) []byte {
	b := make([]byte, 8, 8+len(payload))
	b[0] = kind
	binary.BigEndian.PutUint32(b[4:], uint32(len(payload)))
	return append(b, payload...)
}

func TestLogsDemultiplexesLines(t *testing.T) {
	r, _, d := newConsoleDaemon(t)
	long := strings.Repeat("x", domain.ConsoleLineMaxBytes+100)
	var body []byte
	for _, f := range [][]byte{
		frame(1, "2026-11-17T10:00:00.5Z Starting server\n"),
		frame(2, "2026-11-17T10:00:01Z boom\r\n"),
		frame(0, "2026-11-17T10:00:01Z ignored stdin\n"),
		frame(1, "2026-11-17T10:00:02Z part one, "),
		frame(1, "2026-11-17T10:00:03Z part two\n"),
		frame(1, "2026-11-17T10:00:04Z a\nb\n"),
		frame(1, "2026-11-17T10:00:05Z "+long+"\n"),
		frame(1, "2026-11-17T10:00:06Z é"+strings.Repeat("y", domain.ConsoleLineMaxBytes-3)+"ç\n"),
		frame(1, "2026-11-17T10:00:07Z \n"),
		{1, 0, 0}, // truncated header at the end of the stream
	} {
		body = append(body, f...)
	}
	d.logs = body

	since := time.Date(2026, 11, 17, 10, 0, 0, 1, time.UTC)
	s, err := r.logs(context.Background(), idRunning, serverID, 0, since)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	q := <-d.logQuery
	if q.Get("follow") != "1" || q.Get("timestamps") != "1" || q.Get("stdout") != "1" || q.Get("stderr") != "1" ||
		q.Get("since") != "1794909600.000000001" || q.Get("tail") != "all" {
		t.Fatalf("query = %v", q)
	}

	want := []struct {
		stream domain.ConsoleStream
		text   string
		at     string
	}{
		{domain.StreamStdout, "Starting server", "2026-11-17T10:00:00.5Z"},
		{domain.StreamStderr, "boom", "2026-11-17T10:00:01Z"},
		{domain.StreamStdout, "part one, part two", "2026-11-17T10:00:02Z"},
		{domain.StreamStdout, "a", "2026-11-17T10:00:04Z"},
		{domain.StreamStdout, "b", "2026-11-17T10:00:04Z"},
		{domain.StreamStdout, long[:domain.ConsoleLineMaxBytes], "2026-11-17T10:00:05Z"},
		{domain.StreamStdout, "é" + strings.Repeat("y", domain.ConsoleLineMaxBytes-3), "2026-11-17T10:00:06Z"},
		{domain.StreamStdout, "", "2026-11-17T10:00:07Z"},
	}
	for i, w := range want {
		line, err := s.Next()
		if err != nil {
			t.Fatalf("line %d: %v", i, err)
		}
		at, _ := time.Parse(time.RFC3339Nano, w.at)
		if line.Stream != w.stream || line.Text != w.text || !line.Time.Equal(at) {
			t.Fatalf("line %d = %+v (len %d), want %s %q at %s", i, line.Stream, len(line.Text), w.stream, w.text, w.at)
		}
	}
	if _, err := s.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("after last line: %v, want EOF", err)
	}
}

func TestLogsTailAndOversizedFrame(t *testing.T) {
	r, _, d := newConsoleDaemon(t)
	b := make([]byte, 8)
	b[0] = 1
	binary.BigEndian.PutUint32(b[4:], maxLogFrame+1)
	d.logs = b

	s, err := r.logs(context.Background(), idRunning, serverID, 500, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if q := <-d.logQuery; q.Get("tail") != "500" || q.Has("since") {
		t.Fatalf("query = %v", q)
	}
	if _, err := s.Next(); err == nil || errors.Is(err, io.EOF) {
		t.Fatalf("oversized frame: %v, want an error", err)
	}
}

func TestConsoleRefusesForeignContainers(t *testing.T) {
	r, _, d := newConsoleDaemon(t)
	d.labels = map[string]string{LabelManaged: "true", LabelServerID: "OTHERSERVERIDOTHERSERVERID"}
	if _, err := r.logs(context.Background(), idRunning, serverID, 10, time.Time{}); !errors.Is(err, domain.ErrNotManaged) {
		t.Fatalf("Logs = %v, want ErrNotManaged", err)
	}
	if err := r.SendInput(context.Background(), idRunning, serverID, "op me"); !errors.Is(err, domain.ErrNotManaged) {
		t.Fatalf("SendInput = %v, want ErrNotManaged", err)
	}
	select {
	case q := <-d.logQuery:
		t.Fatalf("logs requested for a foreign container: %v", q)
	default:
	}
	if _, err := r.logs(context.Background(), "../../etc", serverID, 10, time.Time{}); !errors.Is(err, domain.ErrContainerMissing) {
		t.Fatalf("Logs with bad ID = %v", err)
	}
}

func TestSendInputWritesOneLine(t *testing.T) {
	r, _, d := newConsoleDaemon(t)
	d.attach = "hijack"
	if err := r.SendInput(context.Background(), idRunning, serverID, "say hello"); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-d.stdin:
		if got != "say hello\n" {
			t.Fatalf("stdin = %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("attach did not close")
	}

	if err := r.SendInput(context.Background(), idRunning, serverID, "a\nb"); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("multi-line input = %v", err)
	}
}

func TestSendInputRequiresUpgrade(t *testing.T) {
	r, _, d := newConsoleDaemon(t)
	d.attach = "plain"
	if err := r.SendInput(context.Background(), idRunning, serverID, "list"); !errors.Is(err, domain.ErrDockerFailed) {
		t.Fatalf("SendInput = %v, want ErrDockerFailed", err)
	}
}

func TestEventsKeepsOnlyManagedContainers(t *testing.T) {
	_, c, d := newConsoleDaemon(t)
	managed := `"Attributes":{"mesquita.managed":"true","mesquita.server_id":"` + serverID + `"}`
	d.events = strings.Join([]string{
		`{"Type":"container","Action":"start","Actor":{"ID":"` + idRunning + `",` + managed + `}}`,
		`not json`,
		`{"Type":"container","Action":"die","Actor":{"ID":"` + idForeign + `","Attributes":{"name":"crafty"}}}`,
		`{"Type":"container","Action":"die","Actor":{"ID":"../x",` + managed + `}}`,
		`{"Type":"image","Action":"pull","Actor":{"ID":"` + idRunning + `",` + managed + `}}`,
		`{"Type":"container","Action":"health_status: healthy","Actor":{"ID":"` + idRunning + `",` + managed + `}}`,
	}, "\n") + "\n"

	s, err := c.events(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, want := range []string{"start", "health_status"} {
		ev, err := s.Next()
		if err != nil {
			t.Fatal(err)
		}
		if ev.Action != want || ev.ContainerID != idRunning || ev.ServerID != serverID {
			t.Fatalf("event = %+v, want %s", ev, want)
		}
	}
	if _, err := s.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("end of stream = %v, want EOF", err)
	}
}

func TestEventsLineTooLong(t *testing.T) {
	_, c, d := newConsoleDaemon(t)
	d.events = `{"Type":"container","Action":"` + strings.Repeat("x", maxEventBytes) + `"}` + "\n"
	s, err := c.events(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Next(); !errors.Is(err, bufio.ErrTooLong) {
		t.Fatalf("Next = %v, want ErrTooLong", err)
	}
}

func TestStreamsReportDockerUnavailable(t *testing.T) {
	c := NewClient("/nonexistent/docker.sock")
	if _, err := c.events(context.Background()); !errors.Is(err, domain.ErrDockerUnavailable) {
		t.Fatalf("Events = %v, want ErrDockerUnavailable", err)
	}
}

func TestFollowLogsEmitsUntilStop(t *testing.T) {
	r, _, d := newConsoleDaemon(t)
	d.logs = append(frame(1, "2026-11-17T10:00:00Z one\n"), frame(2, "2026-11-17T10:00:01Z two\n")...)
	var got []string
	err := r.FollowLogs(context.Background(), idRunning, serverID, 500, time.Time{}, func(l domain.ConsoleLine) {
		got = append(got, string(l.Stream)+":"+l.Text)
	})
	<-d.logQuery
	if err != nil || strings.Join(got, ",") != "stdout:one,stderr:two" {
		t.Fatalf("FollowLogs = %v, lines %v", err, got)
	}
}

func TestWatchEventsEndIsAnError(t *testing.T) {
	_, c, d := newConsoleDaemon(t)
	d.events = `{"Type":"container","Action":"stop","Actor":{"ID":"` + idRunning + `","Attributes":{"mesquita.managed":"true"}}}` + "\n"
	var n int
	err := c.WatchEvents(context.Background(), func(domain.ContainerEvent) { n++ })
	if n != 1 || !errors.Is(err, domain.ErrDockerUnavailable) {
		t.Fatalf("WatchEvents = %v after %d events", err, n)
	}
}
