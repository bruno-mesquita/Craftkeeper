// Package files implements the file manager of a server's data directory: listing,
// download, upload, text editing, directories, renames, deletion and zip extraction.
// Writes are allowed while the server runs (the UI warns); confinement to the server's
// directory is enforced by the Files adapter.
package files

import (
	"context"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/bruno-mesquita/mesquita-panel/internal/application/servers"
	"github.com/bruno-mesquita/mesquita-panel/internal/domain"
)

// Files reaches the contents of server directories (filesystem.ServerFiles). Paths are
// cleaned with domain.CleanServerPath before they get here.
type Files interface {
	List(id, dir string) (domain.FileListing, error)
	Stat(id, p string) (domain.FileEntry, error)
	OpenFile(id, p string) (*os.File, int64, error)
	WriteZip(id, dir string, w io.Writer) error
	ReadText(id, p string) (domain.FileText, error)
	WriteText(id, p, content, version string) (string, error)
	Save(id, dir, name string, src io.Reader, overwrite bool) (domain.FileEntry, error)
	Mkdir(id, p string) error
	Rename(id, from, to string) error
	Remove(id, p string) error
	Extract(id, archive, dest string, overwrite bool) (int, error)
	FindShellScripts(id string) ([]string, error)
}

// Servers resolves a server (servers.Service).
type Servers interface {
	Get(ctx context.Context, id string) (servers.View, error)
	Start(ctx context.Context, id string, actor servers.Actor) (servers.View, error)
}

type Modpack struct {
	Supported     bool     `json:"supported"`
	Scripts       []string `json:"scripts"`
	InstallScript string   `json:"install_script"`
	StartScript   string   `json:"start_script"`
	Installed     bool     `json:"installed"`
}

type AuditRecorder interface {
	Record(ctx context.Context, e domain.AuditEvent) error
}

type Service struct {
	files   Files
	servers Servers
	audit   AuditRecorder
	now     func() time.Time
}

func NewService(f Files, s Servers, audit AuditRecorder) *Service {
	return &Service{files: f, servers: s, audit: audit, now: time.Now}
}

// clean validates a path; nonRoot rejects the server's root directory itself.
func clean(field, p string, nonRoot bool) (string, error) {
	c, err := domain.CleanServerPath(field, p)
	if err != nil {
		return "", err
	}
	if nonRoot && c == "" {
		return "", &domain.ValidationError{Field: field, Message: "the server's root directory cannot be used here"}
	}
	return c, nil
}

func (s *Service) List(ctx context.Context, id, dir string) (domain.FileListing, error) {
	dir, err := clean("path", dir, false)
	if err != nil {
		return domain.FileListing{}, err
	}
	if _, err := s.servers.Get(ctx, id); err != nil {
		return domain.FileListing{}, err
	}
	return s.files.List(id, dir)
}

// Download is what a download request resolves to: a regular file, or a directory to be
// streamed as a zip archive by WriteZip.
type Download struct {
	Name string   // file name to offer the browser
	File *os.File // nil for a directory
	Size int64
	Dir  string // cleaned path of the directory, when File is nil
}

func (s *Service) Download(ctx context.Context, id, p string) (Download, error) {
	p, err := clean("path", p, false)
	if err != nil {
		return Download{}, err
	}
	if _, err := s.servers.Get(ctx, id); err != nil {
		return Download{}, err
	}
	st, err := s.files.Stat(id, p)
	if err != nil {
		return Download{}, err
	}
	if st.Type == domain.FileTypeDir {
		n := st.Name
		if p == "" {
			n = "server"
		}
		return Download{Name: n + ".zip", Dir: p}, nil
	}
	f, size, err := s.files.OpenFile(id, p)
	if err != nil {
		return Download{}, err
	}
	return Download{Name: st.Name, File: f, Size: size}, nil
}

// WriteZip streams a directory resolved by Download.
func (s *Service) WriteZip(id string, d Download, w io.Writer) error {
	return s.files.WriteZip(id, d.Dir, w)
}

func (s *Service) ReadText(ctx context.Context, id, p string) (domain.FileText, error) {
	p, err := clean("path", p, true)
	if err != nil {
		return domain.FileText{}, err
	}
	if _, err := s.servers.Get(ctx, id); err != nil {
		return domain.FileText{}, err
	}
	return s.files.ReadText(id, p)
}

func (s *Service) WriteText(ctx context.Context, id, p, content, version string, actor servers.Actor) (string, error) {
	p, err := clean("path", p, true)
	if err != nil {
		return "", err
	}
	srv, err := s.servers.Get(ctx, id)
	if err != nil {
		return "", err
	}
	v, err := s.files.WriteText(id, p, content, version)
	s.record(ctx, actor, domain.AuditFileWrite, srv.Name, "/"+p, err)
	return v, err
}

// Upload stores one uploaded file in directory dir.
func (s *Service) Upload(ctx context.Context, id, dir, name string, src io.Reader, overwrite bool, actor servers.Actor) (domain.FileEntry, error) {
	dir, err := clean("path", dir, false)
	if err != nil {
		return domain.FileEntry{}, err
	}
	if err := domain.ValidateFileName("name", name); err != nil {
		return domain.FileEntry{}, err
	}
	srv, err := s.servers.Get(ctx, id)
	if err != nil {
		return domain.FileEntry{}, err
	}
	e, err := s.files.Save(id, dir, name, src, overwrite)
	s.record(ctx, actor, domain.AuditFileUpload, srv.Name, "/"+path.Join(dir, name), err)
	return e, err
}

func (s *Service) Mkdir(ctx context.Context, id, p string, actor servers.Actor) error {
	p, err := clean("path", p, true)
	if err != nil {
		return err
	}
	srv, err := s.servers.Get(ctx, id)
	if err != nil {
		return err
	}
	err = s.files.Mkdir(id, p)
	s.record(ctx, actor, domain.AuditFileMkdir, srv.Name, "/"+p, err)
	return err
}

func (s *Service) Rename(ctx context.Context, id, from, to string, actor servers.Actor) error {
	from, err := clean("from", from, true)
	if err != nil {
		return err
	}
	to, err = clean("to", to, true)
	if err != nil {
		return err
	}
	if to == from || len(to) > len(from) && to[:len(from)+1] == from+"/" {
		return &domain.ValidationError{Field: "to", Message: "cannot move a directory into itself"}
	}
	srv, err := s.servers.Get(ctx, id)
	if err != nil {
		return err
	}
	err = s.files.Rename(id, from, to)
	s.record(ctx, actor, domain.AuditFileRename, srv.Name, "/"+from+" -> /"+to, err)
	return err
}

func (s *Service) Delete(ctx context.Context, id, p string, actor servers.Actor) error {
	p, err := clean("path", p, true)
	if err != nil {
		return err
	}
	srv, err := s.servers.Get(ctx, id)
	if err != nil {
		return err
	}
	err = s.files.Remove(id, p)
	s.record(ctx, actor, domain.AuditFileDelete, srv.Name, "/"+p, err)
	return err
}

// Extract unpacks a zip archive of the server into directory dest.
func (s *Service) Extract(ctx context.Context, id, archive, dest string, overwrite bool, actor servers.Actor) (int, error) {
	archive, err := clean("path", archive, true)
	if err != nil {
		return 0, err
	}
	dest, err = clean("destination", dest, false)
	if err != nil {
		return 0, err
	}
	srv, err := s.servers.Get(ctx, id)
	if err != nil {
		return 0, err
	}
	n, err := s.files.Extract(id, archive, dest, overwrite)
	s.record(ctx, actor, domain.AuditFileExtract, srv.Name, "/"+archive+" -> /"+dest, err)
	return n, err
}

// ModpackInfo discovers shell scripts in the server directory and reports the configured
// scripts plus whether the one-time installer has completed.
func (s *Service) ModpackInfo(ctx context.Context, id string) (Modpack, error) {
	if _, err := s.servers.Get(ctx, id); err != nil {
		return Modpack{}, err
	}
	scripts, err := s.files.FindShellScripts(id)
	if err != nil {
		return Modpack{}, err
	}
	info := Modpack{Scripts: scripts}
	e, err := s.files.Stat(id, ".mesquita-modpack-supported")
	if err == nil && e.Type == domain.FileTypeFile {
		info.Supported = true
	} else if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return Modpack{}, err
	}
	f, _, err := s.files.OpenFile(id, ".mesquita-modpack")
	if err == nil && f != nil {
		defer f.Close()
		b, readErr := io.ReadAll(io.LimitReader(f, 4096))
		if readErr != nil {
			return Modpack{}, readErr
		}
		lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
		if len(lines) == 2 {
			info.InstallScript, info.StartScript = lines[0], lines[1]
		}
	} else if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return Modpack{}, err
	}
	e, err = s.files.Stat(id, ".mesquita-modpack-installed")
	if err == nil && e.Type == domain.FileTypeFile {
		info.Installed = true
	} else if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return Modpack{}, err
	}
	return info, nil
}

// ConfigureModpack persists the user's explicit script selection after confirming both
// paths came from regular shell scripts inside the server directory.
func (s *Service) ConfigureModpack(ctx context.Context, id, install, start string, actor servers.Actor) error {
	cleanInstall, err := clean("install_script", install, true)
	if err != nil {
		return err
	}
	cleanStart, err := clean("start_script", start, true)
	if err != nil {
		return err
	}
	if !strings.EqualFold(filepath.Ext(cleanInstall), ".sh") || !strings.EqualFold(filepath.Ext(cleanStart), ".sh") {
		return &domain.ValidationError{Field: "scripts", Message: "installer and startup must be .sh files"}
	}
	if cleanInstall == cleanStart {
		return &domain.ValidationError{Field: "scripts", Message: "installer and startup scripts must be different"}
	}
	srv, err := s.servers.Get(ctx, id)
	if err != nil {
		return err
	}
	if serverActive(srv.Status) {
		return domain.ErrServerRunning
	}
	modpack, err := s.ModpackInfo(ctx, id)
	if err != nil {
		return err
	}
	if !modpack.Supported {
		return &domain.ValidationError{Field: "server", Message: "this server predates modpack support; create a new server to use modpacks"}
	}
	if !slices.Contains(modpack.Scripts, cleanInstall) || !slices.Contains(modpack.Scripts, cleanStart) {
		return &domain.ValidationError{Field: "scripts", Message: "select scripts found in the server files"}
	}
	_, err = s.files.WriteText(id, ".mesquita-modpack", cleanInstall+"\n"+cleanStart+"\n", "")
	if err == nil && modpack.Installed && modpack.InstallScript != cleanInstall {
		if removeErr := s.files.Remove(id, ".mesquita-modpack-installed"); removeErr != nil && !errors.Is(removeErr, domain.ErrNotFound) {
			err = removeErr
		}
	}
	s.record(ctx, actor, domain.AuditModpackConfigure, srv.Name, cleanInstall+" -> "+cleanStart, err)
	return err
}

// InstallModpack starts the managed container. Its fixed in-container wrapper runs the
// installer once and then the startup script; later starts execute only the startup script.
func (s *Service) InstallModpack(ctx context.Context, id string, actor servers.Actor) (servers.View, error) {
	info, err := s.ModpackInfo(ctx, id)
	if err != nil {
		return servers.View{}, err
	}
	if !info.Supported {
		return servers.View{}, &domain.ValidationError{Field: "server", Message: "this server predates modpack support; create a new server to use modpacks"}
	}
	if info.InstallScript == "" || info.StartScript == "" {
		return servers.View{}, &domain.ValidationError{Field: "scripts", Message: "upload a server pack and configure its scripts first"}
	}
	if !slices.Contains(info.Scripts, info.InstallScript) || !slices.Contains(info.Scripts, info.StartScript) {
		return servers.View{}, &domain.ValidationError{Field: "scripts", Message: "configured scripts are missing; select them again"}
	}
	srv, err := s.servers.Get(ctx, id)
	if err != nil {
		return servers.View{}, err
	}
	if serverActive(srv.Status) {
		return servers.View{}, domain.ErrServerRunning
	}
	v, err := s.servers.Start(ctx, id, actor)
	s.record(ctx, actor, domain.AuditModpackInstall, srv.Name, info.InstallScript, err)
	return v, err
}

func serverActive(status domain.ServerStatus) bool {
	return status == domain.StatusRunning || status == domain.StatusStarting || status == domain.StatusRestarting
}

// record audits a mutation. Validation failures never reach it; failures of the operation
// itself are recorded as unsuccessful.
func (s *Service) record(ctx context.Context, actor servers.Actor, action, server, target string, err error) {
	_ = s.audit.Record(context.WithoutCancel(ctx), domain.AuditEvent{
		CreatedAt: s.now().UTC(), UserID: actor.UserID, Action: action,
		Target: truncate(server+": "+target, 300), IP: actor.IP, Success: err == nil,
	})
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n] + "…"
}
