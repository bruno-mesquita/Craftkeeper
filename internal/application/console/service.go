// Package console implements the live server console: output shared by every viewer of a
// server, commands written to the game's stdin, and status changes pushed from Docker events.
package console

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/bruno-mesquita/mesquita-panel/internal/application/servers"
	"github.com/bruno-mesquita/mesquita-panel/internal/domain"
)

// Runtime reaches a server's container. Both methods check that the container belongs to
// the server before touching it.
type Runtime interface {
	// FollowLogs blocks, calling emit for each line, until the container stops (nil), ctx is
	// cancelled or the stream fails.
	FollowLogs(ctx context.Context, id, serverID string, tail int, since time.Time, emit func(domain.ConsoleLine)) error
	SendInput(ctx context.Context, id, serverID, line string) error
}

// EventSource blocks, calling emit for each managed container event, until ctx is cancelled
// or the stream ends (always an error).
type EventSource interface {
	WatchEvents(ctx context.Context, emit func(domain.ContainerEvent)) error
}

// Servers resolves a server and its current status (servers.Service).
type Servers interface {
	Get(ctx context.Context, id string) (servers.View, error)
}

type AuditRecorder interface {
	Record(ctx context.Context, e domain.AuditEvent) error
}

const (
	// HistoryLines is how many recent lines a new viewer receives.
	HistoryLines = 500
	// auditCommandMax bounds the command text kept in the audit log.
	auditCommandMax = 200

	minRetry = 2 * time.Second
	maxRetry = 30 * time.Second
	// recheck is how often a feed whose stream ended looks at its container again, in case a
	// start event was missed.
	recheck = 30 * time.Second
)

// EventKind tells which field of an Event is set.
type EventKind int

const (
	EventHistory EventKind = iota + 1 // Lines: recent output, oldest first
	EventLine                         // Line: one new output line
	EventStatus                       // Status: the server's status changed
)

type Event struct {
	Kind   EventKind
	Lines  []domain.ConsoleLine
	Line   domain.ConsoleLine
	Status domain.ServerStatus
}

// Sink receives a subscriber's events. It is called with the service lock held and must
// never block: it should only enqueue.
type Sink func(Event)

type Service struct {
	runtime Runtime
	events  EventSource
	servers Servers
	audit   AuditRecorder
	log     *slog.Logger
	now     func() time.Time

	mu     sync.Mutex
	feeds  map[string]*feed // by server ID; exists while it has subscribers
	nextID int
	closed bool
	wg     sync.WaitGroup
}

// feed is the shared console of one server: one log follower for all its subscribers.
type feed struct {
	serverID string
	cancel   context.CancelFunc
	wake     chan struct{} // a start event: reopen the log stream now
	subs     map[int]Sink
	status   domain.ServerStatus

	history [HistoryLines]domain.ConsoleLine // ring buffer
	start   int
	count   int
	last    time.Time // timestamp of the newest line, to resume without duplicates
}

func NewService(runtime Runtime, events EventSource, srv Servers, audit AuditRecorder, log *slog.Logger) *Service {
	return &Service{runtime: runtime, events: events, servers: srv, audit: audit, log: log, now: time.Now,
		feeds: map[string]*feed{}}
}

// Subscribe attaches sink to a server's console. sink first receives the recent history and
// the current status, then new lines and status changes, until unsubscribe is called.
func (s *Service) Subscribe(ctx context.Context, serverID string, sink Sink) (unsubscribe func(), err error) {
	view, err := s.servers.Get(ctx, serverID)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("console: service closed")
	}
	f := s.feeds[serverID]
	if f == nil {
		fctx, cancel := context.WithCancel(context.Background())
		f = &feed{serverID: serverID, cancel: cancel, wake: make(chan struct{}, 1), subs: map[int]Sink{}, status: view.Status}
		s.feeds[serverID] = f
		s.wg.Add(1)
		go s.follow(fctx, f)
	} else if f.status != view.Status {
		f.status = view.Status // fresher than the feed's: tell the others too
		for _, other := range f.subs {
			other(Event{Kind: EventStatus, Status: view.Status})
		}
	}
	s.nextID++
	id := s.nextID
	f.subs[id] = sink
	sink(Event{Kind: EventHistory, Lines: f.snapshot()})
	sink(Event{Kind: EventStatus, Status: f.status})

	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			delete(f.subs, id)
			if len(f.subs) == 0 && s.feeds[serverID] == f {
				f.cancel()
				delete(s.feeds, serverID)
			}
		})
	}, nil
}

func (f *feed) snapshot() []domain.ConsoleLine {
	out := make([]domain.ConsoleLine, f.count)
	for i := range out {
		out[i] = f.history[(f.start+i)%HistoryLines]
	}
	return out
}

func (f *feed) push(l domain.ConsoleLine) {
	if f.count < HistoryLines {
		f.history[(f.start+f.count)%HistoryLines] = l
		f.count++
	} else {
		f.history[f.start] = l
		f.start = (f.start + 1) % HistoryLines
	}
	if !l.Time.IsZero() {
		f.last = l.Time
	}
}

// follow keeps the feed's log stream open while it has subscribers. A stream ends when the
// container stops; it is reopened on a start event (or the periodic recheck) from just after
// the last line received, so viewers never see a line twice.
func (s *Service) follow(ctx context.Context, f *feed) {
	defer s.wg.Done()
	retry := minRetry
	for {
		err := s.followOnce(ctx, f)
		if ctx.Err() != nil {
			return
		}
		wait := recheck
		if err != nil {
			s.log.Debug("console stream failed", "server_id", f.serverID, "err", err)
			wait, retry = retry, min(retry*2, maxRetry)
		} else {
			retry = minRetry
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-f.wake:
			t.Stop()
		case <-t.C:
		}
	}
}

func (s *Service) followOnce(ctx context.Context, f *feed) error {
	view, err := s.servers.Get(ctx, f.serverID)
	if err != nil {
		return err
	}
	switch view.Status {
	case domain.StatusMissing, domain.StatusCreating, domain.StatusCreateFailed: // no container (yet)
		return domain.ErrContainerMissing
	case domain.StatusUnknown:
		return domain.ErrDockerUnavailable
	}
	s.mu.Lock()
	tail, since := HistoryLines, time.Time{}
	if !f.last.IsZero() {
		tail, since = 0, f.last.Add(time.Nanosecond) // Docker's since is inclusive
	}
	s.mu.Unlock()
	return s.runtime.FollowLogs(ctx, view.ContainerID, view.ID, tail, since, func(l domain.ConsoleLine) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if ctx.Err() != nil {
			return // the feed was closed; its replacement has its own stream
		}
		f.push(l)
		for _, sink := range f.subs {
			sink(Event{Kind: EventLine, Line: l})
		}
	})
}

// Run follows Docker events until ctx is cancelled, pushing status changes of subscribed
// servers. Statuses are always re-derived (servers.Service.Get), never read from the event.
// After the event stream fails, it reconnects with backoff and resynchronizes every feed.
func (s *Service) Run(ctx context.Context) {
	retry := minRetry
	for first := true; ; first = false {
		if !first {
			s.resync(ctx)
		}
		connected := false
		err := s.events.WatchEvents(ctx, func(ev domain.ContainerEvent) {
			connected = true
			s.onEvent(ctx, ev)
		})
		if ctx.Err() != nil {
			return
		}
		if connected {
			retry = minRetry
		}
		s.log.Debug("docker event stream ended", "err", err, "retry_in", retry.String())
		t := time.NewTimer(retry)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		retry = min(retry*2, maxRetry)
	}
}

func (s *Service) onEvent(ctx context.Context, ev domain.ContainerEvent) {
	s.mu.Lock()
	f := s.feeds[ev.ServerID]
	s.mu.Unlock()
	if f == nil {
		return // nobody is watching this server
	}
	if ev.Action == "start" {
		select {
		case f.wake <- struct{}{}:
		default:
		}
	}
	s.refresh(ctx, f)
}

func (s *Service) resync(ctx context.Context) {
	s.mu.Lock()
	feeds := make([]*feed, 0, len(s.feeds))
	for _, f := range s.feeds {
		feeds = append(feeds, f)
	}
	s.mu.Unlock()
	for _, f := range feeds {
		select {
		case f.wake <- struct{}{}:
		default:
		}
		s.refresh(ctx, f)
	}
}

// refresh re-derives a feed's status and pushes it to its subscribers if it changed.
func (s *Service) refresh(ctx context.Context, f *feed) {
	view, err := s.servers.Get(ctx, f.serverID)
	status := view.Status
	switch {
	case errors.Is(err, domain.ErrNotFound):
		status = domain.StatusMissing // the server was deleted
	case err != nil:
		s.log.Warn("console: server status unavailable", "server_id", f.serverID, "err", err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if f.status == status {
		return
	}
	f.status = status
	for _, sink := range f.subs {
		sink(Event{Kind: EventStatus, Status: status})
	}
}

// SendCommand writes one command to a running server's console and audits it.
func (s *Service) SendCommand(ctx context.Context, serverID, command string, actor servers.Actor) error {
	if err := domain.ValidateConsoleCommand(command); err != nil {
		return err
	}
	view, err := s.servers.Get(ctx, serverID)
	if err != nil {
		return err
	}
	switch view.Status {
	case domain.StatusRunning, domain.StatusStarting:
	case domain.StatusUnknown:
		return domain.ErrDockerUnavailable
	case domain.StatusMissing:
		return domain.ErrContainerMissing
	default:
		return domain.ErrServerNotRunning
	}
	err = s.runtime.SendInput(ctx, view.ContainerID, view.ID, command)
	_ = s.audit.Record(context.WithoutCancel(ctx), domain.AuditEvent{
		CreatedAt: s.now().UTC(), UserID: actor.UserID, Action: domain.AuditConsoleCommand,
		Target: view.Name + ": " + truncate(command, auditCommandMax), IP: actor.IP, Success: err == nil,
	})
	return err
}

// Close stops every feed and waits for their goroutines. Later subscriptions fail.
func (s *Service) Close() {
	s.mu.Lock()
	s.closed = true
	for id, f := range s.feeds {
		f.cancel()
		delete(s.feeds, id)
	}
	s.mu.Unlock()
	s.wg.Wait()
}

// truncate cuts s to at most n bytes without splitting a rune.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && n < len(s) && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n] + "…"
}
