package servers

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/bruno-mesquita/mesquita-panel/internal/domain"
)

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

// crashEnv is a started server whose clock the test moves.
type crashEnv struct {
	*env
	v   View
	now time.Time
}

func newCrashEnv(t *testing.T) *crashEnv {
	t.Helper()
	e := &crashEnv{env: newEnv(t), now: time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)}
	e.svc.now = func() time.Time { return e.now }
	v, err := e.create(context.Background(), validInput("Modded"), e.actor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Start(context.Background(), v.ID, e.actor); err != nil {
		t.Fatal(err)
	}
	e.v = v
	return e
}

// autoRestart simulates the restart policy restarting the container after a crash: Docker
// raises RestartCount and emits a start event.
func (e *crashEnv) autoRestart(after time.Duration) {
	e.now = e.now.Add(after)
	e.rt.mu.Lock()
	c := e.rt.containers[e.v.ContainerID]
	c.RestartCount++
	c.State = "running"
	e.rt.mu.Unlock()
	e.svc.observeStart(context.Background(), domain.ContainerEvent{ContainerID: e.v.ContainerID, ServerID: e.v.ID, Action: "start"}, discard)
	e.svc.wg.Wait() // the stop runs in the background
}

func (e *crashEnv) stops() int {
	e.rt.mu.Lock()
	defer e.rt.mu.Unlock()
	n := 0
	for _, c := range e.rt.calls {
		if c == "stop" {
			n++
		}
	}
	return n
}

func TestCrashLoopStopsServerAndRecordsIt(t *testing.T) {
	e := newCrashEnv(t)
	ctx := context.Background()

	e.autoRestart(time.Minute)
	e.autoRestart(time.Minute)
	if e.stops() != 0 {
		t.Fatal("stopped before the threshold")
	}
	e.autoRestart(time.Minute)
	if e.stops() != 1 {
		t.Fatalf("stops = %d, want 1", e.stops())
	}
	v, err := e.svc.Get(ctx, e.v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if v.Status != domain.StatusCrashed || v.CrashRestarts != 3 || !v.CrashedAt.Equal(e.now) {
		t.Fatalf("view = %s, restarts %d, at %v", v.Status, v.CrashRestarts, v.CrashedAt)
	}
	events, _ := e.auditDB.Recent(ctx, 1)
	if len(events) != 1 || events[0].Action != domain.AuditServerCrashLoop || events[0].UserID != "" ||
		events[0].Target != "Modded: 3 restarts" || !events[0].Success {
		t.Fatalf("audit = %+v", events)
	}
	list, _ := e.svc.List(ctx)
	if list[0].Status != domain.StatusCrashed {
		t.Fatalf("list status = %s", list[0].Status)
	}
	if c, _ := e.svc.Counts(ctx); c.Stopped != 1 {
		t.Fatalf("counts = %+v", c)
	}

	// A start through the panel clears the record.
	if v, err := e.svc.Start(ctx, e.v.ID, e.actor); err != nil || v.Status != domain.StatusRunning {
		t.Fatalf("start = %s, %v", v.Status, err)
	}
	if _, err := e.svc.Stop(ctx, e.v.ID, e.actor); err != nil {
		t.Fatal(err)
	}
	if v, _ := e.svc.Get(ctx, e.v.ID); v.Status != domain.StatusStopped || !v.CrashedAt.IsZero() {
		t.Fatalf("after start+stop = %s, crashed at %v", v.Status, v.CrashedAt)
	}
}

func TestCrashLoopWindow(t *testing.T) {
	e := newCrashEnv(t)
	// Three restarts, but never three within 10 minutes.
	e.autoRestart(time.Minute)
	e.autoRestart(6 * time.Minute)
	e.autoRestart(6 * time.Minute)
	e.autoRestart(6 * time.Minute)
	if e.stops() != 0 {
		t.Fatalf("stops = %d, want 0", e.stops())
	}
	e.autoRestart(time.Minute) // now 3 within the window
	if e.stops() != 1 {
		t.Fatalf("stops = %d, want 1", e.stops())
	}
}

func TestCrashGuardIgnoresOtherEvents(t *testing.T) {
	e := newCrashEnv(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		e.now = e.now.Add(time.Second)
		// Same RestartCount (a start through the API): not an automatic restart.
		e.svc.observeStart(ctx, domain.ContainerEvent{ContainerID: e.v.ContainerID, ServerID: e.v.ID, Action: "start"}, discard)
		// A container that is not the server's own.
		e.svc.observeStart(ctx, domain.ContainerEvent{ContainerID: "other", ServerID: e.v.ID, Action: "start"}, discard)
		// An unknown server.
		e.svc.observeStart(ctx, domain.ContainerEvent{ContainerID: e.v.ContainerID, ServerID: "nope", Action: "start"}, discard)
	}
	if e.stops() != 0 {
		t.Fatalf("stops = %d", e.stops())
	}
}

func TestCountRestartResetsWhenCounterDrops(t *testing.T) {
	e := newEnv(t)
	for _, c := range []int{1, 2} {
		if _, loop := e.svc.countRestart("S", c); loop {
			t.Fatal("loop too early")
		}
	}
	if n, loop := e.svc.countRestart("S", 0); n != 0 || loop {
		t.Fatalf("after reset = %d, %v", n, loop)
	}
	for _, c := range []int{1, 2} {
		if _, loop := e.svc.countRestart("S", c); loop {
			t.Fatal("history survived the reset")
		}
	}
	if n, loop := e.svc.countRestart("S", 3); n != 3 || !loop {
		t.Fatalf("third = %d, %v", n, loop)
	}
}

// fakeEvents delivers a fixed list of events, signals sent, then blocks until ctx is cancelled.
type fakeEvents struct {
	events []domain.ContainerEvent
	sent   chan struct{}
}

func (f fakeEvents) WatchEvents(ctx context.Context, emit func(domain.ContainerEvent)) error {
	for _, ev := range f.events {
		emit(ev)
	}
	close(f.sent)
	<-ctx.Done()
	return ctx.Err()
}

func TestRunObservesOnlyStartEvents(t *testing.T) {
	e := newCrashEnv(t)
	e.rt.mu.Lock()
	e.rt.containers[e.v.ContainerID].RestartCount = 3
	e.rt.mu.Unlock()
	var evs []domain.ContainerEvent
	for _, a := range []string{"die", "start", "die", "stop"} {
		evs = append(evs, domain.ContainerEvent{ContainerID: e.v.ContainerID, ServerID: e.v.ID, Action: a})
	}
	src := fakeEvents{events: evs, sent: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.svc.Run(ctx, src, discard)
	}()
	<-src.sent
	cancel()
	<-done
	e.svc.wg.Wait()

	e.svc.crashMu.Lock()
	track := e.svc.crashes[e.v.ID]
	e.svc.crashMu.Unlock()
	// One start event: the counter is seen once, as a single automatic restart.
	if track == nil || track.count != 3 || len(track.times) != 1 {
		t.Fatalf("track = %+v", track)
	}
	if e.stops() != 0 {
		t.Fatalf("stops = %d", e.stops())
	}
}
