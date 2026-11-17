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
)

// ValidationError describes which input field is invalid. It wraps ErrInvalidInput.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string { return e.Field + ": " + e.Message }
func (e *ValidationError) Unwrap() error { return ErrInvalidInput }
