package servers

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strconv"
	"strings"
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
	Status  domain.ServerStatus
	Failure string // why the creation failed (status create_failed)
}

// Actor identifies who performs an action, for the audit log.
type Actor struct {
	UserID string
	IP     string
}

type CreateInput struct {
	Name          string
	Description   string
	Loader        string
	Version       string // Minecraft version
	LoaderVersion string // "" picks the recommended build
	MemoryMB      int
	CPUMillicores int
	Port          int // 0 picks the first free port of the range
	AcceptEULA    bool
	Start         bool // start the server once it is created
}

// UpdateInput changes only the fields that are not nil.
type UpdateInput struct {
	Name          *string
	Description   *string
	MemoryMB      *int
	CPUMillicores *int
}

type Service struct {
	repo       ServerRepository
	runtime    ContainerRuntime
	dirs       DataDirs
	installers Installers
	audit      AuditRecorder
	cfg        Config
	now        func() time.Time

	// createLock serializes port picking and recording, so two requests cannot pick the same
	// port; provisionLock serializes background builds, so an image is never pulled twice.
	createLock    sync.Mutex
	provisionLock sync.Mutex

	// Creations in progress or failed, by server ID. A server leaves it once its container
	// exists; a failed one stays until it is deleted (or the panel restarts, see Recover).
	jobsMu sync.Mutex
	jobs   map[string]*createJob

	ctx    context.Context // cancelled by Close
	cancel context.CancelFunc
	wg     sync.WaitGroup // provision goroutines
}

type createJob struct {
	failure string // empty while in progress
}

func NewService(repo ServerRepository, runtime ContainerRuntime, dirs DataDirs, installers Installers,
	audit AuditRecorder, cfg Config) *Service {
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{repo: repo, runtime: runtime, dirs: dirs, installers: installers, audit: audit, cfg: cfg, now: time.Now,
		jobs: map[string]*createJob{}, ctx: ctx, cancel: cancel}
}

// Create validates the input, pins the loader build, assigns a port and records the server,
// then returns it with status creating. Pulling the image for the game's Java version and
// creating the directory and container happen in the background (provision); when start is
// set, the server is started once its container exists.
func (s *Service) Create(ctx context.Context, in CreateInput, actor Actor) (View, error) {
	if err := s.validateCreate(in); err != nil {
		return View{}, err
	}
	// Outside the lock: modded servers may wait on the version sources.
	loaderVersion, err := s.installers.Resolve(ctx, in.Loader, in.Version, in.LoaderVersion)
	if err != nil {
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
		Game: domain.GameMinecraft, Loader: in.Loader, Version: in.Version, LoaderVersion: loaderVersion,
		MemoryMB: in.MemoryMB, CPUMillicores: in.CPUMillicores, Port: port,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.repo.Create(ctx, x); err != nil {
		return View{}, err
	}
	s.jobsMu.Lock()
	s.jobs[x.ID] = &createJob{}
	s.jobsMu.Unlock()
	s.wg.Add(1)
	go s.provision(x, in.Start, actor)
	return View{Server: x, Status: domain.StatusCreating}, nil
}

// provision builds a recorded server and optionally starts it. On failure the record stays,
// shown as create_failed with the reason until it is deleted.
func (s *Service) provision(x domain.Server, start bool, actor Actor) {
	defer s.wg.Done()
	s.provisionLock.Lock() // one image pull at a time
	err := s.build(s.ctx, &x)
	s.provisionLock.Unlock()
	s.record(s.ctx, actor, domain.AuditServerCreate, x.Name, err == nil)

	s.jobsMu.Lock()
	if err != nil {
		s.jobs[x.ID].failure = failureMessage(err)
	} else {
		delete(s.jobs, x.ID)
	}
	s.jobsMu.Unlock()
	if err != nil || !start {
		return
	}
	// A failed start leaves the server stopped, which the list shows.
	err = s.runtime.Start(s.ctx, x.ContainerID, x.ID)
	s.record(s.ctx, actor, domain.AuditServerStart, x.Name, err == nil)
}

// build pulls the image if needed and creates the server's directory and (stopped)
// container. Any failure undoes the steps already done.
func (s *Service) build(ctx context.Context, x *domain.Server) error {
	// Undo must finish even if the panel is shutting down.
	cleanupCtx := context.WithoutCancel(ctx)
	java := domain.JavaFor(x.Version)
	if err := s.runtime.EnsureImage(ctx, java); err != nil {
		return err
	}
	dir, err := s.dirs.Create(x.ID)
	if err != nil {
		return err
	}
	cid, err := s.runtime.Create(ctx, domain.ContainerSpec{
		ServerID: x.ID, Game: x.Game, Java: java, Env: minecraftEnv(*x), DataDir: dir,
		Port: x.Port, MemoryMB: x.MemoryMB, CPUMillicores: x.CPUMillicores,
	})
	if err != nil {
		_ = s.dirs.Remove(x.ID)
		return err
	}
	created := *x
	created.ContainerID = cid
	if err := s.repo.Update(cleanupCtx, created); err != nil {
		_ = s.runtime.Remove(cleanupCtx, cid, x.ID)
		_ = s.dirs.Remove(x.ID)
		return err
	}
	*x = created
	return nil
}

// failureMessage is the reason shown for a failed creation.
func failureMessage(err error) string {
	switch {
	case errors.Is(err, domain.ErrDockerUnavailable):
		return "Docker is unreachable"
	case errors.Is(err, context.Canceled):
		return "interrupted by a panel shutdown"
	}
	msg := err.Error()
	if len(msg) > 300 {
		msg = strings.ToValidUTF8(msg[:300], "") + "…"
	}
	return msg
}

// pending returns the view of a server whose creation is in progress or failed.
func (s *Service) pending(x domain.Server) (View, bool) {
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	j, ok := s.jobs[x.ID]
	switch {
	case !ok:
		return View{}, false
	case j.failure == "":
		return View{Server: x, Status: domain.StatusCreating}, true
	}
	return View{Server: x, Status: domain.StatusCreateFailed, Failure: j.failure}, true
}

// checkBuilt refuses to act on a server that has no container yet.
func (s *Service) checkBuilt(x domain.Server) error {
	v, ok := s.pending(x)
	switch {
	case !ok:
		return nil
	case v.Status == domain.StatusCreating:
		return fmt.Errorf("%w: the server is being created", domain.ErrServerBusy)
	}
	return fmt.Errorf("%w: the server's creation failed; delete it", domain.ErrContainerMissing)
}

// Recover runs before serving. A server without a container is a creation that a previous
// run did not finish, or that failed: its record and directory are removed. A container
// created just before the panel stopped becomes an orphan (DeleteOrphanContainer).
func (s *Service) Recover(ctx context.Context) error {
	list, err := s.repo.List(ctx)
	if err != nil {
		return err
	}
	for _, x := range list {
		if x.ContainerID != "" {
			continue
		}
		if err := s.dirs.Remove(x.ID); err != nil {
			return err
		}
		if err := s.repo.Delete(ctx, x.ID); err != nil {
			return err
		}
	}
	return nil
}

// Close interrupts creations in progress and waits for them.
func (s *Service) Close() {
	s.cancel()
	s.wg.Wait()
}

func (s *Service) validateCreate(in CreateInput) error {
	for _, err := range []error{
		domain.ValidateServerName(in.Name),
		domain.ValidateServerDescription(in.Description),
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
	dockerDown := errors.Is(err, domain.ErrDockerUnavailable)
	if err != nil && !dockerDown {
		return nil, err
	}
	// A container counts only if both its ID and its server_id label match the database.
	byID := make(map[string]domain.ContainerState, len(states))
	for _, st := range states {
		byID[st.ID] = st
	}
	for i, x := range list {
		if v, ok := s.pending(x); ok {
			out[i] = v
			continue
		}
		out[i] = View{Server: x, Status: domain.StatusMissing}
		if dockerDown {
			out[i].Status = domain.StatusUnknown
		} else if st, ok := byID[x.ContainerID]; ok && st.ServerID == x.ID {
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
	if v, ok := s.pending(x); ok {
		return v, nil
	}
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
	if err := s.checkBuilt(x); err != nil {
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
	if err := s.checkBuilt(old); err != nil {
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
	// A failed creation has nothing left but its record; one in progress must finish first.
	if v, ok := s.pending(x); ok && v.Status == domain.StatusCreating {
		return fmt.Errorf("%w: the server is being created", domain.ErrServerBusy)
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
	if err == nil {
		s.jobsMu.Lock()
		delete(s.jobs, x.ID)
		s.jobsMu.Unlock()
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

// DeleteOrphanContainer force-removes a panel-labelled container whose server no longer
// exists in the database. It refuses containers that belong to a registered server (use
// Delete) and anything the runtime does not list as managed.
func (s *Service) DeleteOrphanContainer(ctx context.Context, containerID string, actor Actor) error {
	states, err := s.runtime.States(ctx)
	if err != nil {
		return err
	}
	var found *domain.ContainerState
	for i := range states {
		if states[i].ID == containerID {
			found = &states[i]
			break
		}
	}
	if found == nil {
		return domain.ErrNotFound
	}
	if _, err := s.repo.ByID(ctx, found.ServerID); err == nil {
		return fmt.Errorf("%w: container %s belongs to server %s", domain.ErrNotManaged, found.ID, found.ServerID)
	} else if !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	err = s.runtime.Stop(ctx, found.ID, found.ServerID)
	if err == nil {
		err = s.runtime.Remove(ctx, found.ID, found.ServerID)
	}
	if errors.Is(err, domain.ErrContainerMissing) {
		err = nil
	}
	s.record(ctx, actor, domain.AuditContainerDelete, found.ID, err == nil)
	return err
}

func (s *Service) record(ctx context.Context, actor Actor, action, target string, success bool) {
	_ = s.audit.Record(context.WithoutCancel(ctx), domain.AuditEvent{
		CreatedAt: s.now().UTC(), UserID: actor.UserID, Action: action, Target: target, IP: actor.IP, Success: success,
	})
}
