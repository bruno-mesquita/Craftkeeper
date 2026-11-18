package backups

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/bruno-mesquita/mesquita-panel/internal/application/console"
	"github.com/bruno-mesquita/mesquita-panel/internal/application/servers"
	"github.com/bruno-mesquita/mesquita-panel/internal/domain"
)

const (
	// reserveBytes is the free space a backup or restore must leave on the disk, which is
	// shared with the host's other services.
	reserveBytes = 2 << 30
	// saveTimeout bounds the wait for a running game to confirm "save-all flush".
	saveTimeout = 60 * time.Second
	// saveOnTimeout bounds turning saving back on, also during shutdown.
	saveOnTimeout = 10 * time.Second
	// schedulerTick is how often due schedules are looked for.
	schedulerTick = time.Minute
	// savedMarker is what Minecraft (Vanilla, Paper and forks) prints when "save-all" is done.
	savedMarker = "Saved the game"
)

// Chat messages shown to players during an online backup.
const (
	chatStart  = "[Backup] Starting server backup..."
	chatDone   = "[Backup] Backup complete."
	chatFailed = "[Backup] Backup failed; automatic saving is back on."
)

// tellraw is the console command that shows msg in gray to every player.
func tellraw(msg string) string {
	b, _ := json.Marshal(map[string]string{"text": msg, "color": "gray"})
	return "tellraw @a " + string(b)
}

// errSaveTimeout: the game never confirmed its save, so the files may be inconsistent.
var errSaveTimeout = errors.New("backups: the game did not confirm saving in time")

// Operations that hold a server exclusively.
const (
	opBackup  = "backup"
	opRestore = "restore"
)

type Service struct {
	repo    Repository
	store   Store
	servers Servers
	console Console
	audit   AuditRecorder
	log     *slog.Logger
	now     func() time.Time

	reserve     int64
	saveTimeout time.Duration
	tick        time.Duration

	// slot lets one archive be written at a time: disk and CPU are shared with production.
	slot chan struct{}
	// ctx is the lifetime of backup jobs; Close cancels it.
	ctx    context.Context
	cancel context.CancelFunc

	mu     sync.Mutex
	busy   map[string]string // server ID → opBackup or opRestore
	closed bool
	wg     sync.WaitGroup // backup jobs
}

func NewService(repo Repository, store Store, srv Servers, con Console, audit AuditRecorder, log *slog.Logger) *Service {
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{repo: repo, store: store, servers: srv, console: con, audit: audit, log: log, now: time.Now,
		reserve: reserveBytes, saveTimeout: saveTimeout, tick: schedulerTick,
		slot: make(chan struct{}, 1), ctx: ctx, cancel: cancel, busy: map[string]string{}}
}

// Recover cleans up after an unclean stop: backups left "creating" become failed, and
// temporary archives and restore staging directories are removed. Call it before serving.
func (s *Service) Recover(ctx context.Context) error {
	if err := s.repo.FailInterrupted(ctx, s.now().UTC()); err != nil {
		return fmt.Errorf("backups: recover: %w", err)
	}
	if err := s.store.CleanupStale(); err != nil {
		s.log.Warn("backups: leftover temporary files not removed", "err", err)
	}
	return nil
}

// Close stops accepting work, cancels running backups (they end as "interrupted") and waits
// for them.
func (s *Service) Close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.cancel()
	s.wg.Wait()
}

// acquire holds a server for op. A backup job is also counted in wg, under the same lock
// that Close takes, so Close never misses a job.
func (s *Service) acquire(serverID, op string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("backups: service closed")
	}
	if s.busy[serverID] != "" {
		return domain.ErrBackupInProgress
	}
	s.busy[serverID] = op
	if op == opBackup {
		s.wg.Add(1)
	}
	return nil
}

func (s *Service) release(serverID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.busy, serverID)
}

func (s *Service) busyWith(serverID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.busy[serverID]
}

// List returns a server's backups, newest first.
func (s *Service) List(ctx context.Context, serverID string) ([]domain.Backup, error) {
	if _, err := s.servers.Get(ctx, serverID); err != nil {
		return nil, err
	}
	return s.repo.List(ctx, serverID)
}

// Create starts a manual backup and returns it while it is still being made ("creating").
func (s *Service) Create(ctx context.Context, serverID string, actor servers.Actor) (domain.Backup, error) {
	return s.create(ctx, serverID, domain.BackupManual, actor)
}

func (s *Service) create(ctx context.Context, serverID string, kind domain.BackupKind, actor servers.Actor) (domain.Backup, error) {
	view, err := s.servers.Get(ctx, serverID)
	if err != nil {
		return domain.Backup{}, err
	}
	if _, err := backupMode(view.Status); err != nil {
		return domain.Backup{}, err
	}
	if err := s.checkSpace(serverID); err != nil {
		return domain.Backup{}, err
	}
	if err := s.acquire(serverID, opBackup); err != nil {
		return domain.Backup{}, err
	}
	b := domain.Backup{ID: rand.Text(), ServerID: serverID, Kind: kind, Status: domain.BackupCreating,
		CreatedAt: s.now().UTC().Truncate(time.Second)}
	if err := s.repo.Create(ctx, b); err != nil {
		s.release(serverID)
		s.wg.Done()
		return domain.Backup{}, err
	}
	go s.run(b, view.Name, actor)
	return b, nil
}

// backupMode tells whether a server in status st is backed up online, with saving paused.
func backupMode(st domain.ServerStatus) (online bool, err error) {
	switch st {
	case domain.StatusRunning:
		return true, nil
	case domain.StatusStopped, domain.StatusMissing:
		return false, nil
	case domain.StatusStarting, domain.StatusRestarting, domain.StatusCreating:
		return false, domain.ErrServerBusy
	case domain.StatusCreateFailed:
		return false, domain.ErrContainerMissing
	}
	return false, domain.ErrDockerUnavailable
}

// checkSpace refuses a backup that could eat into the reserve: the archive is at most the
// size of the server's files.
func (s *Service) checkSpace(serverID string) error {
	dir, free, err := s.store.Usage(serverID)
	if err != nil {
		return err
	}
	if free-s.reserve < dir {
		return fmt.Errorf("%w: backup needs up to %d bytes, %d free beyond the reserve",
			domain.ErrInsufficientStorage, dir, free-s.reserve)
	}
	return nil
}

// run makes backup b and stores the outcome. It owns the server's busy mark.
func (s *Service) run(b domain.Backup, serverName string, actor servers.Actor) {
	defer s.wg.Done()
	defer s.release(b.ServerID)

	size, err := s.write(s.ctx, b, actor)
	ctx := context.WithoutCancel(s.ctx)
	b.CompletedAt = s.now().UTC().Truncate(time.Second)
	if err == nil {
		b.Status, b.SizeBytes = domain.BackupReady, size
	} else {
		b.Status, b.Failure = domain.BackupFailed, failureOf(err)
		s.log.Warn("backup failed", "server_id", b.ServerID, "backup_id", b.ID, "failure", b.Failure, "err", err)
	}
	ferr := s.repo.Finish(ctx, b)
	if ferr != nil {
		if err == nil {
			_ = s.store.Remove(b.ServerID, b.ID) // the server or the backup was deleted meanwhile
		}
		if !errors.Is(ferr, domain.ErrNotFound) {
			s.log.Error("backup outcome not stored", "backup_id", b.ID, "err", ferr)
		}
	}
	ok := err == nil && ferr == nil
	s.record(ctx, actor, domain.AuditBackupCreate, serverName+": "+b.ID, ok)
	if ok && b.Kind == domain.BackupScheduled {
		s.prune(ctx, b.ServerID, serverName)
	}
}

// write waits for the archive slot, then archives the server: online (saving paused) if it
// runs, directly if it is stopped.
func (s *Service) write(ctx context.Context, b domain.Backup, actor servers.Actor) (int64, error) {
	select {
	case s.slot <- struct{}{}:
		defer func() { <-s.slot }()
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	// Status and free space may have changed while queued.
	view, err := s.servers.Get(ctx, b.ServerID)
	if err != nil {
		return 0, err
	}
	online, err := backupMode(view.Status)
	if err != nil {
		return 0, err
	}
	if err := s.checkSpace(b.ServerID); err != nil {
		return 0, err
	}
	if online {
		return s.writeOnline(ctx, b, actor)
	}
	size, err := s.store.Write(ctx, b.ServerID, b.ID)
	if err != nil {
		return 0, err
	}
	// A server started while its files were archived may have changed them halfway.
	if view, err := s.servers.Get(ctx, b.ServerID); err == nil {
		switch view.Status {
		case domain.StatusRunning, domain.StatusStarting, domain.StatusRestarting:
			_ = s.store.Remove(b.ServerID, b.ID)
			return 0, fmt.Errorf("%w: started during the backup", domain.ErrServerBusy)
		}
	}
	return size, nil
}

// writeOnline archives a running server with the game's saving turned off, after making it
// write everything to disk ("save-all flush" and its "Saved the game" confirmation). Saving
// is always turned back on. Players are told in the chat when it starts and how it ended.
func (s *Service) writeOnline(ctx context.Context, b domain.Backup, actor servers.Actor) (_ int64, err error) {
	saved := make(chan struct{})
	var once sync.Once
	var since atomic.Int64 // when "save-all flush" was sent (Unix ns); older lines are history
	unsubscribe, err := s.console.Subscribe(ctx, b.ServerID, func(ev console.Event) {
		at := since.Load()
		if ev.Kind != console.EventLine || at == 0 || ev.Line.Time.UnixNano() <= at ||
			!strings.Contains(ev.Line.Text, savedMarker) {
			return
		}
		once.Do(func() { close(saved) })
	})
	if err != nil {
		return 0, err
	}
	defer unsubscribe()

	// A chat message is a courtesy: failing to send one never fails the backup.
	chat := func(ctx context.Context, msg string) {
		if err := s.console.SendCommand(ctx, b.ServerID, tellraw(msg), actor); err != nil {
			s.log.Debug("backup: chat message not sent", "server_id", b.ServerID, "err", err)
		}
	}
	chat(ctx, chatStart)

	// Registered before "save-off" is sent: a failed send may still have reached the game.
	defer func() {
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), saveOnTimeout)
		defer cancel()
		if err := s.console.SendCommand(sctx, b.ServerID, "save-on", actor); err != nil &&
			!errors.Is(err, domain.ErrServerNotRunning) {
			s.log.Error("backup: saving could not be turned back on; run save-on in the console",
				"server_id", b.ServerID, "err", err)
		}
		if err == nil {
			chat(sctx, chatDone)
		} else {
			chat(sctx, chatFailed)
		}
	}()
	if err := s.console.SendCommand(ctx, b.ServerID, "save-off", actor); err != nil {
		return 0, err
	}
	since.Store(time.Now().UnixNano())
	if err := s.console.SendCommand(ctx, b.ServerID, "save-all flush", actor); err != nil {
		return 0, err
	}
	timer := time.NewTimer(s.saveTimeout)
	defer timer.Stop()
	select {
	case <-saved:
	case <-timer.C:
		return 0, errSaveTimeout
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	return s.store.Write(ctx, b.ServerID, b.ID)
}

// failureOf turns a backup error into a domain.BackupFailure* reason.
func failureOf(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return domain.BackupFailureInterrupted
	case errors.Is(err, errSaveTimeout):
		return domain.BackupFailureSaveTimeout
	case errors.Is(err, domain.ErrServerBusy), errors.Is(err, domain.ErrServerNotRunning):
		return domain.BackupFailureServerBusy
	case errors.Is(err, domain.ErrInsufficientStorage):
		return domain.BackupFailureStorage
	case errors.Is(err, domain.ErrDockerUnavailable):
		return domain.BackupFailureDocker
	}
	return domain.BackupFailureError
}

// prune applies retention after a successful scheduled backup.
func (s *Service) prune(ctx context.Context, serverID, serverName string) {
	sc, err := s.repo.Schedule(ctx, serverID)
	if err != nil {
		s.log.Error("backup retention skipped", "server_id", serverID, "err", err)
		return
	}
	old, err := s.repo.Prunable(ctx, serverID, sc.Retain)
	if err != nil {
		s.log.Error("backup retention skipped", "server_id", serverID, "err", err)
		return
	}
	for _, b := range old {
		err := s.store.Remove(serverID, b.ID)
		if err == nil {
			err = s.repo.Delete(ctx, b.ID)
		}
		if err != nil {
			s.log.Error("old backup not removed", "backup_id", b.ID, "err", err)
		}
		s.record(ctx, servers.Actor{}, domain.AuditBackupDelete, serverName+": "+b.ID, err == nil)
	}
}

// Restore replaces a stopped server's files with a backup. The backup is extracted next to
// the server first; only then is the current directory moved aside (servers/.deleted) and
// the restored one put in its place, so a failure leaves the server as it was.
func (s *Service) Restore(ctx context.Context, serverID, backupID string, actor servers.Actor) error {
	view, err := s.servers.Get(ctx, serverID)
	if err != nil {
		return err
	}
	if err := stoppedForRestore(view.Status); err != nil {
		return err
	}
	b, err := s.repo.ByID(ctx, serverID, backupID)
	if err != nil {
		return err
	}
	if b.Status != domain.BackupReady {
		return domain.ErrBackupNotReady
	}
	if err := s.acquire(serverID, opRestore); err != nil {
		return err
	}
	defer s.release(serverID)
	err = s.restore(ctx, b)
	s.record(ctx, actor, domain.AuditBackupRestore, view.Name+": "+b.ID, err == nil)
	return err
}

func (s *Service) restore(ctx context.Context, b domain.Backup) error {
	_, free, err := s.store.Usage(b.ServerID)
	if err != nil {
		return err
	}
	budget := free - s.reserve
	if budget <= 0 {
		return fmt.Errorf("%w: %d bytes free", domain.ErrInsufficientStorage, free)
	}
	staging, err := s.store.StageRestore(ctx, b.ServerID, b.ID, budget)
	if err != nil {
		return err
	}
	// The server may have been started while the backup was extracted.
	view, err := s.servers.Get(ctx, b.ServerID)
	if err == nil {
		err = stoppedForRestore(view.Status)
	}
	if err == nil {
		err = s.store.CommitRestore(b.ServerID, staging)
	}
	if err != nil {
		if derr := s.store.DiscardRestore(b.ServerID, staging); derr != nil {
			s.log.Error("restore staging directory not removed", "staging", staging, "err", derr)
		}
		return err
	}
	return nil
}

func stoppedForRestore(st domain.ServerStatus) error {
	switch st {
	case domain.StatusStopped, domain.StatusMissing:
		return nil
	case domain.StatusUnknown:
		return domain.ErrDockerUnavailable
	}
	return domain.ErrServerRunning
}

// Delete removes a backup and its archive. A backup being made or restored cannot be deleted.
func (s *Service) Delete(ctx context.Context, serverID, backupID string, actor servers.Actor) error {
	view, err := s.servers.Get(ctx, serverID)
	if err != nil {
		return err
	}
	b, err := s.repo.ByID(ctx, serverID, backupID)
	if err != nil {
		return err
	}
	if b.Status == domain.BackupCreating || s.busyWith(serverID) == opRestore {
		return domain.ErrBackupInProgress
	}
	err = s.store.Remove(serverID, backupID)
	if err == nil {
		err = s.repo.Delete(ctx, backupID)
	}
	s.record(ctx, actor, domain.AuditBackupDelete, view.Name+": "+b.ID, err == nil)
	return err
}

// Download is a backup archive opened for streaming. The caller closes File.
type Download struct {
	Name string // file name to offer the browser
	File *os.File
	Size int64
}

func (s *Service) Download(ctx context.Context, serverID, backupID string) (Download, error) {
	view, err := s.servers.Get(ctx, serverID)
	if err != nil {
		return Download{}, err
	}
	b, err := s.repo.ByID(ctx, serverID, backupID)
	if err != nil {
		return Download{}, err
	}
	if b.Status != domain.BackupReady {
		return Download{}, domain.ErrBackupNotReady
	}
	f, size, err := s.store.Open(serverID, backupID)
	if err != nil {
		return Download{}, err
	}
	return Download{Name: downloadName(view.Name, b.CreatedAt), File: f, Size: size}, nil
}

// downloadName is "<server name>-<UTC timestamp>.zip", the name reduced to letters, digits,
// '.', '_' and '-'.
func downloadName(server string, at time.Time) string {
	name := strings.Trim(strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '_' || r == '-' {
			return r
		}
		return '-'
	}, server), "-.")
	if name == "" {
		name = "server"
	}
	return name + "-" + at.UTC().Format("20060102-150405") + ".zip"
}

func (s *Service) Schedule(ctx context.Context, serverID string) (domain.BackupSchedule, error) {
	if _, err := s.servers.Get(ctx, serverID); err != nil {
		return domain.BackupSchedule{}, err
	}
	return s.repo.Schedule(ctx, serverID)
}

// UpdateSchedule sets a server's automatic backups. The first one runs one interval from
// now; changing only the retention keeps the planned run.
func (s *Service) UpdateSchedule(ctx context.Context, serverID string, intervalHours, retain int, actor servers.Actor) (domain.BackupSchedule, error) {
	if err := domain.ValidateBackupSchedule(intervalHours, retain); err != nil {
		return domain.BackupSchedule{}, err
	}
	view, err := s.servers.Get(ctx, serverID)
	if err != nil {
		return domain.BackupSchedule{}, err
	}
	old, err := s.repo.Schedule(ctx, serverID)
	if err != nil {
		return domain.BackupSchedule{}, err
	}
	sc := domain.BackupSchedule{ServerID: serverID, IntervalHours: intervalHours, Retain: retain}
	switch {
	case intervalHours == 0:
	case old.IntervalHours == intervalHours && !old.NextRunAt.IsZero():
		sc.NextRunAt = old.NextRunAt
	default:
		sc.NextRunAt = s.now().UTC().Truncate(time.Second).Add(time.Duration(intervalHours) * time.Hour)
	}
	err = s.repo.PutSchedule(ctx, sc)
	s.record(ctx, actor, domain.AuditBackupSchedule, view.Name, err == nil)
	if err != nil {
		return domain.BackupSchedule{}, err
	}
	return sc, nil
}

// Run starts due scheduled backups every tick until ctx is cancelled. A server that is busy
// (another backup, a restore, starting) is retried on the next tick; any other refusal is
// stored as a failed scheduled backup, so it shows in the list, and waits a full interval.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(s.tick)
	defer t.Stop()
	for {
		s.runDue(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Service) runDue(ctx context.Context) {
	now := s.now().UTC().Truncate(time.Second)
	due, err := s.repo.DueSchedules(ctx, now)
	if err != nil {
		if ctx.Err() == nil {
			s.log.Error("backup schedules not read", "err", err)
		}
		return
	}
	for _, sc := range due {
		_, err := s.create(ctx, sc.ServerID, domain.BackupScheduled, servers.Actor{})
		switch {
		case errors.Is(err, domain.ErrBackupInProgress), errors.Is(err, domain.ErrServerBusy):
			continue
		case err != nil:
			s.log.Warn("scheduled backup not started", "server_id", sc.ServerID, "err", err)
			s.storeRefusal(ctx, sc.ServerID, err)
		}
		// Keep the cadence; after a long downtime, run once and continue from now.
		interval := time.Duration(sc.IntervalHours) * time.Hour
		next := sc.NextRunAt.Add(interval)
		if !next.After(now) {
			next = now.Add(interval)
		}
		if err := s.repo.SetNextRun(ctx, sc.ServerID, next); err != nil {
			s.log.Error("next scheduled backup not stored", "server_id", sc.ServerID, "err", err)
		}
	}
}

// storeRefusal records a scheduled backup that could not even start as a failed one.
func (s *Service) storeRefusal(ctx context.Context, serverID string, cause error) {
	at := s.now().UTC().Truncate(time.Second)
	b := domain.Backup{ID: rand.Text(), ServerID: serverID, Kind: domain.BackupScheduled, Status: domain.BackupFailed,
		Failure: failureOf(cause), CreatedAt: at, CompletedAt: at}
	if err := s.repo.Create(ctx, b); err != nil && ctx.Err() == nil {
		s.log.Error("failed scheduled backup not stored", "server_id", serverID, "err", err)
	}
}

func (s *Service) record(ctx context.Context, actor servers.Actor, action, target string, success bool) {
	_ = s.audit.Record(context.WithoutCancel(ctx), domain.AuditEvent{
		CreatedAt: s.now().UTC(), UserID: actor.UserID, Action: action, Target: target, IP: actor.IP, Success: success,
	})
}
