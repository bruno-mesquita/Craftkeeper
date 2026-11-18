package backups

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bruno-mesquita/mesquita-panel/internal/application/console"
	"github.com/bruno-mesquita/mesquita-panel/internal/application/servers"
	"github.com/bruno-mesquita/mesquita-panel/internal/domain"
	"github.com/bruno-mesquita/mesquita-panel/internal/infrastructure/sqlite"
	"github.com/bruno-mesquita/mesquita-panel/migrations"
)

const serverID = "SERVERSERVERSERVERSERVER22"

// env is a backups service over a real SQLite store and fakes for everything else. Every
// fake appends to one ordered log, so tests can check the sequence of side effects.
type env struct {
	svc   *Service
	repo  *sqlite.Backups
	store *fakeStore
	srv   *fakeServers
	con   *fakeConsole
	audit *fakeAudit
	now   time.Time

	mu  sync.Mutex
	log []string
}

func (e *env) record(s string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.log = append(e.log, s)
}

func (e *env) events() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.log)
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	st, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "db.sqlite"), migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	now := time.Unix(1_800_000_000, 0).UTC()
	if err := sqlite.NewServers(st).Create(ctx, domain.Server{ID: serverID, Name: "Survival", Game: domain.GameMinecraft,
		Loader: domain.LoaderVanilla, Version: "LATEST", MemoryMB: 2048, CPUMillicores: 1000, Port: 25601,
		CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	e := &env{repo: sqlite.NewBackups(st), audit: &fakeAudit{}, now: now}
	e.store = &fakeStore{env: e, free: 1 << 30, dir: 1000}
	e.srv = &fakeServers{status: domain.StatusStopped}
	e.con = &fakeConsole{env: e, confirm: true}
	e.svc = NewService(e.repo, e.store, e.srv, e.con, e.audit, slog.New(slog.NewTextHandler(io.Discard, nil)))
	e.svc.reserve, e.svc.saveTimeout = 0, 300*time.Millisecond
	e.svc.now = func() time.Time {
		e.mu.Lock()
		defer e.mu.Unlock()
		return e.now
	}
	t.Cleanup(e.svc.Close)
	return e
}

func (e *env) advance(d time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.now = e.now.Add(d)
}

// finished waits for every backup job and returns backup id as stored.
func (e *env) finished(t *testing.T, id string) domain.Backup {
	t.Helper()
	e.svc.wg.Wait()
	b, err := e.repo.ByID(context.Background(), serverID, id)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

var actor = servers.Actor{UserID: "U", IP: "127.0.0.1"}

// chatCmd is the logged console command that shows msg to the players.
func chatCmd(msg string) string { return "console " + tellraw(msg) }

func TestTellraw(t *testing.T) {
	if got := tellraw(`say "hi"`); got != `tellraw @a {"color":"gray","text":"say \"hi\""}` {
		t.Fatalf("tellraw = %s", got)
	}
	if err := domain.ValidateConsoleCommand(tellraw(chatFailed)); err != nil {
		t.Fatalf("chat messages must be valid console commands: %v", err)
	}
}

type fakeServers struct {
	mu     sync.Mutex
	status domain.ServerStatus
}

func (f *fakeServers) set(st domain.ServerStatus) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status = st
}

func (f *fakeServers) Get(_ context.Context, id string) (servers.View, error) {
	if id != serverID {
		return servers.View{}, domain.ErrNotFound
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return servers.View{Server: domain.Server{ID: id, Name: "Survival"}, Status: f.status}, nil
}

type fakeStore struct {
	env      *env
	mu       sync.Mutex
	free     int64
	dir      int64
	writeErr error
	// onWrite runs inside Write (before it returns); it may block on ctx.
	onWrite func(ctx context.Context)
	onStage func()
}

func (f *fakeStore) Write(ctx context.Context, sid, bid string) (int64, error) {
	f.env.record("write " + bid)
	f.mu.Lock()
	hook, err := f.onWrite, f.writeErr
	f.mu.Unlock()
	if hook != nil {
		hook(ctx)
	}
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	return 4242, err
}

func (f *fakeStore) Open(sid, bid string) (*os.File, int64, error) {
	file, err := os.Open(os.DevNull)
	return file, 0, err
}

func (f *fakeStore) Remove(sid, bid string) error {
	f.env.record("remove " + bid)
	return nil
}

func (f *fakeStore) Usage(string) (int64, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.dir, f.free, nil
}

func (f *fakeStore) StageRestore(_ context.Context, sid, bid string, maxBytes int64) (string, error) {
	f.env.record("stage " + bid)
	if f.onStage != nil {
		f.onStage()
	}
	return ".restore-" + sid + "-X", nil
}

func (f *fakeStore) CommitRestore(sid, staging string) error {
	f.env.record("commit " + staging)
	return nil
}

func (f *fakeStore) DiscardRestore(sid, staging string) error {
	f.env.record("discard " + staging)
	return nil
}

func (f *fakeStore) CleanupStale() error {
	f.env.record("cleanup")
	return nil
}

// fakeConsole answers "save-all flush" with "Saved the game" when confirm is set. Old
// "Saved the game" lines (history, late tail) must never count as a confirmation.
type fakeConsole struct {
	env     *env
	mu      sync.Mutex
	sink    console.Sink
	confirm bool
}

func (f *fakeConsole) Subscribe(_ context.Context, sid string, sink console.Sink) (func(), error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sink = sink
	old := domain.ConsoleLine{Time: time.Now().Add(-time.Minute), Text: "[Server thread/INFO]: Saved the game"}
	sink(console.Event{Kind: console.EventHistory, Lines: []domain.ConsoleLine{old}})
	sink(console.Event{Kind: console.EventLine, Line: old})
	return func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.sink = nil
	}, nil
}

func (f *fakeConsole) Send(_ context.Context, sid, cmd string) error {
	f.env.record("console " + cmd)
	f.mu.Lock()
	defer f.mu.Unlock()
	if cmd == "save-all flush" && f.sink != nil {
		// Without confirm, a line from before the command arrives late (a new log stream
		// starts with the container's recent output): it must not count.
		at := time.Now().Add(-time.Minute)
		if f.confirm {
			at = time.Now().Add(time.Millisecond)
		}
		f.sink(console.Event{Kind: console.EventLine, Line: domain.ConsoleLine{Time: at, Text: "[Server thread/INFO]: Saved the game"}})
	}
	return nil
}

type fakeAudit struct {
	mu     sync.Mutex
	events []domain.AuditEvent
}

func (f *fakeAudit) Record(_ context.Context, e domain.AuditEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, e)
	return nil
}

func (f *fakeAudit) actions() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, e := range f.events {
		s := e.Action
		if !e.Success {
			s += "(failed)"
		}
		out = append(out, s)
	}
	return strings.Join(out, ",")
}

func TestOfflineBackup(t *testing.T) {
	e := newEnv(t)
	b, err := e.svc.Create(context.Background(), serverID, actor)
	if err != nil {
		t.Fatal(err)
	}
	if b.Status != domain.BackupCreating || b.Kind != domain.BackupManual || !domain.ValidBackupID(b.ID) {
		t.Fatalf("created = %+v", b)
	}
	got := e.finished(t, b.ID)
	if got.Status != domain.BackupReady || got.SizeBytes != 4242 || got.CompletedAt.IsZero() {
		t.Fatalf("finished = %+v", got)
	}
	if ev := e.events(); !slices.Equal(ev, []string{"write " + b.ID}) {
		t.Fatalf("a stopped server is archived directly: %v", ev)
	}
	if a := e.audit.actions(); a != "backup.create" {
		t.Fatalf("audit = %s", a)
	}
	if ev := e.audit.events[0]; ev.Target != "Survival: "+b.ID || ev.UserID != "U" || ev.ServerID != b.ServerID {
		t.Fatalf("audit event = %+v", e.audit.events[0])
	}
}

func TestOnlineBackupPausesSaving(t *testing.T) {
	e := newEnv(t)
	e.srv.set(domain.StatusRunning)
	b, err := e.svc.Create(context.Background(), serverID, actor)
	if err != nil {
		t.Fatal(err)
	}
	if got := e.finished(t, b.ID); got.Status != domain.BackupReady {
		t.Fatalf("finished = %+v", got)
	}
	want := []string{chatCmd(chatStart), "console save-off", "console save-all flush", "write " + b.ID, "console save-on", chatCmd(chatDone)}
	if ev := e.events(); !slices.Equal(ev, want) {
		t.Fatalf("events = %v, want %v", ev, want)
	}
}

func TestOnlineBackupWithoutSaveConfirmation(t *testing.T) {
	e := newEnv(t)
	e.srv.set(domain.StatusRunning)
	e.con.confirm = false // only old "Saved the game" lines arrive
	b, err := e.svc.Create(context.Background(), serverID, actor)
	if err != nil {
		t.Fatal(err)
	}
	got := e.finished(t, b.ID)
	if got.Status != domain.BackupFailed || got.Failure != domain.BackupFailureSaveTimeout {
		t.Fatalf("finished = %+v", got)
	}
	want := []string{chatCmd(chatStart), "console save-off", "console save-all flush", "console save-on", chatCmd(chatFailed)}
	if ev := e.events(); !slices.Equal(ev, want) {
		t.Fatalf("events = %v, want %v", ev, want)
	}
	if a := e.audit.actions(); a != "backup.create(failed)" {
		t.Fatalf("audit = %s", a)
	}
}

func TestOnlineBackupTurnsSavingBackOnAfterAFailure(t *testing.T) {
	e := newEnv(t)
	e.srv.set(domain.StatusRunning)
	e.store.writeErr = domain.ErrInsufficientStorage
	b, err := e.svc.Create(context.Background(), serverID, actor)
	if err != nil {
		t.Fatal(err)
	}
	if got := e.finished(t, b.ID); got.Status != domain.BackupFailed || got.Failure != domain.BackupFailureStorage {
		t.Fatalf("finished = %+v", got)
	}
	if ev := e.events(); ev[len(ev)-2] != "console save-on" || ev[len(ev)-1] != chatCmd(chatFailed) {
		t.Fatalf("events = %v", ev)
	}
}

func TestCreateRefusals(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		status domain.ServerStatus
		want   error
	}{
		{domain.StatusStarting, domain.ErrServerBusy},
		{domain.StatusRestarting, domain.ErrServerBusy},
		{domain.StatusUnknown, domain.ErrDockerUnavailable},
	} {
		e := newEnv(t)
		e.srv.set(tc.status)
		if _, err := e.svc.Create(ctx, serverID, actor); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.status, err, tc.want)
		}
	}

	e := newEnv(t)
	if _, err := e.svc.Create(ctx, "UNKNOWNUNKNOWNUNKNOWNUNKN2", actor); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown server: %v", err)
	}
	e.svc.reserve = 100
	e.store.free = 1050 // 1000 bytes of files need 1100 free with the reserve
	if _, err := e.svc.Create(ctx, serverID, actor); !errors.Is(err, domain.ErrInsufficientStorage) {
		t.Fatalf("no room: %v", err)
	}
	if list, _ := e.repo.List(ctx, serverID); len(list) != 0 {
		t.Fatalf("refused backups must not be stored: %+v", list)
	}
}

func TestOneOperationPerServer(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	release := make(chan struct{})
	e.store.onWrite = func(context.Context) { <-release }
	first, err := e.svc.Create(ctx, serverID, actor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Create(ctx, serverID, actor); !errors.Is(err, domain.ErrBackupInProgress) {
		t.Fatalf("second backup: %v", err)
	}
	if err := e.svc.Delete(ctx, serverID, first.ID, actor); !errors.Is(err, domain.ErrBackupInProgress) {
		t.Fatalf("deleting a backup being made: %v", err)
	}
	if err := e.svc.Restore(ctx, serverID, first.ID, actor); !errors.Is(err, domain.ErrBackupNotReady) {
		t.Fatalf("restoring a backup being made: %v", err)
	}
	if _, err := e.svc.Download(ctx, serverID, first.ID); !errors.Is(err, domain.ErrBackupNotReady) {
		t.Fatalf("downloading a backup being made: %v", err)
	}
	close(release)
	e.finished(t, first.ID)

	// A restore holds the server too.
	stageRelease := make(chan struct{})
	staged := make(chan struct{})
	e.store.onStage = func() { close(staged); <-stageRelease }
	done := make(chan error)
	go func() { done <- e.svc.Restore(ctx, serverID, first.ID, actor) }()
	<-staged
	if _, err := e.svc.Create(ctx, serverID, actor); !errors.Is(err, domain.ErrBackupInProgress) {
		t.Fatalf("backup during a restore: %v", err)
	}
	if err := e.svc.Delete(ctx, serverID, first.ID, actor); !errors.Is(err, domain.ErrBackupInProgress) {
		t.Fatalf("delete during a restore: %v", err)
	}
	close(stageRelease)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Create(ctx, serverID, actor); err != nil {
		t.Fatalf("backup after the restore: %v", err)
	}
}

func TestServerStartedDuringOfflineBackup(t *testing.T) {
	e := newEnv(t)
	e.store.onWrite = func(context.Context) { e.srv.set(domain.StatusRunning) }
	b, err := e.svc.Create(context.Background(), serverID, actor)
	if err != nil {
		t.Fatal(err)
	}
	if got := e.finished(t, b.ID); got.Status != domain.BackupFailed || got.Failure != domain.BackupFailureServerBusy {
		t.Fatalf("finished = %+v", got)
	}
	if ev := e.events(); !slices.Equal(ev, []string{"write " + b.ID, "remove " + b.ID}) {
		t.Fatalf("events = %v", ev)
	}
}

func TestCloseInterruptsBackups(t *testing.T) {
	e := newEnv(t)
	started := make(chan struct{})
	e.store.onWrite = func(ctx context.Context) { close(started); <-ctx.Done() }
	b, err := e.svc.Create(context.Background(), serverID, actor)
	if err != nil {
		t.Fatal(err)
	}
	<-started
	e.svc.Close()
	if got := e.finished(t, b.ID); got.Status != domain.BackupFailed || got.Failure != domain.BackupFailureInterrupted {
		t.Fatalf("finished = %+v", got)
	}
	if _, err := e.svc.Create(context.Background(), serverID, actor); err == nil {
		t.Fatal("a closed service must refuse backups")
	}
}

func readyBackup(t *testing.T, e *env) domain.Backup {
	t.Helper()
	b, err := e.svc.Create(context.Background(), serverID, actor)
	if err != nil {
		t.Fatal(err)
	}
	e.finished(t, b.ID)
	e.mu.Lock()
	e.log = nil
	e.mu.Unlock()
	return b
}

func TestRestore(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	b := readyBackup(t, e)

	if err := e.svc.Restore(ctx, serverID, b.ID, actor); err != nil {
		t.Fatal(err)
	}
	staging := ".restore-" + serverID + "-X"
	if ev := e.events(); !slices.Equal(ev, []string{"stage " + b.ID, "commit " + staging}) {
		t.Fatalf("events = %v", ev)
	}

	e.srv.set(domain.StatusRunning)
	if err := e.svc.Restore(ctx, serverID, b.ID, actor); !errors.Is(err, domain.ErrServerRunning) {
		t.Fatalf("restore of a running server: %v", err)
	}
	e.srv.set(domain.StatusUnknown)
	if err := e.svc.Restore(ctx, serverID, b.ID, actor); !errors.Is(err, domain.ErrDockerUnavailable) {
		t.Fatalf("restore without Docker: %v", err)
	}
	e.srv.set(domain.StatusStopped)
	if err := e.svc.Restore(ctx, serverID, "MISSINGMISSINGMISSINGMISS2", actor); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("restore of a missing backup: %v", err)
	}

	// Started while the backup was being extracted: nothing is swapped.
	e.log = nil
	e.store.onStage = func() { e.srv.set(domain.StatusRunning) }
	if err := e.svc.Restore(ctx, serverID, b.ID, actor); !errors.Is(err, domain.ErrServerRunning) {
		t.Fatalf("restore raced by a start: %v", err)
	}
	if ev := e.events(); !slices.Equal(ev, []string{"stage " + b.ID, "discard " + staging}) {
		t.Fatalf("events = %v", ev)
	}
	e.store.onStage = nil
	e.srv.set(domain.StatusStopped)

	e.svc.reserve = e.store.free
	if err := e.svc.Restore(ctx, serverID, b.ID, actor); !errors.Is(err, domain.ErrInsufficientStorage) {
		t.Fatalf("restore without room: %v", err)
	}
	if a := e.audit.actions(); !strings.HasSuffix(a, "backup.restore,backup.restore(failed),backup.restore(failed)") {
		t.Fatalf("audit = %s", a)
	}
}

func TestDeleteAndDownload(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	b := readyBackup(t, e)
	d, err := e.svc.Download(ctx, serverID, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	d.File.Close()
	if want := "Survival-" + b.CreatedAt.Format("20060102-150405") + ".zip"; d.Name != want {
		t.Fatalf("download name = %q, want %q", d.Name, want)
	}
	if err := e.svc.Delete(ctx, serverID, b.ID, actor); err != nil {
		t.Fatal(err)
	}
	if ev := e.events(); !slices.Equal(ev, []string{"remove " + b.ID}) {
		t.Fatalf("events = %v", ev)
	}
	if err := e.svc.Delete(ctx, serverID, b.ID, actor); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
}

func TestDownloadName(t *testing.T) {
	at := time.Date(2026, 11, 17, 8, 9, 10, 0, time.UTC)
	for in, want := range map[string]string{
		"Survival":      "Survival-20261117-080910.zip",
		"My World 2":    "My-World-2-20261117-080910.zip",
		"Ação/../é":     "Ação-..-é-20261117-080910.zip",
		"   ":           "server-20261117-080910.zip",
		"\"quoted\";x=": "quoted--x-20261117-080910.zip",
	} {
		if got := downloadName(in, at); got != want {
			t.Errorf("downloadName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestScheduleAndRetention(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	manual := readyBackup(t, e)

	if _, err := e.svc.UpdateSchedule(ctx, serverID, 721, 1, actor); err == nil {
		t.Fatal("invalid interval accepted")
	}
	sc, err := e.svc.UpdateSchedule(ctx, serverID, 6, 2, actor)
	if err != nil || !sc.NextRunAt.Equal(e.now.Add(6*time.Hour)) {
		t.Fatalf("UpdateSchedule = %+v, %v", sc, err)
	}
	// Changing only the retention keeps the planned run.
	e.advance(time.Hour)
	if sc2, _ := e.svc.UpdateSchedule(ctx, serverID, 6, 2, actor); !sc2.NextRunAt.Equal(sc.NextRunAt) {
		t.Fatalf("next run moved: %v → %v", sc.NextRunAt, sc2.NextRunAt)
	}

	scheduled := func() (ids []string) {
		list, _ := e.repo.List(ctx, serverID)
		for _, b := range list {
			if b.Kind == domain.BackupScheduled {
				ids = append(ids, string(b.Status)+":"+b.ID)
			}
		}
		return ids
	}
	e.svc.runDue(ctx)
	if got := scheduled(); len(got) != 0 {
		t.Fatalf("ran before it was due: %v", got)
	}

	var made []string
	for range 3 {
		e.advance(6 * time.Hour)
		e.svc.runDue(ctx)
		e.svc.wg.Wait()
		list, _ := e.repo.List(ctx, serverID)
		made = append(made, list[0].ID)
	}
	got := scheduled()
	if len(got) != 2 || got[0] != "ready:"+made[2] || got[1] != "ready:"+made[1] {
		t.Fatalf("after retention: %v (made %v)", got, made)
	}
	if _, err := e.repo.ByID(ctx, serverID, manual.ID); err != nil {
		t.Fatalf("retention must keep manual backups: %v", err)
	}
	if ev := e.events(); !slices.Contains(ev, "remove "+made[0]) {
		t.Fatalf("oldest archive not removed: %v", ev)
	}
	if a := e.audit.actions(); !strings.Contains(a, "backup.delete") {
		t.Fatalf("retention not audited: %s", a)
	}
	// The cadence is kept: one interval after the previous planned run.
	cur, _ := e.repo.Schedule(ctx, serverID)
	if !cur.NextRunAt.Equal(sc.NextRunAt.Add(3 * 6 * time.Hour)) {
		t.Fatalf("next run = %v, want %v", cur.NextRunAt, sc.NextRunAt.Add(18*time.Hour))
	}

	// A busy server is retried on the next tick; the next run does not move.
	e.advance(6 * time.Hour)
	e.srv.set(domain.StatusStarting)
	e.svc.runDue(ctx)
	if again, _ := e.repo.Schedule(ctx, serverID); !again.NextRunAt.Equal(cur.NextRunAt) {
		t.Fatalf("busy server moved the next run to %v", again.NextRunAt)
	}

	// Any other refusal is shown as a failed scheduled backup and waits an interval.
	e.srv.set(domain.StatusStopped)
	e.svc.reserve = e.store.free
	e.svc.runDue(ctx)
	if got := scheduled(); !strings.HasPrefix(got[0], "failed:") {
		t.Fatalf("refusal not stored: %v", got)
	}
	list, _ := e.repo.List(ctx, serverID)
	if list[0].Failure != domain.BackupFailureStorage {
		t.Fatalf("refusal = %+v", list[0])
	}
	if again, _ := e.repo.Schedule(ctx, serverID); !again.NextRunAt.After(e.now) {
		t.Fatalf("next run after a refusal = %v", again.NextRunAt)
	}

	// After a long downtime it runs once and continues from now.
	e.svc.reserve = 0
	e.advance(100 * time.Hour)
	e.svc.runDue(ctx)
	e.svc.wg.Wait()
	if again, _ := e.repo.Schedule(ctx, serverID); !again.NextRunAt.Equal(e.now.Add(6 * time.Hour)) {
		t.Fatalf("next run after downtime = %v, want %v", again.NextRunAt, e.now.Add(6*time.Hour))
	}
	// The successful run pruned the failed one.
	for _, s := range scheduled() {
		if strings.HasPrefix(s, "failed:") {
			t.Fatalf("failed scheduled backup kept after a success: %v", scheduled())
		}
	}

	// Off.
	if sc, err := e.svc.UpdateSchedule(ctx, serverID, 0, 2, actor); err != nil || !sc.NextRunAt.IsZero() {
		t.Fatalf("schedule off = %+v, %v", sc, err)
	}
}

func TestRecover(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	b := domain.Backup{ID: "STALESTALESTALESTALESTALE2", ServerID: serverID, Kind: domain.BackupManual,
		Status: domain.BackupCreating, CreatedAt: e.now}
	if err := e.repo.Create(ctx, b); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := e.repo.ByID(ctx, serverID, b.ID)
	if got.Status != domain.BackupFailed || got.Failure != domain.BackupFailureInterrupted {
		t.Fatalf("after Recover = %+v", got)
	}
	if ev := e.events(); !slices.Equal(ev, []string{"cleanup"}) {
		t.Fatalf("events = %v", ev)
	}
}
