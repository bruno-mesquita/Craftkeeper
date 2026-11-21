package mods

import (
	"context"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bruno-mesquita/Craftkeeper/internal/application/servers"
	"github.com/bruno-mesquita/Craftkeeper/internal/domain"
	"github.com/bruno-mesquita/Craftkeeper/internal/infrastructure/filesystem"
)

const serverID = "SERVERSERVERSERVERSERVER22"

var actor = servers.Actor{UserID: "U", IP: "127.0.0.1"}

// fakeCatalog holds versions by ID; each version's file content is "<id>" unless corrupt.
type fakeCatalog struct {
	mu       sync.Mutex
	versions map[string]domain.ModVersion
	corrupt  map[string]bool // version ID → its download fails verification
	down     bool
}

func content(versionID string) []byte { return []byte("jar " + versionID) }

func hexSum(b []byte) string {
	s := sha512.Sum512(b)
	return hex.EncodeToString(s[:])
}

// add registers a version of project published day days in, depending on deps
// ("required:PROJ" or "incompatible:PROJ").
func (c *fakeCatalog) add(id, project string, day int, deps ...string) {
	data := content(id)
	v := domain.ModVersion{
		ID: id, ProjectID: project, Number: id, Type: domain.ModVersionRelease,
		Loaders: []string{"fabric"}, GameVersions: []string{"1.21.1"},
		Published: time.Date(2026, 1, 1+day, 0, 0, 0, 0, time.UTC),
		File:      domain.ModFile{URL: "cdn/" + id, Name: strings.ToLower(id) + ".jar", SHA512: hexSum(data), Size: int64(len(data))},
	}
	for _, d := range deps {
		kind, pid, _ := strings.Cut(d, ":")
		v.Dependencies = append(v.Dependencies, domain.ModDependency{ProjectID: pid, Kind: kind})
	}
	c.versions[id] = v
}

func (c *fakeCatalog) err() error {
	if c.down {
		return domain.ErrModrinthUnavailable
	}
	return nil
}

func (c *fakeCatalog) Search(context.Context, string, string, string, int) (domain.ModSearch, error) {
	return domain.ModSearch{}, c.err()
}

func (c *fakeCatalog) Projects(_ context.Context, ids []string) (map[string]domain.ModProject, error) {
	out := map[string]domain.ModProject{}
	for _, id := range ids {
		out[id] = domain.ModProject{ID: id, Title: "T " + id}
	}
	return out, c.err()
}

func (c *fakeCatalog) ProjectVersions(_ context.Context, project, _, _ string) ([]domain.ModVersion, error) {
	var out []domain.ModVersion
	for _, v := range c.versions {
		if v.ProjectID == project {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return nil, domain.ErrNotFound
	}
	return out, c.err()
}

func (c *fakeCatalog) Version(_ context.Context, id string) (domain.ModVersion, error) {
	v, ok := c.versions[id]
	if !ok {
		return v, domain.ErrNotFound
	}
	return v, c.err()
}

func (c *fakeCatalog) VersionsByHash(_ context.Context, hashes []string) (map[string]domain.ModVersion, error) {
	out := map[string]domain.ModVersion{}
	for _, v := range c.versions {
		if slices.Contains(hashes, v.File.SHA512) {
			out[v.File.SHA512] = v
		}
	}
	return out, c.err()
}

func (c *fakeCatalog) LatestByHash(ctx context.Context, hashes []string, _, _ string) (map[string]domain.ModVersion, error) {
	cur, err := c.VersionsByHash(ctx, hashes)
	out := map[string]domain.ModVersion{}
	for h, v := range cur {
		for _, x := range c.versions {
			if x.ProjectID == v.ProjectID && x.Published.After(v.Published) && (out[h].ID == "" || x.Published.After(out[h].Published)) {
				out[h] = x
			}
		}
	}
	return out, err
}

func (c *fakeCatalog) Download(_ context.Context, f domain.ModFile) (io.ReadCloser, error) {
	id := strings.TrimPrefix(f.URL, "cdn/")
	if c.corrupt[id] {
		return io.NopCloser(io.MultiReader(strings.NewReader("partial"), errReader{})), nil
	}
	return io.NopCloser(strings.NewReader(string(content(id)))), c.err()
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) {
	return 0, fmt.Errorf("%w: checksum", domain.ErrModrinthUnavailable)
}

func (c *fakeCatalog) Icon(context.Context, string) ([]byte, string, error) { return nil, "", c.err() }

type fakeServers struct{ view servers.View }

func (f *fakeServers) Get(context.Context, string) (servers.View, error) { return f.view, nil }

type fakeBackups struct{ calls int }

func (f *fakeBackups) Protect(ctx context.Context, _ string, _ servers.Actor, change func(context.Context) error) error {
	f.calls++
	return change(ctx)
}

type fakeAudit struct {
	mu     sync.Mutex
	events []string
}

func (f *fakeAudit) Record(_ context.Context, e domain.AuditEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, fmt.Sprintf("%s %s %v", e.Action, e.Target, e.Success))
	return nil
}

type env struct {
	svc     *Service
	dir     string
	catalog *fakeCatalog
	servers *fakeServers
	backups *fakeBackups
	audit   *fakeAudit
}

func newEnv(t *testing.T) *env {
	t.Helper()
	root := t.TempDir()
	e := &env{
		dir:     filepath.Join(root, serverID),
		catalog: &fakeCatalog{versions: map[string]domain.ModVersion{}, corrupt: map[string]bool{}},
		servers: &fakeServers{view: servers.View{Server: domain.Server{ID: serverID, Name: "S", Loader: "fabric", Version: "1.21.1"}, Status: domain.StatusRunning}},
		backups: &fakeBackups{},
		audit:   &fakeAudit{},
	}
	if err := os.Mkdir(e.dir, 0o750); err != nil {
		t.Fatal(err)
	}
	e.svc = NewService(e.catalog, filesystem.NewServerFiles(root, os.Getuid(), os.Getgid()), e.servers, e.backups, e.audit)
	e.svc.reserve = 0
	return e
}

func (e *env) mods(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(e.dir, domain.ModsDir))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var out []string
	for _, d := range entries {
		out = append(out, d.Name())
	}
	return out
}

func (e *env) put(t *testing.T, name string, data []byte) {
	t.Helper()
	os.MkdirAll(filepath.Join(e.dir, domain.ModsDir), 0o750)
	if err := os.WriteFile(filepath.Join(e.dir, domain.ModsDir, name), data, 0o640); err != nil {
		t.Fatal(err)
	}
}

func TestInstallResolvesDependencies(t *testing.T) {
	e := newEnv(t)
	e.catalog.add("ROOTAAA1", "ROOTPROJ", 1, "required:LIBPROJ1", "required:APIPROJ1", "optional:OPTPROJ1")
	e.catalog.add("LIBAAAA1", "LIBPROJ1", 1, "required:APIPROJ1")
	e.catalog.add("APIAAAA1", "APIPROJ1", 1)
	e.catalog.add("APIAAAA2", "APIPROJ1", 2)
	e.catalog.add("OPTAAAA1", "OPTPROJ1", 1)
	e.put(t, "api.jar.disabled", content("APIAAAA1")) // installed (disabled): not downloaded again

	res, err := e.svc.Install(context.Background(), serverID, "ROOTPROJ", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.Installed, []string{"rootaaa1.jar", "libaaaa1.jar"}) {
		t.Fatalf("installed = %v", res.Installed)
	}
	if got := e.mods(t); !slices.Equal(got, []string{"api.jar.disabled", "libaaaa1.jar", "rootaaa1.jar"}) {
		t.Fatalf("mods = %v", got)
	}
	if b, _ := os.ReadFile(filepath.Join(e.dir, "mods", "rootaaa1.jar")); string(b) != "jar ROOTAAA1" {
		t.Fatalf("content = %q", b)
	}
	if _, err := e.svc.Install(context.Background(), serverID, "ROOTPROJ", "", actor); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("reinstall: %v", err)
	}
	if e.backups.calls != 0 {
		t.Fatal("install must not back up")
	}
}

func TestInstallPicksNewestRelease(t *testing.T) {
	e := newEnv(t)
	e.catalog.add("MODAAAA1", "MODPROJ1", 1)
	e.catalog.add("MODAAAA2", "MODPROJ1", 3)
	beta := e.catalog.versions["MODAAAA2"]
	beta.Type = "beta"
	e.catalog.versions["MODAAAA2"] = beta
	e.catalog.add("MODAAAA3", "MODPROJ1", 2)
	other := e.catalog.versions["MODAAAA3"]
	other.GameVersions = []string{"1.20.1"} // does not run on the server
	e.catalog.versions["MODAAAA3"] = other

	if _, err := e.svc.Install(context.Background(), serverID, "MODPROJ1", "MODAAAA3", actor); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("version for another Minecraft: %v", err)
	}
	res, err := e.svc.Install(context.Background(), serverID, "MODPROJ1", "", actor)
	if err != nil || !slices.Equal(res.Installed, []string{"modaaaa1.jar"}) {
		t.Fatalf("install = %v, %v", res, err)
	}
}

func TestInstallIncompatible(t *testing.T) {
	e := newEnv(t)
	e.catalog.add("OLDAAAA1", "OLDPROJ1", 1, "incompatible:NEWPROJ1")
	e.catalog.add("NEWAAAA1", "NEWPROJ1", 1)
	e.catalog.add("BADAAAA1", "BADPROJ1", 1, "incompatible:OLDPROJ1")
	e.put(t, "old.jar", content("OLDAAAA1"))

	for _, p := range []string{"NEWPROJ1", "BADPROJ1"} {
		if _, err := e.svc.Install(context.Background(), serverID, p, "", actor); !errors.Is(err, domain.ErrModIncompatible) {
			t.Fatalf("%s: %v", p, err)
		}
	}
	if got := e.mods(t); !slices.Equal(got, []string{"old.jar"}) {
		t.Fatalf("mods = %v", got)
	}
}

func TestInstallLimit(t *testing.T) {
	e := newEnv(t)
	prev := ""
	for i := range domain.ModsPerOperation + 1 {
		id, project := fmt.Sprintf("VERS%04d", i), fmt.Sprintf("PROJ%04d", i)
		if prev == "" {
			e.catalog.add(id, project, 1)
		} else {
			e.catalog.add(id, project, 1, "required:"+prev)
		}
		prev = project
	}
	if _, err := e.svc.Install(context.Background(), serverID, prev, "", actor); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("chain of %d: %v", domain.ModsPerOperation+1, err)
	}
	if got := e.mods(t); len(got) != 0 {
		t.Fatalf("mods = %v", got)
	}
}

func TestFailedDownloadLeavesNothing(t *testing.T) {
	e := newEnv(t)
	e.catalog.add("ROOTAAA1", "ROOTPROJ", 1, "required:LIBPROJ1")
	e.catalog.add("LIBAAAA1", "LIBPROJ1", 1)
	e.catalog.corrupt["LIBAAAA1"] = true

	if _, err := e.svc.Install(context.Background(), serverID, "ROOTPROJ", "", actor); !errors.Is(err, domain.ErrModrinthUnavailable) {
		t.Fatalf("install: %v", err)
	}
	if got := e.mods(t); len(got) != 0 {
		t.Fatalf("mods left behind: %v", got)
	}
	if len(e.audit.events) != 1 || !strings.HasSuffix(e.audit.events[0], "false") {
		t.Fatalf("audit = %v", e.audit.events)
	}
}

func TestUpdate(t *testing.T) {
	e := newEnv(t)
	e.catalog.add("AAAAAAA1", "AAAAPROJ", 1)
	e.catalog.add("AAAAAAA2", "AAAAPROJ", 2, "required:NEWDEPPR")
	e.catalog.add("BBBBBBB1", "BBBBPROJ", 1)
	e.catalog.add("BBBBBBB2", "BBBBPROJ", 2)
	e.catalog.add("DEPAAAA1", "NEWDEPPR", 1)
	e.put(t, "a.jar.disabled", content("AAAAAAA1"))
	e.put(t, "b.jar", content("BBBBBBB1"))
	e.put(t, "manual.jar", []byte("unknown"))

	l, err := e.svc.List(context.Background(), serverID)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Mods) != 3 || l.Mods[0].Update == nil || l.Mods[2].Version != nil || l.Mods[2].File != "manual.jar" {
		t.Fatalf("list = %+v", l)
	}

	if _, err := e.svc.Update(context.Background(), serverID, []string{"manual.jar"}, true, actor); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("update of an unknown mod: %v", err)
	}
	res, err := e.svc.Update(context.Background(), serverID, []string{"a.jar.disabled"}, true, actor)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Updated) != 1 || res.Updated[0].To != "aaaaaaa2.jar.disabled" || !slices.Equal(res.Installed, []string{"depaaaa1.jar"}) {
		t.Fatalf("update = %+v", res)
	}
	if got := e.mods(t); !slices.Equal(got, []string{"aaaaaaa2.jar.disabled", "b.jar", "depaaaa1.jar", "manual.jar"}) {
		t.Fatalf("mods = %v", got)
	}
	if e.backups.calls != 1 {
		t.Fatalf("backups = %d", e.backups.calls)
	}

	res, err = e.svc.Update(context.Background(), serverID, nil, false, actor) // everything left: b, without a backup
	if err != nil || len(res.Updated) != 1 || res.Updated[0].From != "b.jar" || e.backups.calls != 1 {
		t.Fatalf("update all = %+v, %v", res, err)
	}
	res, err = e.svc.Update(context.Background(), serverID, nil, true, actor) // nothing left: no backup
	if err != nil || len(res.Updated) != 0 || e.backups.calls != 1 {
		t.Fatalf("no-op update = %+v, %v, %d backups", res, err, e.backups.calls)
	}
}

func TestEnableRemoveAndScope(t *testing.T) {
	e := newEnv(t)
	e.put(t, "x.jar", []byte("x"))
	ctx := context.Background()

	name, err := e.svc.SetEnabled(ctx, serverID, "x.jar", false, actor)
	if err != nil || name != "x.jar.disabled" {
		t.Fatalf("disable = %q, %v", name, err)
	}
	if name, err = e.svc.SetEnabled(ctx, serverID, "x.jar.disabled", true, actor); err != nil || name != "x.jar" {
		t.Fatalf("enable = %q, %v", name, err)
	}
	if _, err := e.svc.SetEnabled(ctx, serverID, "../x.jar", true, actor); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("traversal: %v", err)
	}
	if err := e.svc.Remove(ctx, serverID, "x.jar", actor); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Remove(ctx, serverID, "x.jar", actor); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("second remove: %v", err)
	}

	e.put(t, "y.jar", []byte("y"))
	e.catalog.down = true
	l, err := e.svc.List(ctx, serverID)
	if err != nil || !l.CatalogUnavailable || len(l.Mods) != 1 {
		t.Fatalf("degraded list = %+v, %v", l, err)
	}

	os.WriteFile(filepath.Join(e.dir, domain.ModpackConfigFile), []byte("a.sh\nb.sh\n"), 0o640)
	if _, err := e.svc.List(ctx, serverID); !errors.Is(err, domain.ErrModpackServer) {
		t.Fatalf("modpack: %v", err)
	}
	e.servers.view.Loader = domain.LoaderVanilla
	if _, err := e.svc.List(ctx, serverID); !errors.Is(err, domain.ErrModsUnsupported) {
		t.Fatalf("vanilla: %v", err)
	}
}
