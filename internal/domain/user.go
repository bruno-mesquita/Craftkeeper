package domain

import (
	"time"
	"unicode/utf8"
)

// Role is a user's authorization role. Phase 1 only has administrators.
type Role string

const RoleAdmin Role = "admin"

type User struct {
	ID           string
	Username     string
	PasswordHash string
	Role         Role
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

const (
	usernameMinLen = 3
	usernameMaxLen = 32
	// PasswordMinLen and PasswordMaxLen bound password length in characters/bytes.
	// The upper bound caps hashing cost for hostile input.
	PasswordMinLen = 12
	PasswordMaxLen = 256
)

// ValidateUsername accepts 3–32 characters of [A-Za-z0-9._-].
func ValidateUsername(username string) error {
	if len(username) < usernameMinLen || len(username) > usernameMaxLen {
		return &ValidationError{Field: "username", Message: "must be 3 to 32 characters"}
	}
	for _, r := range username {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-'
		if !ok {
			return &ValidationError{Field: "username", Message: "may only contain letters, digits, '.', '_' and '-'"}
		}
	}
	return nil
}

// ValidatePassword enforces length limits only (NIST SP 800-63B: no composition rules).
func ValidatePassword(field, password string) error {
	if utf8.RuneCountInString(password) < PasswordMinLen {
		return &ValidationError{Field: field, Message: "must be at least 12 characters"}
	}
	if len(password) > PasswordMaxLen {
		return &ValidationError{Field: field, Message: "must be at most 256 bytes"}
	}
	return nil
}
