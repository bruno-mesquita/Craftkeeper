package servers

import (
	"context"
	"fmt"
	"time"

	"github.com/bruno-mesquita/mesquita-panel/internal/domain"
)

// VersionInput is the game a server should run after ChangeVersion.
type VersionInput struct {
	Loader        string
	Version       string // Minecraft version
	LoaderVersion string // "" picks the recommended build
}

// ResolveVersion validates a version change without applying it: the loader build that
// ChangeVersion would install ("" for vanilla).
func (s *Service) ResolveVersion(ctx context.Context, in VersionInput) (string, error) {
	return s.installers.Resolve(ctx, in.Loader, in.Version, in.LoaderVersion)
}

// ChangeVersion makes a stopped server run another Minecraft version and/or loader. The game
// version lives in the container's environment, so the container is replaced by a new one with
// the same port, limits and directory. The old container is only renamed until the new one is
// recorded, and every failure puts it back. Downgrades and loader changes are allowed: the
// caller backs the server up first.
func (s *Service) ChangeVersion(ctx context.Context, id string, in VersionInput, actor Actor) (View, error) {
	old, err := s.repo.ByID(ctx, id)
	if err != nil {
		return View{}, err
	}
	if err := s.checkBuilt(old); err != nil {
		return View{}, err
	}
	if err := s.beginReplace(old.ID); err != nil {
		return View{}, err
	}
	defer s.endReplace(old.ID)

	x, err := s.replaceContainer(ctx, old, in)
	target := old.Name + ": " + versionLabel(old) + " → " + versionLabel(domain.Server{
		Loader: in.Loader, Version: in.Version, LoaderVersion: x.LoaderVersion})
	s.record(ctx, actor, domain.AuditServerVersionChange, old.ID, target, err == nil)
	if err != nil {
		return View{}, err
	}
	return s.view(ctx, x)
}

func (s *Service) beginReplace(id string) error {
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	if s.replacing[id] {
		return fmt.Errorf("%w: the server's version is being changed", domain.ErrServerBusy)
	}
	s.replacing[id] = true
	return nil
}

func (s *Service) endReplace(id string) {
	s.jobsMu.Lock()
	delete(s.replacing, id)
	s.jobsMu.Unlock()
}

// replaceContainer swaps old's container for one running the new version and records it.
func (s *Service) replaceContainer(ctx context.Context, old domain.Server, in VersionInput) (domain.Server, error) {
	if err := s.requireStopped(ctx, old); err != nil {
		return domain.Server{}, err
	}
	loaderVersion, err := s.installers.Resolve(ctx, in.Loader, in.Version, in.LoaderVersion)
	if err != nil {
		return domain.Server{}, err
	}
	x := old
	x.Loader, x.Version, x.LoaderVersion = in.Loader, in.Version, loaderVersion
	java := domain.JavaFor(x.Version)
	if err := s.runtime.EnsureImage(ctx, java); err != nil {
		return domain.Server{}, err
	}
	dir, err := s.dirs.Path(x.ID)
	if err != nil {
		return domain.Server{}, err
	}

	// Undo must finish even if the request is cancelled.
	undo := context.WithoutCancel(ctx)
	if err := s.runtime.RenameReplaced(ctx, old.ContainerID, old.ID, true); err != nil {
		return domain.Server{}, err
	}
	putBack := func() { _ = s.runtime.RenameReplaced(undo, old.ContainerID, old.ID, false) }
	cid, err := s.runtime.Create(ctx, domain.ContainerSpec{
		ServerID: x.ID, Game: x.Game, Java: java, Env: minecraftEnv(x), DataDir: dir,
		Port: x.Port, MemoryMB: x.MemoryMB, CPUMillicores: x.CPUMillicores,
	})
	if err != nil {
		putBack()
		return domain.Server{}, err
	}
	discard := func() {
		_ = s.runtime.Remove(undo, cid, x.ID)
		putBack()
	}
	x.ContainerID = cid
	x.UpdatedAt = s.now().UTC().Truncate(time.Second)
	if err := s.repo.Update(ctx, x); err != nil {
		discard()
		return domain.Server{}, err
	}
	// A start that slipped in before the server was marked busy must not be lost: only a
	// still stopped old container is removed.
	err = s.requireStopped(ctx, old)
	if err == nil {
		err = s.runtime.Remove(ctx, old.ContainerID, old.ID)
	}
	if err != nil {
		_ = s.repo.Update(undo, old)
		discard()
		return domain.Server{}, err
	}
	s.forgetRestarts(x.ID)
	if err := s.repo.ClearCrash(undo, x.ID); err != nil {
		return domain.Server{}, fmt.Errorf("clear crash record: %w", err)
	}
	x.CrashedAt, x.CrashRestarts = time.Time{}, 0
	return x, nil
}

// requireStopped refuses a server whose container is not stopped (crashed counts as stopped).
func (s *Service) requireStopped(ctx context.Context, x domain.Server) error {
	st, err := s.runtime.Inspect(ctx, x.ContainerID, x.ID)
	if err != nil {
		return err
	}
	if st.Status() != domain.StatusStopped {
		return domain.ErrServerRunning
	}
	return nil
}

// versionLabel names a server's game for the audit log, e.g. "fabric 1.21.1 0.16.10".
func versionLabel(x domain.Server) string {
	l := x.Loader + " " + x.Version
	if x.LoaderVersion != "" {
		l += " " + x.LoaderVersion
	}
	return l
}
