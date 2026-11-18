package console

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bruno-mesquita/mesquita-panel/internal/application/servers"
	"github.com/bruno-mesquita/mesquita-panel/internal/domain"
)

const (
	sid = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	cid = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

type fakeServers struct {
	mu     sync.Mutex
	status map[string]domain.ServerStatus
	gets   int
}

func (f *fakeServers) Get(_ context.Context, id string) (servers.View, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gets++
	st, ok := f.status[id]
	if !ok {
		return servers.View{}, domain.ErrNotFound
	}
	return servers.View{Server: domain.Server{ID: id, Name: "Survival", ContainerID: cid}, Status: st}, nil
}

func (f *fakeServers) set(id string, st domain.ServerStatus) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status[id] = st
}

func (f *fakeServers) getCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gets
}

// followCall is one FollowLogs call; the test feeds it lines and ends it.
type followCall struct {
	tail  int
	since time.Time
	lines chan domain.ConsoleLine
	end   chan error
	done  chan struct{} // closed when FollowLogs returned
}

type fakeRuntime struct {
	calls   chan *followCall
	inputs  chan string
	sendErr error
}

func (r *fakeRuntime) FollowLogs(ctx context.Context, id, serverID string, tail int, since time.Time, emit func(domain.ConsoleLine)) error {
	c := &followCall{tail: tail, since: since, lines: make(chan domain.ConsoleLine), end: make(chan error), done: make(chan struct{})}
	defer close(c.done)
	if id != cid || serverID != sid {
		return errors.New("wrong container")
	}
	r.calls <- c
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case l := <-c.lines:
			emit(l)
		case err := <-c.end:
			return err
		}
	}
}

func (r *fakeRuntime) SendInput(_ context.Context, id, serverID, line string) error {
	if id != cid || serverID != sid {
		return errors.New("wrong container")
	}
	r.inputs <- line
	return r.sendErr
}

type fakeEvents struct {
	events chan domain.ContainerEvent
	fail   chan error
	starts chan struct{} // one per WatchEvents call
}

func (e *fakeEvents) WatchEvents(ctx context.Context, emit func(domain.ContainerEvent)) error {
	e.starts <- struct{}{}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev := <-e.events:
			emit(ev)
		case err := <-e.fail:
			return err
		}
	}
}

type fakeAudit struct {
	mu     sync.Mutex
	events []domain.AuditEvent
}

func (a *fakeAudit) Record(_ context.Context, e domain.AuditEvent) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, e)
	return nil
}

type harness struct {
	svc     *Service
	servers *fakeServers
	runtime *fakeRuntime
	events  *fakeEvents
	audit   *fakeAudit
}

func newHarness(t *testing.T) *harness {
	h := &harness{
		servers: &fakeServers{status: map[string]domain.ServerStatus{sid: domain.StatusRunning}},
		runtime: &fakeRuntime{calls: make(chan *followCall, 4), inputs: make(chan string, 4)},
		events:  &fakeEvents{events: make(chan domain.ContainerEvent), fail: make(chan error), starts: make(chan struct{}, 4)},
		audit:   &fakeAudit{},
	}
	h.svc = NewService(h.runtime, h.events, h.servers, h.audit, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(h.svc.Close)
	return h
}

// collector is a non-blocking sink with a generous buffer.
type collector chan Event

func (c collector) sink(e Event) { c <- e }

func (c collector) next(t *testing.T) Event {
	t.Helper()
	select {
	case e := <-c:
		return e
	case <-time.After(5 * time.Second):
		t.Fatal("no event")
		return Event{}
	}
}

func (c collector) none(t *testing.T) {
	t.Helper()
	select {
	case e := <-c:
		t.Fatalf("unexpected event %+v", e)
	case <-time.After(50 * time.Millisecond):
	}
}

func (h *harness) subscribe(t *testing.T) (collector, func()) {
	t.Helper()
	c := make(collector, 1024)
	unsub, err := h.svc.Subscribe(context.Background(), sid, c.sink)
	if err != nil {
		t.Fatal(err)
	}
	return c, unsub
}

func (h *harness) nextCall(t *testing.T) *followCall {
	t.Helper()
	select {
	case c := <-h.runtime.calls:
		return c
	case <-time.After(5 * time.Second):
		t.Fatal("FollowLogs was not called")
		return nil
	}
}

func line(text string, sec int) domain.ConsoleLine {
	return domain.ConsoleLine{Time: time.Date(2026, 11, 17, 10, 0, sec, 0, time.UTC), Stream: domain.StreamStdout, Text: text}
}

func TestSubscribeUnknownServer(t *testing.T) {
	h := newHarness(t)
	if _, err := h.svc.Subscribe(context.Background(), "ZZZZZZZZZZZZZZZZZZZZZZZZZZ", func(Event) {}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Subscribe = %v, want ErrNotFound", err)
	}
}

func TestSubscribersShareOneFeed(t *testing.T) {
	h := newHarness(t)
	a, unsubA := h.subscribe(t)
	if e := a.next(t); e.Kind != EventHistory || len(e.Lines) != 0 {
		t.Fatalf("first event = %+v", e)
	}
	if e := a.next(t); e.Kind != EventStatus || e.Status != domain.StatusRunning {
		t.Fatalf("second event = %+v", e)
	}
	call := h.nextCall(t)
	if call.tail != HistoryLines || !call.since.IsZero() {
		t.Fatalf("first stream: tail %d since %v", call.tail, call.since)
	}
	call.lines <- line("one", 1)
	call.lines <- line("two", 2)
	for _, want := range []string{"one", "two"} {
		if e := a.next(t); e.Kind != EventLine || e.Line.Text != want {
			t.Fatalf("event = %+v, want line %q", e, want)
		}
	}

	b, unsubB := h.subscribe(t)
	if e := b.next(t); e.Kind != EventHistory || len(e.Lines) != 2 || e.Lines[0].Text != "one" {
		t.Fatalf("history = %+v", e)
	}
	b.next(t) // status
	select {
	case <-h.runtime.calls:
		t.Fatal("a second subscriber opened a second stream")
	case <-time.After(50 * time.Millisecond):
	}
	call.lines <- line("three", 3)
	if a.next(t).Line.Text != "three" || b.next(t).Line.Text != "three" {
		t.Fatal("line not delivered to both subscribers")
	}

	unsubA()
	unsubA() // idempotent
	select {
	case <-call.done:
		t.Fatal("stream closed while a subscriber remains")
	case <-time.After(50 * time.Millisecond):
	}
	unsubB()
	select {
	case <-call.done:
	case <-time.After(5 * time.Second):
		t.Fatal("stream not closed after the last subscriber left")
	}
}

func TestHistoryIsBounded(t *testing.T) {
	h := newHarness(t)
	_, unsub := h.subscribe(t)
	defer unsub()
	call := h.nextCall(t)
	for i := range HistoryLines + 100 {
		call.lines <- domain.ConsoleLine{Text: strings.Repeat("x", i%7) + string(rune('a'+i%26)), Time: time.Unix(int64(i), 0)}
	}
	c, unsub2 := h.subscribe(t)
	defer unsub2()
	e := c.next(t)
	if len(e.Lines) != HistoryLines || !e.Lines[0].Time.Equal(time.Unix(100, 0)) || !e.Lines[HistoryLines-1].Time.Equal(time.Unix(HistoryLines+99, 0)) {
		t.Fatalf("history: %d lines, first %v", len(e.Lines), e.Lines[0].Time)
	}
}

func TestStartEventReopensAfterLastLine(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go h.svc.Run(ctx)
	<-h.events.starts

	c, unsub := h.subscribe(t)
	defer unsub()
	c.next(t)
	c.next(t)
	call := h.nextCall(t)
	call.lines <- line("bye", 5)
	c.next(t)
	call.end <- nil // the container stopped
	h.servers.set(sid, domain.StatusStopped)
	h.events.events <- domain.ContainerEvent{ContainerID: cid, ServerID: sid, Action: "die"}
	if e := c.next(t); e.Kind != EventStatus || e.Status != domain.StatusStopped {
		t.Fatalf("event = %+v, want status stopped", e)
	}

	h.servers.set(sid, domain.StatusStarting)
	h.events.events <- domain.ContainerEvent{ContainerID: cid, ServerID: sid, Action: "start"}
	if e := c.next(t); e.Kind != EventStatus || e.Status != domain.StatusStarting {
		t.Fatalf("event = %+v, want status starting", e)
	}
	call = h.nextCall(t)
	if call.tail != 0 || !call.since.Equal(line("", 5).Time.Add(time.Nanosecond)) {
		t.Fatalf("reopened with tail %d since %v", call.tail, call.since)
	}

	// Same status again: nothing is pushed.
	h.events.events <- domain.ContainerEvent{ContainerID: cid, ServerID: sid, Action: "health_status"}
	c.none(t)
}

func TestEventsForUnwatchedServersAreIgnored(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go h.svc.Run(ctx)
	<-h.events.starts
	before := h.servers.getCount()
	h.events.events <- domain.ContainerEvent{ContainerID: cid, ServerID: sid, Action: "die"}
	h.events.events <- domain.ContainerEvent{ContainerID: cid, ServerID: sid, Action: "start"} // processed after the first
	if n := h.servers.getCount(); n != before {
		t.Fatalf("status looked up %d times for an unwatched server", n-before)
	}
}

func TestDeletedServerReportsMissing(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go h.svc.Run(ctx)
	<-h.events.starts
	c, unsub := h.subscribe(t)
	defer unsub()
	c.next(t)
	c.next(t)
	h.servers.mu.Lock()
	delete(h.servers.status, sid)
	h.servers.mu.Unlock()
	h.events.events <- domain.ContainerEvent{ContainerID: cid, ServerID: sid, Action: "destroy"}
	if e := c.next(t); e.Kind != EventStatus || e.Status != domain.StatusMissing {
		t.Fatalf("event = %+v, want missing", e)
	}
}

func TestEventStreamFailureResyncs(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go h.svc.Run(ctx)
	<-h.events.starts
	c, unsub := h.subscribe(t)
	defer unsub()
	c.next(t)
	c.next(t)
	h.servers.set(sid, domain.StatusStopped) // changed while the stream is down
	h.events.fail <- domain.ErrDockerUnavailable
	select {
	case <-h.events.starts:
	case <-time.After(10 * time.Second):
		t.Fatal("event stream not reconnected")
	}
	if e := c.next(t); e.Kind != EventStatus || e.Status != domain.StatusStopped {
		t.Fatalf("event = %+v, want resynced status", e)
	}
}

func TestSendCommand(t *testing.T) {
	h := newHarness(t)
	actor := servers.Actor{UserID: "U1", IP: "100.64.0.1"}
	ctx := context.Background()

	var ve *domain.ValidationError
	for _, bad := range []string{"", "say a\nop me", "say \x1b[2J", strings.Repeat("x", domain.ConsoleCommandMaxBytes+1), "\xff"} {
		if err := h.svc.SendCommand(ctx, sid, bad, actor); !errors.As(err, &ve) {
			t.Fatalf("command %q: %v, want validation error", bad, err)
		}
	}
	if err := h.svc.SendCommand(ctx, "ZZZZZZZZZZZZZZZZZZZZZZZZZZ", "list", actor); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown server: %v", err)
	}
	for st, want := range map[domain.ServerStatus]error{
		domain.StatusStopped:    domain.ErrServerNotRunning,
		domain.StatusRestarting: domain.ErrServerNotRunning,
		domain.StatusMissing:    domain.ErrContainerMissing,
		domain.StatusUnknown:    domain.ErrDockerUnavailable,
	} {
		h.servers.set(sid, st)
		if err := h.svc.SendCommand(ctx, sid, "list", actor); !errors.Is(err, want) {
			t.Fatalf("status %s: %v, want %v", st, err, want)
		}
	}
	if len(h.audit.events) != 0 {
		t.Fatalf("rejected commands were audited: %+v", h.audit.events)
	}

	h.servers.set(sid, domain.StatusRunning)
	if err := h.svc.SendCommand(ctx, sid, "say olá", actor); err != nil {
		t.Fatal(err)
	}
	if got := <-h.runtime.inputs; got != "say olá" {
		t.Fatalf("input = %q", got)
	}
	h.runtime.sendErr = domain.ErrDockerFailed
	long := "say " + strings.Repeat("é", 300)
	if err := h.svc.SendCommand(ctx, sid, long, actor); !errors.Is(err, domain.ErrDockerFailed) {
		t.Fatalf("failed send: %v", err)
	}
	<-h.runtime.inputs

	if len(h.audit.events) != 2 {
		t.Fatalf("audit = %+v", h.audit.events)
	}
	ok, failed := h.audit.events[0], h.audit.events[1]
	if ok.Action != domain.AuditConsoleCommand || ok.Target != "say olá" || ok.ServerID != sid || !ok.Success ||
		ok.UserID != "U1" || ok.IP != "100.64.0.1" {
		t.Fatalf("audit = %+v", ok)
	}
	if failed.Success || len(failed.Target) > auditCommandMax+len("…") || !strings.HasSuffix(failed.Target, "…") {
		t.Fatalf("failed audit = %+v (len %d)", failed, len(failed.Target))
	}
}

func TestSendIsNotAudited(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.servers.set(sid, domain.StatusStopped)
	if err := h.svc.Send(ctx, sid, "save-off"); !errors.Is(err, domain.ErrServerNotRunning) {
		t.Fatalf("stopped server: %v", err)
	}
	h.servers.set(sid, domain.StatusRunning)
	if err := h.svc.Send(ctx, sid, "save-off"); err != nil {
		t.Fatal(err)
	}
	if got := <-h.runtime.inputs; got != "save-off" {
		t.Fatalf("input = %q", got)
	}
	if len(h.audit.events) != 0 {
		t.Fatalf("panel command was audited: %+v", h.audit.events)
	}
}

func TestCloseStopsFeeds(t *testing.T) {
	h := newHarness(t)
	_, _ = h.subscribe(t)
	call := h.nextCall(t)
	h.svc.Close()
	select {
	case <-call.done:
	default:
		t.Fatal("Close returned before the stream ended")
	}
	if _, err := h.svc.Subscribe(context.Background(), sid, func(Event) {}); err == nil {
		t.Fatal("Subscribe after Close succeeded")
	}
}
