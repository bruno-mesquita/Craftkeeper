package servers

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bruno-mesquita/mesquita-panel/internal/application/installers"
	"github.com/bruno-mesquita/mesquita-panel/internal/domain"
	"github.com/bruno-mesquita/mesquita-panel/internal/infrastructure/filesystem"
	"github.com/bruno-mesquita/mesquita-panel/internal/infrastructure/sqlite"
	"github.com/bruno-mesquita/mesquita-panel/migrations"
)

// fakeRuntime keeps containers in memory and can fail chosen operations.
type fakeRuntime struct {
	mu         sync.Mutex
	containers map[string]*domain.ContainerState // by container ID
	specs      map[string]domain.ContainerSpec
	limits     map[string][2]int
	fail       map[string]error // operation name -> error
	calls      []string
	pulled     []string // Java runtimes passed to EnsureImage
	n          int
}

func newFakeRuntime() *fakeRuntime {
	return &fakeRuntime{containers: map[string]*domain.ContainerState{}, specs: map[string]domain.ContainerSpec{},
		limits: map[string][2]int{}, fail: map[string]error{}}
}

func (f *fakeRuntime) begin(op string) error {
	f.calls = append(f.calls, op)
	return f.fail[op]
}

// owned mirrors the real runtime's ownership check.
func (f *fakeRuntime) owned(id, serverID string) (*domain.ContainerState, error) {
	c, ok := f.containers[id]
	if !ok {
		return nil, domain.ErrContainerMissing
	}
	if c.ServerID != serverID {
		return nil, domain.ErrNotManaged
	}
	return c, nil
}

func (f *fakeRuntime) EnsureImage(_ context.Context, java string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pulled = append(f.pulled, java)
	return f.begin("pull")
}

func (f *fakeRuntime) Create(_ context.Context, spec domain.ContainerSpec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.begin("create"); err != nil {
		return "", err
	}
	f.n++
	id := strings.Repeat(string(rune('a'+f.n)), 64)
	f.containers[id] = &domain.ContainerState{ID: id, ServerID: spec.ServerID, State: "created"}
	f.specs[id] = spec
	return id, nil
}

func (f *fakeRuntime) Inspect(_ context.Context, id, serverID string) (domain.ContainerState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.begin("inspect"); err != nil {
		return domain.ContainerState{}, err
	}
	c, err := f.owned(id, serverID)
	if err != nil {
		return domain.ContainerState{}, err
	}
	return *c, nil
}

func (f *fakeRuntime) States(context.Context) ([]domain.ContainerState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.begin("states"); err != nil {
		return nil, err
	}
	var out []domain.ContainerState
	for _, c := range f.containers {
		out = append(out, *c)
	}
	return out, nil
}

func (f *fakeRuntime) setState(op, state string) func(context.Context, string, string) error {
	return func(_ context.Context, id, serverID string) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		if err := f.begin(op); err != nil {
			return err
		}
		c, err := f.owned(id, serverID)
		if err != nil {
			return err
		}
		c.State = state
		return nil
	}
}

func (f *fakeRuntime) Start(ctx context.Context, id, sid string) error {
	return f.setState("start", "running")(ctx, id, sid)
}
func (f *fakeRuntime) Stop(ctx context.Context, id, sid string) error {
	return f.setState("stop", "exited")(ctx, id, sid)
}
func (f *fakeRuntime) Restart(ctx context.Context, id, sid string) error {
	return f.setState("restart", "running")(ctx, id, sid)
}
func (f *fakeRuntime) Kill(ctx context.Context, id, sid string) error {
	return f.setState("kill", "exited")(ctx, id, sid)
}

func (f *fakeRuntime) Remove(_ context.Context, id, serverID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.begin("remove"); err != nil {
		return err
	}
	if _, err := f.owned(id, serverID); err != nil {
		return err
	}
	delete(f.containers, id)
	return nil
}

func (f *fakeRuntime) UpdateLimits(_ context.Context, id, serverID string, mem, cpu int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.begin("update"); err != nil {
		return err
	}
	if _, err := f.owned(id, serverID); err != nil {
		return err
	}
	f.limits[id] = [2]int{mem, cpu}
	return nil
}

// fakeSource is the version catalog for tests: one Forge, Fabric and NeoForge release.
type fakeSource struct{ down bool }

func (f *fakeSource) catalog(mc string, builds ...string) (map[string]domain.LoaderVersions, error) {
	if f.down {
		return nil, domain.ErrInstallerUnavailable
	}
	return map[string]domain.LoaderVersions{mc: {Versions: builds, Recommended: builds[len(builds)-1]}}, nil
}

func (f *fakeSource) Minecraft(context.Context) ([]string, error) {
	return nil, errors.New("unused")
}

func (f *fakeSource) Fabric(context.Context) (map[string]domain.LoaderVersions, error) {
	return f.catalog("1.21.1", "0.20.1")
}

func (f *fakeSource) Forge(context.Context) (map[string]domain.LoaderVersions, error) {
	return f.catalog("1.20.1", "47.4.5", "47.3.0")
}

func (f *fakeSource) NeoForge(context.Context) (map[string]domain.LoaderVersions, error) {
	return f.catalog("1.21.1", "21.1.267")
}

type env struct {
	src     *fakeSource
	svc     *Service
	rt      *fakeRuntime
	repo    *sqlite.Servers
	dir     string
	busy    map[int]bool // host ports reported as taken
	actor   Actor
	auditDB *sqlite.AuditLog
}

func newEnv(t *testing.T) *env {
	t.Helper()
	tmp := t.TempDir()
	store, err := sqlite.Open(context.Background(), filepath.Join(tmp, "db.sqlite"), migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	e := &env{src: &fakeSource{}, rt: newFakeRuntime(), repo: sqlite.NewServers(store), dir: filepath.Join(tmp, "servers"),
		busy: map[int]bool{}, actor: Actor{UserID: "U", IP: "127.0.0.1"},
		auditDB: sqlite.NewAuditLog(store, slog.New(slog.NewTextHandler(io.Discard, nil)))}
	e.svc = NewService(e.repo, e.rt, filesystem.NewServerDirs(e.dir, filepath.Join(tmp, "backups"), os.Getuid(), os.Getgid()),
		installers.NewService(e.src, time.Hour), e.auditDB,
		Config{FirstPort: 25601, LastPort: 25603, HostCPUs: 4, PortFree: func(p int) bool { return !e.busy[p] }})
	return e
}

func validInput(name string) CreateInput {
	return CreateInput{Name: name, Loader: domain.LoaderVanilla, Version: "LATEST", MemoryMB: 2048, CPUMillicores: 2000, AcceptEULA: true}
}

func fieldOf(err error) string {
	var ve *domain.ValidationError
	if errors.As(err, &ve) {
		return ve.Field
	}
	return ""
}

func TestCreateAssignsPortsAndBuildsContainer(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.busy[25601] = true // taken by another program

	v, err := e.svc.Create(ctx, validInput("Survival"), e.actor)
	if err != nil {
		t.Fatal(err)
	}
	if v.Port != 25602 || v.Status != domain.StatusStopped || !domain.ValidServerID(v.ID) || v.ContainerID == "" {
		t.Fatalf("created = %+v", v)
	}
	spec := e.rt.specs[v.ContainerID]
	if spec.ServerID != v.ID || spec.DataDir != filepath.Join(e.dir, v.ID) || spec.Port != 25602 ||
		spec.MemoryMB != 2048 || spec.CPUMillicores != 2000 {
		t.Fatalf("spec = %+v", spec)
	}
	if !strings.Contains(strings.Join(spec.Env, " "), "EULA=TRUE") {
		t.Fatalf("env = %v", spec.Env)
	}
	if _, err := os.Stat(spec.DataDir); err != nil {
		t.Fatalf("data dir: %v", err)
	}

	v2, err := e.svc.Create(ctx, validInput("Creative"), e.actor)
	if err != nil || v2.Port != 25603 {
		t.Fatalf("second = %+v, %v", v2, err)
	}
	if _, err := e.svc.Create(ctx, validInput("Third"), e.actor); fieldOf(err) != "port" {
		t.Fatalf("range exhausted: err = %v", err)
	}
	e.busy[25601] = false // a port is free again, so the duplicate name is what fails
	if _, err := e.svc.Create(ctx, validInput("survival"), e.actor); fieldOf(err) != "name" {
		t.Fatalf("duplicate name (case-insensitive): err = %v", err)
	}
	if entries, _ := os.ReadDir(e.dir); len(entries) != 2 {
		t.Fatalf("data dirs = %d, want 2", len(entries))
	}
}

func TestCreateValidation(t *testing.T) {
	e := newEnv(t)
	cases := map[string]func(*CreateInput){
		"name":           func(in *CreateInput) { in.Name = "" },
		"loader":         func(in *CreateInput) { in.Loader = "paper" },
		"version":        func(in *CreateInput) { in.Version = "1.21; rm -rf /" },
		"loader_version": func(in *CreateInput) { in.LoaderVersion = "47.3.0" }, // vanilla has none
		"memory_mb":      func(in *CreateInput) { in.MemoryMB = 100 },
		"cpu_cores":      func(in *CreateInput) { in.CPUMillicores = 8000 },
		"accept_eula":    func(in *CreateInput) { in.AcceptEULA = false },
		"port":           func(in *CreateInput) { in.Port = 25565 },
	}
	for field, mutate := range cases {
		in := validInput("X")
		mutate(&in)
		if _, err := e.svc.Create(context.Background(), in, e.actor); fieldOf(err) != field {
			t.Errorf("%s: err = %v", field, err)
		}
	}
	if len(e.rt.containers) != 0 {
		t.Fatal("no container may be created on invalid input")
	}
}

func TestCreateModded(t *testing.T) {
	e := newEnv(t)
	e.svc.cfg.LastPort = 25610
	ctx := context.Background()
	cases := []struct {
		name, loader, mc, lv string
		wantLV, wantEnv      string
		wantJava             string
	}{
		{"Forge", domain.LoaderForge, "1.20.1", "", "47.3.0", "FORGE_VERSION=47.3.0", domain.Java17},
		{"Forge pinned", domain.LoaderForge, "1.20.1", "47.4.5", "47.4.5", "FORGE_VERSION=47.4.5", domain.Java17},
		{"Fabric", domain.LoaderFabric, "1.21.1", "", "0.20.1", "FABRIC_LOADER_VERSION=0.20.1", domain.Java21},
	}
	e.rt.pulled = nil
	for _, c := range cases {
		in := validInput(c.name)
		in.Loader, in.Version, in.LoaderVersion = c.loader, c.mc, c.lv
		v, err := e.svc.Create(ctx, in, e.actor)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		stored, _ := e.repo.ByID(ctx, v.ID)
		if v.Loader != c.loader || v.LoaderVersion != c.wantLV || stored.LoaderVersion != c.wantLV || stored.Version != c.mc {
			t.Fatalf("%s: view %+v, stored %+v", c.name, v.Server, stored)
		}
		spec := e.rt.specs[v.ContainerID]
		env := strings.Join(spec.Env, " ")
		if spec.Java != c.wantJava || !strings.Contains(env, c.wantEnv) ||
			!strings.Contains(env, "TYPE="+strings.ToUpper(c.loader)) || !strings.Contains(env, "VERSION="+c.mc) {
			t.Fatalf("%s: java %s env %v", c.name, spec.Java, spec.Env)
		}
		if got := e.rt.pulled[len(e.rt.pulled)-1]; got != c.wantJava {
			t.Fatalf("%s: pulled %s", c.name, got)
		}
	}

	// Vanilla gets no loader variable and the newest Java for LATEST.
	v, err := e.svc.Create(ctx, validInput("Vanilla"), e.actor)
	if err != nil {
		t.Fatal(err)
	}
	if spec := e.rt.specs[v.ContainerID]; spec.Java != domain.Java25 || strings.Contains(strings.Join(spec.Env, " "), "LOADER") ||
		strings.Contains(strings.Join(spec.Env, " "), "FORGE") || v.LoaderVersion != "" {
		t.Fatalf("vanilla spec = %+v", spec)
	}

	bad := map[string]CreateInput{}
	in := validInput("Bad1")
	in.Loader = domain.LoaderForge // LATEST is vanilla-only
	bad["version"] = in
	in = validInput("Bad2")
	in.Loader, in.Version, in.LoaderVersion = domain.LoaderForge, "1.20.1", "47.9.9"
	bad["loader_version"] = in
	for field, in := range bad {
		if _, err := e.svc.Create(ctx, in, e.actor); fieldOf(err) != field {
			t.Errorf("%s: err = %v", field, err)
		}
	}

	e.src.down = true
	in = validInput("Down")
	in.Loader, in.Version = domain.LoaderNeoForge, "1.21.1"
	if _, err := e.svc.Create(ctx, in, e.actor); !errors.Is(err, domain.ErrInstallerUnavailable) {
		t.Fatalf("source down: err = %v", err)
	}
	if list, _ := e.repo.List(ctx); len(list) != 4 {
		t.Fatalf("servers = %d, want 4 (nothing created on failure)", len(list))
	}
}

func TestCreateRollsBackEveryStep(t *testing.T) {
	boom := errors.New("boom")
	for _, op := range []string{"pull", "create"} {
		e := newEnv(t)
		e.rt.fail[op] = boom
		if _, err := e.svc.Create(context.Background(), validInput("S"), e.actor); !errors.Is(err, boom) {
			t.Fatalf("%s: err = %v", op, err)
		}
		list, _ := e.repo.List(context.Background())
		entries, _ := os.ReadDir(e.dir)
		if len(list) != 0 || len(entries) != 0 || len(e.rt.containers) != 0 {
			t.Fatalf("%s: leftovers rows=%d dirs=%d containers=%d", op, len(list), len(entries), len(e.rt.containers))
		}
		events, _ := e.auditDB.Recent(context.Background(), 1)
		if len(events) != 1 || events[0].Action != domain.AuditServerCreate || events[0].Success {
			t.Fatalf("%s: audit = %+v", op, events)
		}
	}
}

func TestLifecycleAndStatus(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	v, err := e.svc.Create(ctx, validInput("S"), e.actor)
	if err != nil {
		t.Fatal(err)
	}
	steps := []struct {
		do   func(context.Context, string, Actor) (View, error)
		want domain.ServerStatus
	}{
		{e.svc.Start, domain.StatusRunning},
		{e.svc.Stop, domain.StatusStopped},
		{e.svc.Restart, domain.StatusRunning},
		{e.svc.Kill, domain.StatusStopped},
	}
	for i, st := range steps {
		got, err := st.do(ctx, v.ID, e.actor)
		if err != nil || got.Status != st.want {
			t.Fatalf("step %d: %v, %v", i, got.Status, err)
		}
	}

	if _, err := e.svc.Start(ctx, "AAAAAAAAAAAAAAAAAAAAAAAAAA", e.actor); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown server: %v", err)
	}

	// A container whose label points at another server is never acted on.
	e.rt.containers[v.ContainerID].ServerID = "OTHER"
	if _, err := e.svc.Start(ctx, v.ID, e.actor); !errors.Is(err, domain.ErrNotManaged) {
		t.Fatalf("foreign container: %v", err)
	}
	list, _ := e.svc.List(ctx)
	if list[0].Status != domain.StatusMissing {
		t.Fatalf("foreign container status = %s", list[0].Status)
	}

	delete(e.rt.containers, v.ContainerID)
	if got, _ := e.svc.Get(ctx, v.ID); got.Status != domain.StatusMissing {
		t.Fatalf("missing container status = %s", got.Status)
	}

	e.rt.fail["states"] = domain.ErrDockerUnavailable
	list, err = e.svc.List(ctx)
	if err != nil || list[0].Status != domain.StatusUnknown {
		t.Fatalf("docker down: %+v, %v", list, err)
	}
}

func TestUpdate(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	v, _ := e.svc.Create(ctx, validInput("S"), e.actor)
	_, _ = e.svc.Create(ctx, validInput("Other"), e.actor)
	name, mem := "Renamed", 4096

	if _, err := e.svc.Start(ctx, v.ID, e.actor); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Update(ctx, v.ID, UpdateInput{MemoryMB: &mem}, e.actor); !errors.Is(err, domain.ErrServerRunning) {
		t.Fatalf("limits while running: %v", err)
	}
	got, err := e.svc.Update(ctx, v.ID, UpdateInput{Name: &name}, e.actor)
	if err != nil || got.Name != "Renamed" {
		t.Fatalf("rename while running: %+v, %v", got, err)
	}
	other := "other"
	if _, err := e.svc.Update(ctx, v.ID, UpdateInput{Name: &other}, e.actor); fieldOf(err) != "name" {
		t.Fatalf("duplicate name: %v", err)
	}

	if _, err := e.svc.Stop(ctx, v.ID, e.actor); err != nil {
		t.Fatal(err)
	}
	got, err = e.svc.Update(ctx, v.ID, UpdateInput{MemoryMB: &mem}, e.actor)
	if err != nil || got.MemoryMB != 4096 || e.rt.limits[v.ContainerID] != [2]int{4096, 2000} {
		t.Fatalf("limits while stopped: %+v, %v, %v", got, err, e.rt.limits)
	}

	// Docker failure reverts the database.
	e.rt.fail["update"] = domain.ErrDockerFailed
	big := 8192
	if _, err := e.svc.Update(ctx, v.ID, UpdateInput{MemoryMB: &big}, e.actor); !errors.Is(err, domain.ErrDockerFailed) {
		t.Fatalf("docker failure: %v", err)
	}
	if x, _ := e.repo.ByID(ctx, v.ID); x.MemoryMB != 4096 {
		t.Fatalf("memory after failed update = %d", x.MemoryMB)
	}
}

func TestDelete(t *testing.T) {
	ctx := context.Background()
	for _, deleteFiles := range []bool{false, true} {
		e := newEnv(t)
		v, _ := e.svc.Create(ctx, validInput("S"), e.actor)
		_, _ = e.svc.Start(ctx, v.ID, e.actor)
		if err := e.svc.Delete(ctx, v.ID, deleteFiles, e.actor); err != nil {
			t.Fatal(err)
		}
		if len(e.rt.containers) != 0 {
			t.Fatal("container not removed")
		}
		if !strings.Contains(strings.Join(e.rt.calls, ","), "stop,remove") {
			t.Fatalf("not stopped before removal: %v", e.rt.calls)
		}
		if _, err := e.repo.ByID(ctx, v.ID); !errors.Is(err, domain.ErrNotFound) {
			t.Fatal("row not deleted")
		}
		if _, err := os.Stat(filepath.Join(e.dir, v.ID)); !os.IsNotExist(err) {
			t.Fatal("data dir still in place")
		}
		archived, _ := filepath.Glob(filepath.Join(e.dir, ".deleted", v.ID+"-*"))
		if deleteFiles == (len(archived) != 0) {
			t.Fatalf("deleteFiles=%v archived=%v", deleteFiles, archived)
		}
	}

	// A container that disappeared, or that is no longer ours, is only forgotten.
	e := newEnv(t)
	v, _ := e.svc.Create(ctx, validInput("S"), e.actor)
	e.rt.containers[v.ContainerID].ServerID = "OTHER"
	if err := e.svc.Delete(ctx, v.ID, true, e.actor); err != nil {
		t.Fatal(err)
	}
	if len(e.rt.containers) != 1 {
		t.Fatal("a container that is not ours was removed")
	}

	// Docker down: nothing is deleted.
	e = newEnv(t)
	v, _ = e.svc.Create(ctx, validInput("S"), e.actor)
	e.rt.fail["stop"] = domain.ErrDockerUnavailable
	if err := e.svc.Delete(ctx, v.ID, true, e.actor); !errors.Is(err, domain.ErrDockerUnavailable) {
		t.Fatalf("docker down: %v", err)
	}
	if _, err := e.repo.ByID(ctx, v.ID); err != nil {
		t.Fatal("row deleted although the container could not be removed")
	}
}

func TestCounts(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a, _ := e.svc.Create(ctx, validInput("A"), e.actor)
	_, _ = e.svc.Create(ctx, validInput("B"), e.actor)
	_, _ = e.svc.Start(ctx, a.ID, e.actor)
	c, err := e.svc.Counts(ctx)
	if err != nil || c != (domain.ServerCounts{Total: 2, Running: 1, Stopped: 1}) {
		t.Fatalf("counts = %+v, %v", c, err)
	}
}

func TestDeleteOrphanContainer(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	v, _ := e.svc.Create(ctx, validInput("S"), e.actor)

	// Still registered: refused, container kept.
	if err := e.svc.DeleteOrphanContainer(ctx, v.ContainerID, e.actor); !errors.Is(err, domain.ErrNotManaged) {
		t.Fatalf("registered server: %v", err)
	}
	if _, ok := e.rt.containers[v.ContainerID]; !ok {
		t.Fatal("container of a registered server was removed")
	}
	// Unknown container: not found.
	if err := e.svc.DeleteOrphanContainer(ctx, "nope", e.actor); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown container: %v", err)
	}
	// Orphan: removed.
	_ = e.repo.Delete(ctx, v.ID)
	if err := e.svc.DeleteOrphanContainer(ctx, v.ContainerID, e.actor); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.rt.containers[v.ContainerID]; ok {
		t.Fatal("orphan container not removed")
	}
}
