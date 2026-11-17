package files

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/bruno-mesquita/mesquita-panel/internal/application/servers"
	"github.com/bruno-mesquita/mesquita-panel/internal/domain"
)

const sid = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"

type fakeServers struct{}

func (fakeServers) Get(_ context.Context, id string) (servers.View, error) {
	if id != sid {
		return servers.View{}, domain.ErrNotFound
	}
	return servers.View{Server: domain.Server{ID: sid, Name: "Survival"}, Status: domain.StatusRunning}, nil
}
func (fakeServers) Start(_ context.Context, id string, _ servers.Actor) (servers.View, error) {
	return servers.View{Server: domain.Server{ID: id, Name: "Survival"}, Status: domain.StatusRunning}, nil
}

// fakeFiles records calls; fail makes every mutation fail.
type fakeFiles struct {
	calls []string
	fail  error
}

func (f *fakeFiles) call(s string) error {
	f.calls = append(f.calls, s)
	return f.fail
}

func (f *fakeFiles) List(id, dir string) (domain.FileListing, error) {
	return domain.FileListing{Path: dir}, f.call("list " + dir)
}
func (f *fakeFiles) Stat(id, p string) (domain.FileEntry, error) {
	if p == "world" || p == "" {
		return domain.FileEntry{Name: "world", Type: domain.FileTypeDir}, f.call("stat " + p)
	}
	return domain.FileEntry{Name: "a.txt", Type: domain.FileTypeFile}, f.call("stat " + p)
}
func (f *fakeFiles) OpenFile(id, p string) (*os.File, int64, error) {
	return nil, 3, f.call("open " + p)
}
func (f *fakeFiles) WriteZip(id, dir string, w io.Writer) error {
	return f.call("zip " + dir)
}
func (f *fakeFiles) ReadText(id, p string) (domain.FileText, error) {
	return domain.FileText{}, f.call("read " + p)
}
func (f *fakeFiles) WriteText(id, p, content, version string) (string, error) {
	return "v2", f.call("write " + p + " " + version)
}
func (f *fakeFiles) Save(id, dir, name string, src io.Reader, overwrite bool) (domain.FileEntry, error) {
	b, _ := io.ReadAll(src)
	return domain.FileEntry{Name: name}, f.call("save " + dir + " " + name + " " + string(b))
}
func (f *fakeFiles) Mkdir(id, p string) error {
	return f.call("mkdir " + p)
}
func (f *fakeFiles) Rename(id, from, to string) error {
	return f.call("rename " + from + " " + to)
}
func (f *fakeFiles) Remove(id, p string) error {
	return f.call("remove " + p)
}
func (f *fakeFiles) Extract(id, a, d string, o bool) (int, error) {
	return 2, f.call("extract " + a + " " + d)
}
func (f *fakeFiles) FindShellScripts(id string) ([]string, error) {
	return []string{}, f.call("find shell scripts")
}

type fakeAudit struct{ events []domain.AuditEvent }

func (a *fakeAudit) Record(_ context.Context, e domain.AuditEvent) error {
	a.events = append(a.events, e)
	return nil
}

var actor = servers.Actor{UserID: "U1", IP: "100.64.0.1"}

func TestMutationsAreCleanedAndAudited(t *testing.T) {
	f, audit := &fakeFiles{}, &fakeAudit{}
	s := NewService(f, fakeServers{}, audit)
	ctx := context.Background()

	if _, err := s.WriteText(ctx, sid, "/server.properties", "motd=x", "v1", actor); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Upload(ctx, sid, "/plugins/", "x.jar", strings.NewReader("jar"), false, actor); err != nil {
		t.Fatal(err)
	}
	if err := s.Mkdir(ctx, sid, "mods//", actor); err != nil {
		t.Fatal(err)
	}
	if err := s.Rename(ctx, sid, "mods", "/old/mods", actor); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, sid, "old", actor); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Extract(ctx, sid, "pack.zip", "/", false, actor); err != nil {
		t.Fatal(err)
	}

	wantCalls := "write server.properties v1|save plugins x.jar jar|mkdir mods|rename mods old/mods|remove old|extract pack.zip "
	if got := strings.Join(f.calls, "|"); got != wantCalls {
		t.Fatalf("calls = %q", got)
	}
	want := []string{
		"file.write Survival: /server.properties",
		"file.upload Survival: /plugins/x.jar",
		"file.mkdir Survival: /mods",
		"file.rename Survival: /mods -> /old/mods",
		"file.delete Survival: /old",
		"file.extract Survival: /pack.zip -> /",
	}
	for i, e := range audit.events {
		if got := e.Action + " " + e.Target; got != want[i] || !e.Success || e.UserID != "U1" || e.IP != "100.64.0.1" {
			t.Errorf("audit %d = %+v, want %s", i, e, want[i])
		}
	}

	f.fail = domain.ErrFileExists
	if err := s.Mkdir(ctx, sid, "mods", actor); !errors.Is(err, domain.ErrFileExists) {
		t.Fatal(err)
	}
	if last := audit.events[len(audit.events)-1]; last.Success {
		t.Fatal("failed mutation audited as successful")
	}
}

func TestRejectedRequestsTouchNothing(t *testing.T) {
	f, audit := &fakeFiles{}, &fakeAudit{}
	s := NewService(f, fakeServers{}, audit)
	ctx := context.Background()
	var ve *domain.ValidationError

	_, uploadErr := s.Upload(ctx, sid, "", "../x", strings.NewReader(""), false, actor)
	_, editErr := s.ReadText(ctx, sid, "/")
	checks := map[string]error{
		"delete root":     s.Delete(ctx, sid, "/", actor),
		"mkdir root":      s.Mkdir(ctx, sid, "", actor),
		"rename root":     s.Rename(ctx, sid, "", "x", actor),
		"rename to root":  s.Rename(ctx, sid, "x", "/", actor),
		"move into self":  s.Rename(ctx, sid, "world", "world/sub", actor),
		"rename same":     s.Rename(ctx, sid, "a", "a/", actor),
		"traversal":       s.Delete(ctx, sid, "../other", actor),
		"bad upload name": uploadErr,
		"edit root":       editErr,
	}
	for name, err := range checks {
		if !errors.As(err, &ve) {
			t.Errorf("%s: %v, want validation error", name, err)
		}
	}
	if err := s.Delete(ctx, "ZZZZZZZZZZZZZZZZZZZZZZZZZZ", "x", actor); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("unknown server: %v", err)
	}
	if len(f.calls) != 0 || len(audit.events) != 0 {
		t.Fatalf("calls %v, audit %v", f.calls, audit.events)
	}
	// "worldx" is not inside "world".
	if err := s.Rename(ctx, sid, "world", "worldx", actor); err != nil {
		t.Fatal(err)
	}
}

func TestDownloadResolvesFilesAndDirectories(t *testing.T) {
	s := NewService(&fakeFiles{}, fakeServers{}, &fakeAudit{})
	d, err := s.Download(context.Background(), sid, "/world/")
	if err != nil || d.Name != "world.zip" || d.Dir != "world" || d.File != nil {
		t.Fatalf("dir download = %+v, %v", d, err)
	}
	d, err = s.Download(context.Background(), sid, "")
	if err != nil || d.Name != "server.zip" || d.Dir != "" {
		t.Fatalf("root download = %+v, %v", d, err)
	}
	d, err = s.Download(context.Background(), sid, "a.txt")
	if err != nil || d.Name != "a.txt" || d.Size != 3 {
		t.Fatalf("file download = %+v, %v", d, err)
	}
}
