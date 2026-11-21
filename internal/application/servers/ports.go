// Package servers implements the game server use cases: creation, lifecycle, limits and
// metadata. The container runtime is always reached through ownership-checked operations.
package servers

import (
	"context"
	"time"

	"github.com/bruno-mesquita/Craftkeeper/internal/domain"
)

type ServerRepository interface {
	Create(ctx context.Context, x domain.Server) error
	ByID(ctx context.Context, id string) (domain.Server, error)
	List(ctx context.Context) ([]domain.Server, error)
	Update(ctx context.Context, x domain.Server) error
	Delete(ctx context.Context, id string) error
	UsedPorts(ctx context.Context) (map[int]bool, error)
	MarkCrashed(ctx context.Context, id string, at time.Time, restarts int) error
	ClearCrash(ctx context.Context, id string) error
}

// EventSource blocks, calling emit for each managed container event, until ctx is cancelled
// or the stream ends (always an error).
type EventSource interface {
	WatchEvents(ctx context.Context, emit func(domain.ContainerEvent)) error
}

// ContainerRuntime runs server containers. Every method that touches an existing container
// takes the server ID and refuses containers that do not carry it (domain.ErrNotManaged).
type ContainerRuntime interface {
	EnsureImage(ctx context.Context, java string) error
	Create(ctx context.Context, spec domain.ContainerSpec) (string, error)
	Inspect(ctx context.Context, id, serverID string) (domain.ContainerState, error)
	States(ctx context.Context) ([]domain.ContainerState, error)
	Start(ctx context.Context, id, serverID string) error
	Stop(ctx context.Context, id, serverID string) error
	Restart(ctx context.Context, id, serverID string) error
	Kill(ctx context.Context, id, serverID string) error
	Remove(ctx context.Context, id, serverID string) error
	// RenameReplaced moves a container to its "-replaced" name, freeing the normal one for a
	// new container, or back (replaced false).
	RenameReplaced(ctx context.Context, id, serverID string, replaced bool) error
	UpdateLimits(ctx context.Context, id, serverID string, memoryMB, cpuMillicores int) error
}

// Installers pins the versions a new server installs.
type Installers interface {
	// Resolve validates loader and versions and returns the exact loader build to install
	// ("" for vanilla).
	Resolve(ctx context.Context, loader, mcVersion, loaderVersion string) (string, error)
}

// DataDirs owns the per-server data directories.
type DataDirs interface {
	Create(id string) (string, error)
	Path(id string) (string, error)
	Remove(id string) error
	Archive(id string) error
}

type AuditRecorder interface {
	Record(ctx context.Context, e domain.AuditEvent) error
}
