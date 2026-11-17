package docker

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bruno-mesquita/mesquita-panel/internal/domain"
)

const (
	// maxLogFrame bounds one multiplexed frame. json-file splits messages at 16 KiB, so a
	// larger frame means a broken stream.
	maxLogFrame = 1 << 20
	// maxEventBytes bounds one JSON event line.
	maxEventBytes = 64 << 10
	attachTimeout = 5 * time.Second
)

// Container lifecycle events the console follows (docs/WEBSOCKET.md, server.status).
var eventsFilter = url.Values{
	"filters": {`{"type":["container"],"label":["` + LabelManaged + `=` + managedLabelValue + `"],` +
		`"event":["start","die","stop","restart","destroy","health_status"]}`},
}.Encode()

// FollowLogs calls emit with each output line of a server's container, after checking that
// it belongs to the server, until the container stops (nil), ctx is cancelled (ctx.Err()) or
// the stream fails. tail > 0 starts with the last tail lines; a non-zero since starts at that
// time (inclusive).
func (r *Runtime) FollowLogs(ctx context.Context, id, serverID string, tail int, since time.Time, emit func(domain.ConsoleLine)) error {
	s, err := r.logs(ctx, id, serverID, tail, since)
	if err != nil {
		return err
	}
	defer s.Close()
	for {
		line, err := s.Next()
		switch {
		case ctx.Err() != nil:
			return ctx.Err()
		case errors.Is(err, io.EOF):
			return nil
		case err != nil:
			return fmt.Errorf("%w: follow logs: %v", domain.ErrDockerFailed, err)
		}
		emit(line)
	}
}

// logs opens the log stream of FollowLogs.
func (r *Runtime) logs(ctx context.Context, id, serverID string, tail int, since time.Time) (*logStream, error) {
	if _, err := r.Inspect(ctx, id, serverID); err != nil {
		return nil, err
	}
	q := url.Values{"follow": {"1"}, "stdout": {"1"}, "stderr": {"1"}, "timestamps": {"1"}}
	if tail > 0 {
		q.Set("tail", strconv.Itoa(tail))
	} else {
		q.Set("tail", "all")
	}
	if !since.IsZero() {
		q.Set("since", fmt.Sprintf("%d.%09d", since.Unix(), since.Nanosecond()))
	}
	resp, err := r.client.stream(ctx, http.MethodGet, "/containers/"+id+"/logs?"+q.Encode(), nil)
	if err != nil {
		return nil, mapError("follow logs", err)
	}
	return &logStream{body: resp.Body, r: bufio.NewReader(resp.Body)}, nil
}

// logStream demultiplexes Docker's stdout/stderr stream (non-TTY containers) into lines.
type logStream struct {
	body    io.ReadCloser
	r       *bufio.Reader
	partial [3]partialLine // per stream type (1 = stdout, 2 = stderr)
	ready   []domain.ConsoleLine
}

type partialLine struct {
	time      time.Time
	text      []byte
	truncated bool
}

// Next returns the next complete line. Lines longer than domain.ConsoleLineMaxBytes are
// truncated. It returns io.EOF when the stream ends.
func (s *logStream) Next() (domain.ConsoleLine, error) {
	for len(s.ready) == 0 {
		if err := s.readFrame(); err != nil {
			return domain.ConsoleLine{}, err
		}
	}
	line := s.ready[0]
	s.ready = s.ready[1:]
	return line, nil
}

func (s *logStream) Close() error { return s.body.Close() }

// readFrame reads one frame: an 8-byte header (stream type, 3 zero bytes, big-endian size)
// followed by the payload, which starts with an RFC 3339 timestamp and a space.
func (s *logStream) readFrame() error {
	var hdr [8]byte
	if _, err := io.ReadFull(s.r, hdr[:]); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return io.EOF
		}
		return err
	}
	size := binary.BigEndian.Uint32(hdr[4:])
	if size > maxLogFrame {
		return fmt.Errorf("docker: log frame of %d bytes", size)
	}
	payload := make([]byte, size)
	if _, err := io.ReadFull(s.r, payload); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return io.EOF
		}
		return err
	}
	kind := hdr[0]
	if kind != 1 && kind != 2 {
		return nil // stdin echo (type 0) never carries output
	}
	stream := domain.StreamStdout
	if kind == 2 {
		stream = domain.StreamStderr
	}

	at := time.Time{}
	if i := strings.IndexByte(string(payload[:min(len(payload), 40)]), ' '); i > 0 {
		if t, err := time.Parse(time.RFC3339Nano, string(payload[:i])); err == nil {
			at, payload = t, payload[i+1:]
		}
	}

	p := &s.partial[kind]
	for len(payload) > 0 {
		if len(p.text) == 0 && !p.truncated {
			p.time = at
		}
		nl := strings.IndexByte(string(payload), '\n')
		chunk := payload
		if nl >= 0 {
			chunk = payload[:nl]
		}
		if room := domain.ConsoleLineMaxBytes - len(p.text); len(chunk) > room {
			chunk, p.truncated = chunk[:room], true
		}
		p.text = append(p.text, chunk...)
		if nl < 0 {
			break // partial message: Docker sends the rest in the next frame
		}
		s.ready = append(s.ready, domain.ConsoleLine{Time: p.time, Stream: stream, Text: lineText(p.text)})
		*p = partialLine{}
		payload = payload[nl+1:]
	}
	return nil
}

// lineText drops a trailing carriage return and any rune cut by truncation.
func lineText(b []byte) string {
	b = []byte(strings.TrimSuffix(string(b), "\r"))
	for i := 0; i < utf8.UTFMax && len(b) > 0 && !utf8.Valid(b); i++ {
		r, size := utf8.DecodeLastRune(b)
		if r != utf8.RuneError || size != 1 {
			break
		}
		b = b[:len(b)-1]
	}
	return string(b)
}

// SendInput writes one line to the stdin of a server's container (created with OpenStdin)
// through a short-lived attach. Closing the attach does not close the container's stdin.
func (r *Runtime) SendInput(ctx context.Context, id, serverID, line string) error {
	if strings.ContainsAny(line, "\r\n") {
		return fmt.Errorf("%w: input must be a single line", domain.ErrInvalidInput)
	}
	if _, err := r.Inspect(ctx, id, serverID); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, attachTimeout)
	defer cancel()
	resp, err := r.client.stream(ctx, http.MethodPost, "/containers/"+id+"/attach?stream=1&stdin=1",
		http.Header{"Connection": {"Upgrade"}, "Upgrade": {"tcp"}})
	if err != nil {
		return mapError("attach container", err)
	}
	defer resp.Body.Close()
	conn, ok := resp.Body.(io.Writer)
	if resp.StatusCode != http.StatusSwitchingProtocols || !ok {
		return fmt.Errorf("%w: attach container: unexpected status %d", domain.ErrDockerFailed, resp.StatusCode)
	}
	stop := context.AfterFunc(ctx, func() { resp.Body.Close() })
	defer stop()
	if _, err := io.WriteString(conn, line+"\n"); err != nil {
		return fmt.Errorf("%w: attach container: %v", domain.ErrDockerFailed, err)
	}
	return nil
}

// WatchEvents calls emit with each lifecycle event of a managed container until ctx is
// cancelled (ctx.Err()) or the stream ends or fails.
func (c *Client) WatchEvents(ctx context.Context, emit func(domain.ContainerEvent)) error {
	s, err := c.events(ctx)
	if err != nil {
		return err
	}
	defer s.Close()
	for {
		ev, err := s.Next()
		switch {
		case ctx.Err() != nil:
			return ctx.Err()
		case errors.Is(err, io.EOF):
			return fmt.Errorf("%w: event stream ended", domain.ErrDockerUnavailable)
		case err != nil:
			return fmt.Errorf("%w: watch events: %v", domain.ErrDockerFailed, err)
		}
		emit(ev)
	}
}

// events opens the event stream of WatchEvents.
func (c *Client) events(ctx context.Context) (*eventStream, error) {
	resp, err := c.stream(ctx, http.MethodGet, "/events?"+eventsFilter, nil)
	if err != nil {
		return nil, mapError("watch events", err)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 4<<10), maxEventBytes)
	return &eventStream{body: resp.Body, sc: sc}, nil
}

// eventStream decodes the daemon's newline-delimited JSON events.
type eventStream struct {
	body io.ReadCloser
	sc   *bufio.Scanner
}

type eventMessage struct {
	Type   string `json:"Type"`
	Action string `json:"Action"`
	Actor  struct {
		ID         string            `json:"ID"`
		Attributes map[string]string `json:"Attributes"`
	} `json:"Actor"`
}

// Next returns the next event of a managed container. The daemon-side filter is not trusted
// alone: type, label and ID format are re-checked and other events skipped. It returns
// io.EOF when the daemon ends the stream.
func (s *eventStream) Next() (domain.ContainerEvent, error) {
	for s.sc.Scan() {
		var m eventMessage
		if json.Unmarshal(s.sc.Bytes(), &m) != nil {
			continue
		}
		if m.Type != "container" || m.Actor.Attributes[LabelManaged] != managedLabelValue || !validContainerID(m.Actor.ID) {
			continue
		}
		action, _, _ := strings.Cut(m.Action, ":") // "health_status: healthy"
		return domain.ContainerEvent{ContainerID: m.Actor.ID, ServerID: m.Actor.Attributes[LabelServerID], Action: action}, nil
	}
	if err := s.sc.Err(); err != nil {
		return domain.ContainerEvent{}, err
	}
	return domain.ContainerEvent{}, io.EOF
}

func (s *eventStream) Close() error { return s.body.Close() }
