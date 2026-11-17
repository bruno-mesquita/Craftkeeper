package domain

import "time"

// Audit actions. These strings are persisted and returned by the API; treat them as a contract.
const (
	AuditSetupCompleted  = "auth.setup"
	AuditLogin           = "auth.login"
	AuditLogout          = "auth.logout"
	AuditPasswordChanged = "auth.password_change"

	AuditServerCreate  = "server.create"
	AuditServerUpdate  = "server.update"
	AuditServerStart   = "server.start"
	AuditServerStop    = "server.stop"
	AuditServerRestart = "server.restart"
	AuditServerKill    = "server.kill"
	AuditServerDelete  = "server.delete"
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
