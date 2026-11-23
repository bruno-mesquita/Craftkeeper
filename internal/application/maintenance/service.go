// Package maintenance implements the changes that can lose a world: changing a server's game
// version or loader, resetting its world and importing another one. Each runs on a stopped
// server, right after a full backup of it, so it can always be undone by a restore.
package maintenance

import (
	"context"
	"crypto/rand"
	"errors"
	"path"
	"strings"
	"time"

	"github.com/bruno-mesquita/Craftkeeper/internal/application/servers"
	"github.com/bruno-mesquita/Craftkeeper/internal/domain"
)

// Servers resolves servers and replaces their container (servers.Service).
type Servers interface {
	Get(ctx context.Context, id string) (servers.View, error)
	ResolveVersion(ctx context.Context, in servers.VersionInput) (string, error)
	ChangeVersion(ctx context.Context, id string, in servers.VersionInput, actor servers.Actor) (servers.View, error)
	ChangeModpack(ctx context.Context, id string, ref domain.ModpackRef, game servers.VersionInput, actor servers.Actor) (servers.View, error)
}

// Backups makes the backup that protects each change (backups.Service).
type Backups interface {
	Protect(ctx context.Context, serverID string, actor servers.Actor, change func(ctx context.Context) error) error
}

// Files reaches a server's directory (filesystem.ServerFiles).
type Files interface {
	ReadText(id, p string) (domain.FileText, error)
	WriteText(id, p, content, version string) (string, error)
	Stat(id, p string) (domain.FileEntry, error)
	List(id, dir string) (domain.FileListing, error)
	Mkdir(id, p string) error
	Rename(id, from, to string) error
	Remove(id, p string) error
	Extract(id, archive, dest string, overwrite bool) (int, error)
}

type AuditRecorder interface {
	Record(ctx context.Context, e domain.AuditEvent) error
}

type Service struct {
	servers Servers
	backups Backups
	files   Files
	audit   AuditRecorder
	now     func() time.Time
}

func NewService(srv Servers, b Backups, f Files, audit AuditRecorder) *Service {
	return &Service{servers: srv, backups: b, files: f, audit: audit, now: time.Now}
}

// ChangeVersion backs a stopped server up and makes it run another Minecraft version and/or
// loader. Servers that run a modpack are refused: the pack's scripts choose the version.
func (s *Service) ChangeVersion(ctx context.Context, id string, in servers.VersionInput, actor servers.Actor) (servers.View, error) {
	if v, err := s.stopped(ctx, id); err != nil {
		return servers.View{}, err
	} else if v.Modpack.Set() {
		return servers.View{}, domain.ErrModpackServer
	}
	if _, err := s.files.Stat(id, domain.ModpackConfigFile); err == nil {
		return servers.View{}, domain.ErrModpackServer
	} else if !errors.Is(err, domain.ErrNotFound) {
		return servers.View{}, err
	}
	// A bad version must not cost a backup.
	if _, err := s.servers.ResolveVersion(ctx, in); err != nil {
		return servers.View{}, err
	}
	var v servers.View
	err := s.backups.Protect(ctx, id, actor, func(ctx context.Context) error {
		var err error
		v, err = s.servers.ChangeVersion(ctx, id, in, actor)
		return err
	})
	return v, err
}

// ChangeModpack backs a stopped server that runs a catalog modpack up and makes it run another
// version of that pack (ref, resolved from the catalog with the game it runs). The world is
// kept: the game image only replaces the files of the previous pack version.
func (s *Service) ChangeModpack(ctx context.Context, id string, ref domain.ModpackRef, game servers.VersionInput, actor servers.Actor) (servers.View, error) {
	v, err := s.stopped(ctx, id)
	if err != nil {
		return servers.View{}, err
	}
	if !v.Modpack.Set() || v.Modpack.Source != ref.Source || v.Modpack.ProjectID != ref.ProjectID {
		return servers.View{}, domain.ErrNotFound // not this server's modpack
	}
	err = s.backups.Protect(ctx, id, actor, func(ctx context.Context) error {
		var err error
		v, err = s.servers.ChangeModpack(ctx, id, ref, game, actor)
		return err
	})
	return v, err
}

// ResetWorld backs a stopped server up and deletes its world, which the game generates again
// on the next start. A seed, when given, is stored as level-seed first ("" = random).
func (s *Service) ResetWorld(ctx context.Context, id string, seed *string, actor servers.Actor) error {
	if seed != nil {
		*seed = strings.TrimSpace(*seed)
		if err := domain.ValidateSeed(*seed); err != nil {
			return err
		}
	}
	srv, err := s.stopped(ctx, id)
	if err != nil {
		return err
	}
	props, err := s.properties(id)
	if err != nil {
		return err
	}
	world, err := domain.LevelName(props.Content)
	if err != nil {
		return err
	}
	err = s.backups.Protect(ctx, id, actor, func(ctx context.Context) error {
		if _, err := s.stopped(ctx, id); err != nil {
			return err
		}
		if err := s.files.Remove(id, world); err != nil && !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		if seed == nil {
			return nil
		}
		// Re-read: the backup took a while and the file may have changed meanwhile.
		props, err := s.properties(id)
		if err != nil {
			return err
		}
		_, err = s.files.WriteText(id, domain.PropertiesFile, domain.SetProperty(props.Content, "level-seed", *seed), props.Version)
		return err
	})
	s.record(ctx, actor, domain.AuditWorldReset, srv, world, err)
	return err
}

// ImportWorld replaces a stopped server's world with the one in a zip archive already
// uploaded to the server directory. The archive holds the world either at its root or in one
// top-level directory (as "Download world" makes it); level.dat marks it. It is unpacked and
// checked first, then the server is backed up and the worlds are swapped; the archive is
// deleted once unpacked.
func (s *Service) ImportWorld(ctx context.Context, id, archive string, actor servers.Actor) error {
	archive, err := domain.CleanServerPath("path", archive)
	if err != nil {
		return err
	}
	if archive == "" || !strings.EqualFold(path.Ext(archive), ".zip") {
		return &domain.ValidationError{Field: "path", Message: "choose a .zip archive"}
	}
	srv, err := s.stopped(ctx, id)
	if err != nil {
		return err
	}
	props, err := s.properties(id)
	if err != nil {
		return err
	}
	world, err := domain.LevelName(props.Content)
	if err != nil {
		return err
	}

	staging := domain.WorldStagingPrefix + rand.Text()
	if err := s.files.Mkdir(id, staging); err != nil {
		return err
	}
	defer func() { _ = s.files.Remove(id, staging) }()
	err = func() error {
		if _, err := s.files.Extract(id, archive, staging, false); err != nil {
			return err
		}
		root, err := s.worldRoot(id, staging)
		if err != nil {
			return err
		}
		if err := s.files.Remove(id, archive); err != nil {
			return err
		}
		return s.backups.Protect(ctx, id, actor, func(ctx context.Context) error {
			if _, err := s.stopped(ctx, id); err != nil {
				return err
			}
			if err := s.files.Remove(id, world); err != nil && !errors.Is(err, domain.ErrNotFound) {
				return err
			}
			if dir := path.Dir(world); dir != "." {
				if err := s.mkdirAll(id, dir); err != nil {
					return err
				}
			}
			return s.files.Rename(id, root, world)
		})
	}()
	s.record(ctx, actor, domain.AuditWorldImport, srv, world, err)
	return err
}

// worldRoot finds the directory holding level.dat in an unpacked archive: its root, or its
// only top-level directory (a "__MACOSX" folder added by macOS is ignored).
func (s *Service) worldRoot(id, dir string) (string, error) {
	if s.isWorld(id, dir) {
		return dir, nil
	}
	l, err := s.files.List(id, dir)
	if err != nil {
		return "", err
	}
	var dirs []string
	for _, e := range l.Entries {
		if e.Name == "__MACOSX" {
			continue
		}
		if e.Type != domain.FileTypeDir {
			return "", domain.ErrInvalidWorld
		}
		dirs = append(dirs, path.Join(dir, e.Name))
	}
	if len(dirs) == 1 && s.isWorld(id, dirs[0]) {
		return dirs[0], nil
	}
	return "", domain.ErrInvalidWorld
}

func (s *Service) isWorld(id, dir string) bool {
	e, err := s.files.Stat(id, path.Join(dir, domain.LevelDatFile))
	return err == nil && e.Type == domain.FileTypeFile
}

func (s *Service) mkdirAll(id, dir string) error {
	cur := ""
	for _, seg := range strings.Split(dir, "/") {
		cur = path.Join(cur, seg)
		if err := s.files.Mkdir(id, cur); err != nil && !errors.Is(err, domain.ErrFileExists) {
			return err
		}
	}
	return nil
}

// stopped returns the server if it is stopped (crashed and missing containers count: the game
// is not running), or the error that refuses the change.
func (s *Service) stopped(ctx context.Context, id string) (servers.View, error) {
	v, err := s.servers.Get(ctx, id)
	if err != nil {
		return v, err
	}
	switch v.Status {
	case domain.StatusStopped, domain.StatusCrashed:
		return v, nil
	case domain.StatusRunning:
		return v, domain.ErrServerRunning
	case domain.StatusUnknown:
		return v, domain.ErrDockerUnavailable
	case domain.StatusMissing, domain.StatusCreateFailed:
		return v, domain.ErrContainerMissing
	}
	return v, domain.ErrServerBusy
}

// properties reads server.properties; a server that never started has none yet.
func (s *Service) properties(id string) (domain.FileText, error) {
	ft, err := s.files.ReadText(id, domain.PropertiesFile)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.FileText{}, nil
	}
	return ft, err
}

func (s *Service) record(ctx context.Context, actor servers.Actor, action string, srv servers.View, world string, err error) {
	var ve *domain.ValidationError
	if errors.As(err, &ve) {
		return // nothing was attempted
	}
	_ = s.audit.Record(context.WithoutCancel(ctx), domain.AuditEvent{
		CreatedAt: s.now().UTC(), UserID: actor.UserID, ServerID: srv.ID, Action: action,
		Target: srv.Name + ": " + world, IP: actor.IP, Success: err == nil,
	})
}
