package servers

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"github.com/bruno-mesquita/mesquita-panel/internal/domain"
)

// Crash loop policy: a container that its restart policy (unless-stopped) restarts this many
// times within the window is stopped by the panel, and the server is shown as crashed.
const (
	crashLoopRestarts = 3
	crashLoopWindow   = 10 * time.Minute

	minEventRetry = 2 * time.Second
	maxEventRetry = 30 * time.Second
)

// restartTrack is what the crash guard knows about one server's automatic restarts.
type restartTrack struct {
	count int         // last RestartCount seen
	times []time.Time // when it grew, within the window
}

// Run follows Docker events until ctx is cancelled and stops servers stuck in a crash loop.
// The signal is the container's RestartCount, which Docker raises only for restarts made by
// the restart policy (never for a start, stop or restart requested through the API).
func (s *Service) Run(ctx context.Context, events EventSource, log *slog.Logger) {
	retry := minEventRetry
	for {
		connected := false
		err := events.WatchEvents(ctx, func(ev domain.ContainerEvent) {
			connected = true
			if ev.Action == "start" {
				s.observeStart(ctx, ev, log)
			}
		})
		if ctx.Err() != nil {
			return
		}
		if connected {
			retry = minEventRetry
		}
		log.Debug("crash guard: docker event stream ended", "err", err, "retry_in", retry.String())
		t := time.NewTimer(retry)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		retry = min(retry*2, maxEventRetry)
	}
}

// observeStart counts an automatic restart of a known server's container and stops the server
// once it reaches the crash loop threshold.
func (s *Service) observeStart(ctx context.Context, ev domain.ContainerEvent, log *slog.Logger) {
	x, err := s.repo.ByID(ctx, ev.ServerID)
	if err != nil || x.ContainerID == "" || x.ContainerID != ev.ContainerID {
		return // unknown server or a container that is not (or no longer) its own
	}
	st, err := s.runtime.Inspect(ctx, x.ContainerID, x.ID)
	if err != nil {
		if !errors.Is(err, domain.ErrContainerMissing) {
			log.Warn("crash guard: inspect failed", "server_id", x.ID, "err", err)
		}
		return
	}
	restarts, loop := s.countRestart(x.ID, st.RestartCount)
	if !loop {
		return
	}
	now := s.now().UTC().Truncate(time.Second)
	if err := s.repo.MarkCrashed(ctx, x.ID, now, restarts); err != nil {
		log.Error("crash guard: recording crash loop failed", "server_id", x.ID, "err", err)
		return
	}
	log.Warn("crash loop: stopping server", "server_id", x.ID, "restarts", restarts)
	// A graceful stop takes up to 75 s: do not hold up the event stream.
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		err := s.runtime.Stop(s.ctx, x.ContainerID, x.ID)
		if err != nil {
			log.Error("crash guard: stop failed", "server_id", x.ID, "err", err)
		}
		s.record(s.ctx, Actor{}, domain.AuditServerCrashLoop, x.Name+": "+strconv.Itoa(restarts)+" restarts", err == nil)
	}()
}

// countRestart records the RestartCount just observed for a server. It returns how many
// automatic restarts fall within the window and whether that makes a crash loop; on a loop
// the server's history is reset, so it is reported once.
func (s *Service) countRestart(serverID string, count int) (int, bool) {
	now := s.now()
	s.crashMu.Lock()
	defer s.crashMu.Unlock()
	t := s.crashes[serverID]
	if t == nil {
		t = &restartTrack{}
		s.crashes[serverID] = t
	}
	switch {
	case count < t.count: // the counter was reset by a start through the API
		t.count, t.times = count, nil
		return 0, false
	case count == t.count:
		return len(t.times), false
	}
	t.count = count
	t.times = append(t.times, now)
	kept := t.times[:0]
	for _, at := range t.times {
		if now.Sub(at) <= crashLoopWindow {
			kept = append(kept, at)
		}
	}
	t.times = kept
	if len(t.times) < crashLoopRestarts {
		return len(t.times), false
	}
	n := len(t.times)
	t.times = nil
	return n, true
}

// forgetRestarts drops a server's restart history (after a start through the panel, which
// also resets Docker's counter).
func (s *Service) forgetRestarts(serverID string) {
	s.crashMu.Lock()
	delete(s.crashes, serverID)
	s.crashMu.Unlock()
}
