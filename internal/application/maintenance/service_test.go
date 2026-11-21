package maintenance

import (
	"archive/zip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/bruno-mesquita/Craftkeeper/internal/application/servers"
	"github.com/bruno-mesquita/Craftkeeper/internal/domain"
	"github.com/bruno-mesquita/Craftkeeper/internal/infrastructure/filesystem"
)

const serverID = "SERVERSERVERSERVERSERVER22"

var actor = servers.Actor{UserID: "U", IP: "127.0.0.1"}

// env is the service over a real server directory and fakes that log their side effects.
type env struct {
	svc   *Service
	dir   string // the server's directory
	srv   *fakeServers
	audit *fakeAudit

	mu  sync.Mutex
	log []string
}

func (e *env) record(s string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.log = append(e.log, s)
}

func newEnv(t *testing.T) *env {
	t.Helper()
	root := t.TempDir()
	e := &env{dir: filepath.Join(root, serverID), audit: &fakeAudit{}}
	if err := os.Mkdir(e.dir, 0o750); err != nil {
		t.Fatal(err)
	}
	e.srv = &fakeServers{env: e, status: domain.StatusStopped}
	e.svc = NewService(e.srv, &fakeBackups{env: e}, filesystem.NewServerFiles(root, os.Getuid(), os.Getgid()), e.audit)
	return e
}

func (e *env) write(t *testing.T, p, content string) {
	t.Helper()
	full := filepath.Join(e.dir, p)
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o640); err != nil {
		t.Fatal(err)
	}
}

func (e *env) read(p string) string {
	b, err := os.ReadFile(filepath.Join(e.dir, p))
	if err != nil {
		return "<missing>"
	}
	return string(b)
}

// zipFile writes a zip archive with the given files into the server directory.
func (e *env) zipFile(t *testing.T, name string, files map[string]string) {
	t.Helper()
	f, err := os.Create(filepath.Join(e.dir, name))
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for n, c := range files {
		w, err := zw.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(c))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

func (e *env) leftovers(t *testing.T) []string {
	t.Helper()
	entries, _ := os.ReadDir(e.dir)
	var out []string
	for _, en := range entries {
		if strings.HasPrefix(en.Name(), ".craftkeeper-tmp-") {
			out = append(out, en.Name())
		}
	}
	return out
}

type fakeServers struct {
	env    *env
	mu     sync.Mutex
	status domain.ServerStatus
	// afterBackup, when set, becomes the status once the backup is made.
	afterBackup domain.ServerStatus
}

func (f *fakeServers) Get(_ context.Context, id string) (servers.View, error) {
	if id != serverID {
		return servers.View{}, domain.ErrNotFound
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return servers.View{Server: domain.Server{ID: id, Name: "Survival"}, Status: f.status}, nil
}

func (f *fakeServers) ResolveVersion(_ context.Context, in servers.VersionInput) (string, error) {
	if in.Version == "bad" {
		return "", &domain.ValidationError{Field: "version", Message: "unknown"}
	}
	return "", nil
}

func (f *fakeServers) ChangeVersion(_ context.Context, id string, in servers.VersionInput, _ servers.Actor) (servers.View, error) {
	f.env.record("version " + in.Loader + " " + in.Version)
	return servers.View{Server: domain.Server{ID: id, Loader: in.Loader, Version: in.Version}, Status: domain.StatusStopped}, nil
}

type fakeBackups struct{ env *env }

func (f *fakeBackups) Protect(ctx context.Context, _ string, _ servers.Actor, change func(context.Context) error) error {
	f.env.record("backup")
	s := f.env.srv
	s.mu.Lock()
	if s.afterBackup != "" {
		s.status = s.afterBackup
	}
	s.mu.Unlock()
	return change(ctx)
}

type fakeAudit struct {
	mu     sync.Mutex
	events []domain.AuditEvent
}

func (f *fakeAudit) Record(_ context.Context, ev domain.AuditEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, ev)
	return nil
}

func (e *env) events() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.log)
}

func TestChangeVersion(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	v, err := e.svc.ChangeVersion(ctx, serverID, servers.VersionInput{Loader: "fabric", Version: "1.21.1"}, actor)
	if err != nil || v.Loader != "fabric" {
		t.Fatalf("view %+v, err %v", v, err)
	}
	if ev := e.events(); !slices.Equal(ev, []string{"backup", "version fabric 1.21.1"}) {
		t.Fatalf("events = %v", ev)
	}

	// Refused before any backup.
	for name, tc := range map[string]struct {
		prepare func(e *env)
		in      servers.VersionInput
		want    error
	}{
		"running": {func(e *env) { e.srv.status = domain.StatusRunning }, servers.VersionInput{Loader: "vanilla", Version: "1.21.1"}, domain.ErrServerRunning},
		"busy":    {func(e *env) { e.srv.status = domain.StatusStarting }, servers.VersionInput{Loader: "vanilla", Version: "1.21.1"}, domain.ErrServerBusy},
		"modpack": {func(e *env) { e.write(t, domain.ModpackConfigFile, "x") }, servers.VersionInput{Loader: "vanilla", Version: "1.21.1"}, domain.ErrModpackServer},
		"invalid": {func(*env) {}, servers.VersionInput{Loader: "vanilla", Version: "bad"}, domain.ErrInvalidInput},
	} {
		e := newEnv(t)
		tc.prepare(e)
		if _, err := e.svc.ChangeVersion(ctx, serverID, tc.in, actor); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v", name, err)
		}
		if ev := e.events(); len(ev) != 0 {
			t.Errorf("%s: events = %v", name, ev)
		}
	}
}

func TestResetWorld(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	e.write(t, "server.properties", "#c\r\nlevel-name=maps/survival\r\nlevel-seed=1\r\n")
	e.write(t, "maps/survival/level.dat", "old")
	e.write(t, "world/level.dat", "not the configured world")
	seed := "  -42 "
	if err := e.svc.ResetWorld(ctx, serverID, &seed, actor); err != nil {
		t.Fatal(err)
	}
	if got := e.read("maps/survival/level.dat"); got != "<missing>" {
		t.Fatalf("world kept: %q", got)
	}
	if got := e.read("world/level.dat"); got == "<missing>" {
		t.Fatal("another directory was deleted")
	}
	if got := e.read("server.properties"); got != "#c\r\nlevel-name=maps/survival\r\nlevel-seed=-42\r\n" {
		t.Fatalf("properties = %q", got)
	}
	if ev := e.events(); !slices.Equal(ev, []string{"backup"}) {
		t.Fatalf("events = %v", ev)
	}
	if a := e.audit.events; len(a) != 1 || a[0].Action != domain.AuditWorldReset || !a[0].Success ||
		a[0].Target != "Survival: maps/survival" || a[0].ServerID != serverID {
		t.Fatalf("audit = %+v", a)
	}

	// Without a seed the properties are left alone; a missing world is fine.
	e = newEnv(t)
	e.write(t, "server.properties", "level-seed=7\n")
	if err := e.svc.ResetWorld(ctx, serverID, nil, actor); err != nil {
		t.Fatal(err)
	}
	if got := e.read("server.properties"); got != "level-seed=7\n" {
		t.Fatalf("properties = %q", got)
	}

	// Started while the backup was made: nothing is deleted.
	e = newEnv(t)
	e.write(t, "world/level.dat", "old")
	e.srv.afterBackup = domain.StatusRunning
	if err := e.svc.ResetWorld(ctx, serverID, nil, actor); !errors.Is(err, domain.ErrServerRunning) {
		t.Fatalf("started meanwhile: %v", err)
	}
	if e.read("world/level.dat") != "old" {
		t.Fatal("world deleted although the server started")
	}

	// Bad input never reaches the backup.
	e = newEnv(t)
	bad := "a\nb"
	if err := e.svc.ResetWorld(ctx, serverID, &bad, actor); fieldOf(err) != "seed" {
		t.Fatalf("bad seed: %v", err)
	}
	e.write(t, "server.properties", "level-name=../escape\n")
	if err := e.svc.ResetWorld(ctx, serverID, nil, actor); fieldOf(err) != "level-name" {
		t.Fatalf("bad level-name: %v", err)
	}
	if ev := e.events(); len(ev) != 0 {
		t.Fatalf("events = %v", ev)
	}
}

func TestImportWorld(t *testing.T) {
	ctx := context.Background()
	for name, files := range map[string]map[string]string{
		"top-level folder": {"MyWorld/level.dat": "new", "MyWorld/region/r.0.0.mca": "r", "__MACOSX/._x": "junk"},
		"at the root":      {"level.dat": "new", "region/r.0.0.mca": "r"},
	} {
		e := newEnv(t)
		e.write(t, "world/level.dat", "old")
		e.write(t, "world/stale.dat", "stale")
		e.zipFile(t, "upload.zip", files)
		if err := e.svc.ImportWorld(ctx, serverID, "upload.zip", actor); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if e.read("world/level.dat") != "new" || e.read("world/region/r.0.0.mca") != "r" || e.read("world/stale.dat") != "<missing>" {
			t.Fatalf("%s: world not replaced", name)
		}
		if e.read("upload.zip") != "<missing>" || len(e.leftovers(t)) != 0 {
			t.Fatalf("%s: archive or staging left: %v", name, e.leftovers(t))
		}
		if ev := e.events(); !slices.Equal(ev, []string{"backup"}) {
			t.Fatalf("%s: events = %v", name, ev)
		}
		if a := e.audit.events; len(a) != 1 || a[0].Action != domain.AuditWorldImport || !a[0].Success {
			t.Fatalf("%s: audit = %+v", name, a)
		}
	}

	// Not a world: refused before the backup, the current world and the archive stay.
	for name, files := range map[string]map[string]string{
		"no level.dat":    {"MyWorld/region/r.0.0.mca": "r"},
		"two folders":     {"A/level.dat": "a", "B/level.dat": "b"},
		"file at the top": {"readme.txt": "x", "W/level.dat": "w"},
	} {
		e := newEnv(t)
		e.write(t, "world/level.dat", "old")
		e.zipFile(t, "upload.zip", files)
		if err := e.svc.ImportWorld(ctx, serverID, "upload.zip", actor); !errors.Is(err, domain.ErrInvalidWorld) {
			t.Fatalf("%s: err = %v", name, err)
		}
		if e.read("world/level.dat") != "old" || e.read("upload.zip") == "<missing>" || len(e.leftovers(t)) != 0 {
			t.Fatalf("%s: state changed", name)
		}
		if ev := e.events(); len(ev) != 0 {
			t.Fatalf("%s: events = %v", name, ev)
		}
	}

	e := newEnv(t)
	for _, p := range []string{"", "world.tar", "../x.zip"} {
		if err := e.svc.ImportWorld(ctx, serverID, p, actor); fieldOf(err) != "path" {
			t.Errorf("path %q: %v", p, err)
		}
	}
	e.srv.status = domain.StatusRunning
	e.zipFile(t, "upload.zip", map[string]string{"level.dat": "new"})
	if err := e.svc.ImportWorld(ctx, serverID, "upload.zip", actor); !errors.Is(err, domain.ErrServerRunning) {
		t.Fatalf("running: %v", err)
	}
}

func fieldOf(err error) string {
	var ve *domain.ValidationError
	if errors.As(err, &ve) {
		return ve.Field
	}
	return ""
}
