// Package auth implements setup, login, session and password use cases.
package auth

import (
	"context"
	"time"

	"github.com/bruno-mesquita/Craftkeeper/internal/domain"
)

// UserRepository persists users and the setup-completed marker.
type UserRepository interface {
	// CreateInitialAdmin atomically stores the first administrator and marks setup completed.
	// It returns domain.ErrSetupCompleted if setup was already completed.
	CreateInitialAdmin(ctx context.Context, u domain.User) error
	SetupCompleted(ctx context.Context) (bool, error)
	ByUsername(ctx context.Context, username string) (domain.User, error)
	ByID(ctx context.Context, id string) (domain.User, error)
	UpdatePassword(ctx context.Context, userID, passwordHash string, at time.Time) error
}

type SessionRepository interface {
	Create(ctx context.Context, s domain.Session) error
	ByTokenHash(ctx context.Context, tokenHash string) (domain.Session, error)
	Touch(ctx context.Context, tokenHash string, at time.Time) error
	Delete(ctx context.Context, tokenHash string) error
	DeleteOthersForUser(ctx context.Context, userID, keepTokenHash string) error
	DeleteExpired(ctx context.Context, now time.Time) error
}

type PasswordHasher interface {
	Hash(password string) (string, error)
	Verify(password, encoded string) (bool, error)
}

type AuditRecorder interface {
	Record(ctx context.Context, e domain.AuditEvent) error
}
