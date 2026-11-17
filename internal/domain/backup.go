package domain

import "time"

// BackupKind tells who asked for a backup. Retention only ever removes scheduled backups.
type BackupKind string

const (
	BackupManual    BackupKind = "manual"
	BackupScheduled BackupKind = "scheduled"
)

// BackupStatus is the state of a backup archive.
type BackupStatus string

const (
	BackupCreating BackupStatus = "creating" // queued or being written
	BackupReady    BackupStatus = "ready"
	BackupFailed   BackupStatus = "failed" // no archive; Failure says why
)

// Reasons a backup failed (Backup.Failure). Returned by the API; treat them as a contract.
const (
	BackupFailureInterrupted = "interrupted"          // the panel stopped while it was being made
	BackupFailureSaveTimeout = "save_timeout"         // the running game did not confirm its save
	BackupFailureServerBusy  = "server_busy"          // the server was starting or restarting
	BackupFailureStorage     = "insufficient_storage" // not enough disk space
	BackupFailureDocker      = "docker_unavailable"
	BackupFailureError       = "error" // anything else; details are in the panel log
)

// Backup schedule limits (docs/API.md, "Backups").
const (
	BackupIntervalMaxHours = 720
	BackupRetainMax        = 100
)

// Backup is a zip archive of a server's whole data directory.
type Backup struct {
	ID          string
	ServerID    string
	Kind        BackupKind
	Status      BackupStatus
	SizeBytes   int64 // size of the archive, once ready
	Failure     string
	CreatedAt   time.Time
	CompletedAt time.Time // zero while creating
}

// BackupSchedule makes automatic backups every IntervalHours (0 = off), keeping the newest
// Retain scheduled ones (0 = all).
type BackupSchedule struct {
	ServerID      string
	IntervalHours int
	Retain        int
	NextRunAt     time.Time // zero when off
}

// ValidBackupID accepts the IDs the panel generates, the same format as server IDs.
func ValidBackupID(id string) bool { return ValidServerID(id) }

func ValidateBackupSchedule(intervalHours, retain int) error {
	if intervalHours < 0 || intervalHours > BackupIntervalMaxHours {
		return &ValidationError{Field: "interval_hours", Message: "must be between 0 (off) and 720"}
	}
	if retain < 0 || retain > BackupRetainMax {
		return &ValidationError{Field: "retain", Message: "must be between 0 (keep all) and 100"}
	}
	return nil
}
