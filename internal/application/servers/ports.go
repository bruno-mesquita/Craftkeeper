// Package servers implements the game server use cases: creation, lifecycle, limits and
// metadata. The container runtime is always reached through ownership-checked operations.
package servers

import (
	"context"

	"github.com/bruno-mesquita/mesquita-panel/internal/domain"
)

type ServerRepository interface {
	Create(ctx context.Context, x domain.Server) error
	ByID(ctx context.Context, id string) (domain.Server, error)
	List(ctx context.Context) ([]domain.Server, error)
	Update(ctx context.Context, x domain.Server) error
	Delete(ctx context.Context, id string) error
	UsedPorts(ctx context.Context) (map[int]bool, error)
}

// ContainerRuntime runs server containers. Every method that touches an existing container
// takes the server ID and refuses containers that do not carry it (domain.ErrNotManaged).
type ContainerRuntime interface {
	EnsureImage(ctx context.Context) error
	Create(ctx context.Context, spec domain.ContainerSpec) (string, error)
	Inspect(ctx context.Context, id, serverID string) (domain.ContainerState, error)
	States(ctx context.Context) ([]domain.ContainerState, error)
	Start(ctx context.Context, id, serverID string) error
	Stop(ctx context.Context, id, serverID string) error
	Restart(ctx context.Context, id, serverID string) error
	Kill(ctx context.Context, id, serverID string) error
	Remove(ctx context.Context, id, serverID string) error
	UpdateLimits(ctx context.Context, id, serverID string, memoryMB, cpuMillicores int) error
}

// DataDirs owns the per-server data directories.
type DataDirs interface {
	Create(id string) (string, error)
	Remove(id string) error
	Archive(id string) error
}

type AuditRecorder interface {
	Record(ctx context.Context, e domain.AuditEvent) error
}
