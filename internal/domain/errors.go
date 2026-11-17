// Package domain holds entities, value rules and errors. It imports nothing from infrastructure.
package domain

import "errors"

var (
	ErrNotFound           = errors.New("not found")
	ErrInvalidInput       = errors.New("invalid input")
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrUnauthenticated    = errors.New("unauthenticated")
	ErrRateLimited        = errors.New("rate limited")
	ErrSetupCompleted     = errors.New("setup already completed")
	ErrSetupTokenInvalid  = errors.New("invalid setup token")
	ErrDockerUnavailable  = errors.New("docker unavailable")
	ErrDockerFailed       = errors.New("docker rejected the operation")
	ErrServerRunning      = errors.New("server is running")
	ErrServerNotRunning   = errors.New("server is not running")
	ErrContainerMissing   = errors.New("server container is missing")
	ErrPortInUse          = errors.New("port already in use")
	ErrNotManaged         = errors.New("container is not managed by this server")

	ErrInstallerUnavailable = errors.New("version source unavailable")

	ErrFileExists          = errors.New("file already exists")
	ErrNotDirectory        = errors.New("not a directory")
	ErrIsDirectory         = errors.New("is a directory")
	ErrFileChanged         = errors.New("file changed since it was read")
	ErrFileTooLarge        = errors.New("file too large")
	ErrNotText             = errors.New("not a text file")
	ErrInvalidArchive      = errors.New("invalid archive")
	ErrArchiveTooLarge     = errors.New("archive too large")
	ErrInsufficientStorage = errors.New("insufficient storage")

	ErrBackupInProgress = errors.New("a backup or restore of this server is in progress")
	ErrBackupNotReady   = errors.New("backup is not ready")
	ErrServerBusy       = errors.New("server is starting or restarting")
)

// ValidationError describes which input field is invalid. It wraps ErrInvalidInput.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string { return e.Field + ": " + e.Message }
func (e *ValidationError) Unwrap() error { return ErrInvalidInput }
