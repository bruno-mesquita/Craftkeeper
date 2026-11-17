package domain

import "time"

// Audit actions. These strings are persisted and returned by the API; treat them as a contract.
const (
	AuditSetupCompleted  = "auth.setup"
	AuditLogin           = "auth.login"
	AuditLogout          = "auth.logout"
	AuditPasswordChanged = "auth.password_change"
)

type AuditEvent struct {
	ID        int64
	CreatedAt time.Time
	UserID    string // empty when the actor is unknown (e.g. failed login)
	Action    string
	Target    string
	IP        string
	Success   bool
}
