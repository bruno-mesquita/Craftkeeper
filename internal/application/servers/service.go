package servers

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/bruno-mesquita/mesquita-panel/internal/domain"
)

type Config struct {
	FirstPort, LastPort int // inclusive range of host ports for game servers
	HostCPUs            int

	// PortFree reports whether a host port can be bound right now.
	PortFree func(port int) bool
}

// View is a server with its current status.
type View struct {
	domain.Server
	Status domain.ServerStatus
}

// Actor identifies who performs an action, for the audit log.
type Actor struct {
	UserID string
	IP     string
}

type CreateInput struct {
	Name          string
	Description   string
	Version       string
	MemoryMB      int
	CPUMillicores int
	Port          int // 0 picks the first free port of the range
	AcceptEULA    bool
}

// UpdateInput changes only the fields that are not nil.
type UpdateInput struct {
	Name          *string
	Description   *string
	MemoryMB      *int
	CPUMillicores *int
}

type Service struct {
	repo    ServerRepository
	runtime ContainerRuntime
	dirs    DataDirs
	audit   AuditRecorder
	cfg     Config
	now     func() time.Time

	// createLock serializes creations, so two requests cannot pick the same port or pull the
	// image twice.
	createLock sync.Mutex
}

func NewService(repo ServerRepository, runtime ContainerRuntime, dirs DataDirs, audit AuditRecorder, cfg Config) *Service {
	return &Service{repo: repo, runtime: runtime, dirs: dirs, audit: audit, cfg: cfg, now: time.Now}
}

// Create validates the input, assigns a port, pulls the image if needed and creates the
// server's directory and (stopped) container. Any failure undoes the steps already done.
func (s *Service) Create(ctx context.Context, in CreateInput, actor Actor) (View, error) {
	if err := s.validateCreate(in); err != nil {
		return View{}, err
	}

	s.createLock.Lock()
	defer s.createLock.Unlock()

	port, err := s.pickPort(ctx, in.Port)
	if err != nil {
		return View{}, err
	}
	now := s.now().UTC().Truncate(time.Second) // stored with second precision
	x := domain.Server{
		ID: rand.Text(), Name: in.Name, Description: in.Description,
		Game: domain.GameMinecraft, Loader: domain.LoaderVanilla, Version: in.Version,
		MemoryMB: in.MemoryMB, CPUMillicores: in.CPUMillicores, Port: port,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.repo.Create(ctx, x); err != nil {
		return View{}, err
	}

	// Undo must finish even if the client goes away mid-request.
	cleanupCtx := context.WithoutCancel(ctx)
	var undo []func()
	fail := func(err error) (View, error) {
		for i := len(undo) - 1; i >= 0; i-- {
			undo[i]()
		}
		s.record(ctx, actor, domain.AuditServerCreate, x.Name, false)
		return View{}, err
	}
	undo = append(undo, func() { _ = s.repo.Delete(cleanupCtx, x.ID) })

	if err := s.runtime.EnsureImage(ctx); err != nil {
		return fail(err)
	}
	dir, err := s.dirs.Create(x.ID)
	if err != nil {
		return fail(err)
	}
	undo = append(undo, func() { _ = s.dirs.Remove(x.ID) })

	cid, err := s.runtime.Create(ctx, domain.ContainerSpec{
		ServerID: x.ID, Game: x.Game, Env: minecraftEnv(x), DataDir: dir,
		Port: x.Port, MemoryMB: x.MemoryMB, CPUMillicores: x.CPUMillicores,
	})
	if err != nil {
		return fail(err)
	}
	undo = append(undo, func() { _ = s.runtime.Remove(cleanupCtx, cid, x.ID) })

	x.ContainerID = cid
	if err := s.repo.Update(cleanupCtx, x); err != nil {
		return fail(err)
	}
	s.record(ctx, actor, domain.AuditServerCreate, x.Name, true)
	return View{Server: x, Status: domain.StatusStopped}, nil
}

func (s *Service) validateCreate(in CreateInput) error {
	for _, err := range []error{
		domain.ValidateServerName(in.Name),
		domain.ValidateServerDescription(in.Description),
		domain.ValidateMinecraftVersion(in.Version),
		domain.ValidateServerMemory(in.MemoryMB),
		domain.ValidateServerCPU(in.CPUMillicores, s.cfg.HostCPUs),
	} {
		if err != nil {
			return err
		}
	}
	if !in.AcceptEULA {
		return &domain.ValidationError{Field: "accept_eula", Message: "the Minecraft EULA must be accepted"}
	}
	return nil
}

// pickPort validates a requested port, or picks the first free one of the range.
func (s *Service) pickPort(ctx context.Context, requested int) (int, error) {
	used, err := s.repo.UsedPorts(ctx)
	if err != nil {
		return 0, err
	}
	if requested != 0 {
		switch {
		case requested < s.cfg.FirstPort || requested > s.cfg.LastPort:
			return 0, &domain.ValidationError{Field: "port", Message: "must be between " +
				strconv.Itoa(s.cfg.FirstPort) + " and " + strconv.Itoa(s.cfg.LastPort)}
		case used[requested]:
			return 0, &domain.ValidationError{Field: "port", Message: "is already used by another server"}
		case !s.cfg.PortFree(requested):
			return 0, &domain.ValidationError{Field: "port", Message: "is in use by another program on the host"}
		}
		return requested, nil
	}
	for p := s.cfg.FirstPort; p <= s.cfg.LastPort; p++ {
		if !used[p] && s.cfg.PortFree(p) {
			return p, nil
		}
	}
	return 0, &domain.ValidationError{Field: "port", Message: "no free port left in the configured range"}
}

// List returns every server with its status. If Docker is unreachable the list is still
// returned, with status unknown.
func (s *Service) List(ctx context.Context) ([]View, error) {
	list, err := s.repo.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]View, len(list))
	states, err := s.runtime.States(ctx)
	if err != nil {
		if !errors.Is(err, domain.ErrDockerUnavailable) {
			return nil, err
		}
		for i, x := range list {
			out[i] = View{Server: x, Status: domain.StatusUnknown}
		}
		return out, nil
	}
	// A container counts only if both its ID and its server_id label match the database.
	byID := make(map[string]domain.ContainerState, len(states))
	for _, st := range states {
		byID[st.ID] = st
	}
	for i, x := range list {
		out[i] = View{Server: x, Status: domain.StatusMissing}
		if st, ok := byID[x.ContainerID]; ok && st.ServerID == x.ID {
			out[i].Status = st.Status()
		}
	}
	return out, nil
}

// Counts summarizes servers by status.
func (s *Service) Counts(ctx context.Context) (domain.ServerCounts, error) {
	list, err := s.List(ctx)
	if err != nil {
		return domain.ServerCounts{}, err
	}
	c := domain.ServerCounts{Total: len(list)}
	for _, v := range list {
		switch v.Status {
		case domain.StatusRunning, domain.StatusStarting, domain.StatusRestarting:
			c.Running++
		case domain.StatusStopped:
			c.Stopped++
		}
	}
	return c, nil
}

func (s *Service) Get(ctx context.Context, id string) (View, error) {
	x, err := s.repo.ByID(ctx, id)
	if err != nil {
		return View{}, err
	}
	return s.view(ctx, x)
}

func (s *Service) view(ctx context.Context, x domain.Server) (View, error) {
	st, err := s.runtime.Inspect(ctx, x.ContainerID, x.ID)
	switch {
	case err == nil:
		return View{Server: x, Status: st.Status()}, nil
	case errors.Is(err, domain.ErrContainerMissing), errors.Is(err, domain.ErrNotManaged):
		return View{Server: x, Status: domain.StatusMissing}, nil
	case errors.Is(err, domain.ErrDockerUnavailable):
		return View{Server: x, Status: domain.StatusUnknown}, nil
	}
	return View{}, err
}

func (s *Service) Start(ctx context.Context, id string, actor Actor) (View, error) {
	return s.act(ctx, id, actor, domain.AuditServerStart, s.runtime.Start)
}

func (s *Service) Stop(ctx context.Context, id string, actor Actor) (View, error) {
	return s.act(ctx, id, actor, domain.AuditServerStop, s.runtime.Stop)
}

func (s *Service) Restart(ctx context.Context, id string, actor Actor) (View, error) {
	return s.act(ctx, id, actor, domain.AuditServerRestart, s.runtime.Restart)
}

func (s *Service) Kill(ctx context.Context, id string, actor Actor) (View, error) {
	return s.act(ctx, id, actor, domain.AuditServerKill, s.runtime.Kill)
}

func (s *Service) act(ctx context.Context, id string, actor Actor, action string,
	op func(ctx context.Context, containerID, serverID string) error) (View, error) {
	x, err := s.repo.ByID(ctx, id)
	if err != nil {
		return View{}, err
	}
	err = op(ctx, x.ContainerID, x.ID)
	s.record(ctx, actor, action, x.Name, err == nil)
	if err != nil {
		return View{}, err
	}
	return s.view(ctx, x)
}

// Update changes metadata at any time; memory and CPU limits only while the server is stopped,
// so a lower limit can never kill a running game.
func (s *Service) Update(ctx context.Context, id string, in UpdateInput, actor Actor) (View, error) {
	old, err := s.repo.ByID(ctx, id)
	if err != nil {
		return View{}, err
	}
	x := old
	if in.Name != nil {
		x.Name = *in.Name
	}
	if in.Description != nil {
		x.Description = *in.Description
	}
	if in.MemoryMB != nil {
		x.MemoryMB = *in.MemoryMB
	}
	if in.CPUMillicores != nil {
		x.CPUMillicores = *in.CPUMillicores
	}
	for _, err := range []error{
		domain.ValidateServerName(x.Name),
		domain.ValidateServerDescription(x.Description),
		domain.ValidateServerMemory(x.MemoryMB),
		domain.ValidateServerCPU(x.CPUMillicores, s.cfg.HostCPUs),
	} {
		if err != nil {
			return View{}, err
		}
	}

	limitsChanged := x.MemoryMB != old.MemoryMB || x.CPUMillicores != old.CPUMillicores
	if limitsChanged {
		st, err := s.runtime.Inspect(ctx, x.ContainerID, x.ID)
		if err != nil {
			return View{}, err
		}
		if st.Status() != domain.StatusStopped {
			return View{}, domain.ErrServerRunning
		}
	}

	// Database first (it can reject a duplicate name), then Docker; revert the row if Docker fails.
	x.UpdatedAt = s.now().UTC().Truncate(time.Second)
	if err := s.repo.Update(ctx, x); err != nil {
		return View{}, err
	}
	if limitsChanged {
		if err := s.runtime.UpdateLimits(ctx, x.ContainerID, x.ID, x.MemoryMB, x.CPUMillicores); err != nil {
			_ = s.repo.Update(context.WithoutCancel(ctx), old)
			s.record(ctx, actor, domain.AuditServerUpdate, old.Name, false)
			return View{}, err
		}
	}
	s.record(ctx, actor, domain.AuditServerUpdate, x.Name, true)
	return s.view(ctx, x)
}

// Delete stops the server gracefully, removes its container, then deletes or archives its
// files and finally its record. A container that is gone, or that no longer carries this
// server's labels, is left alone and only forgotten.
func (s *Service) Delete(ctx context.Context, id string, deleteFiles bool, actor Actor) error {
	x, err := s.repo.ByID(ctx, id)
	if err != nil {
		return err
	}
	err = s.deleteContainer(ctx, x)
	if err == nil {
		if deleteFiles {
			err = s.dirs.Remove(x.ID)
		} else {
			err = s.dirs.Archive(x.ID)
		}
	}
	if err == nil {
		err = s.repo.Delete(ctx, x.ID)
	}
	s.record(ctx, actor, domain.AuditServerDelete, x.Name, err == nil)
	return err
}

func (s *Service) deleteContainer(ctx context.Context, x domain.Server) error {
	if x.ContainerID == "" {
		return nil
	}
	err := s.runtime.Stop(ctx, x.ContainerID, x.ID)
	if err == nil {
		err = s.runtime.Remove(ctx, x.ContainerID, x.ID)
	}
	if errors.Is(err, domain.ErrContainerMissing) || errors.Is(err, domain.ErrNotManaged) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete container: %w", err)
	}
	return nil
}

func (s *Service) record(ctx context.Context, actor Actor, action, target string, success bool) {
	_ = s.audit.Record(context.WithoutCancel(ctx), domain.AuditEvent{
		CreatedAt: s.now().UTC(), UserID: actor.UserID, Action: action, Target: target, IP: actor.IP, Success: success,
	})
}
