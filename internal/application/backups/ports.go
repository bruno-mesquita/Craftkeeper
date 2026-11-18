// Package backups implements server backups: creation (also while the game runs, with its
// saving paused), listing, download, restore, deletion, and automatic scheduled backups
// with retention.
package backups

import (
	"context"
	"os"
	"time"

	"github.com/bruno-mesquita/mesquita-panel/internal/application/console"
	"github.com/bruno-mesquita/mesquita-panel/internal/application/servers"
	"github.com/bruno-mesquita/mesquita-panel/internal/domain"
)

// Repository stores backup metadata and schedules (sqlite.Backups).
type Repository interface {
	Create(ctx context.Context, b domain.Backup) error
	Finish(ctx context.Context, b domain.Backup) error
	ByID(ctx context.Context, serverID, id string) (domain.Backup, error)
	List(ctx context.Context, serverID string) ([]domain.Backup, error)
	Delete(ctx context.Context, id string) error
	FailInterrupted(ctx context.Context, at time.Time) error
	Prunable(ctx context.Context, serverID string, keep int) ([]domain.Backup, error)
	Schedule(ctx context.Context, serverID string) (domain.BackupSchedule, error)
	PutSchedule(ctx context.Context, sc domain.BackupSchedule) error
	DueSchedules(ctx context.Context, now time.Time) ([]domain.BackupSchedule, error)
	SetNextRun(ctx context.Context, serverID string, at time.Time) error
}

// Store holds the archives and restores them (filesystem.Backups).
type Store interface {
	Write(ctx context.Context, serverID, backupID string) (int64, error)
	Open(serverID, backupID string) (*os.File, int64, error)
	Remove(serverID, backupID string) error
	Usage(serverID string) (dirBytes, freeBytes int64, err error)
	StageRestore(ctx context.Context, serverID, backupID string, maxBytes int64) (string, error)
	CommitRestore(serverID, staging string) error
	DiscardRestore(serverID, staging string) error
	CleanupStale() error
}

// Servers resolves a server and its current status (servers.Service).
type Servers interface {
	Get(ctx context.Context, id string) (servers.View, error)
}

// Console reaches a running game (console.Service): commands in, output lines out.
type Console interface {
	Subscribe(ctx context.Context, serverID string, sink console.Sink) (unsubscribe func(), err error)
	Send(ctx context.Context, serverID, command string) error
}

type AuditRecorder interface {
	Record(ctx context.Context, e domain.AuditEvent) error
}
