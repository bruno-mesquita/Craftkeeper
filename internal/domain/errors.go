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
)

// ValidationError describes which input field is invalid. It wraps ErrInvalidInput.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string { return e.Field + ": " + e.Message }
func (e *ValidationError) Unwrap() error { return ErrInvalidInput }
