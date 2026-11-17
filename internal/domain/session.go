package domain

import "time"

const (
	// SessionAbsoluteTTL is the maximum lifetime of a session.
	SessionAbsoluteTTL = 7 * 24 * time.Hour
	// SessionIdleTTL ends a session that has not been used for this long.
	SessionIdleTTL = 24 * time.Hour
)

// Session is an authenticated browser session. Only the hash of its token is stored.
type Session struct {
	TokenHash  string
	UserID     string
	CSRFToken  string
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
	IP         string
	UserAgent  string
}

// ActiveAt reports whether the session is usable at instant now.
func (s Session) ActiveAt(now time.Time) bool {
	return now.Before(s.ExpiresAt) && now.Sub(s.LastSeenAt) < SessionIdleTTL
}
