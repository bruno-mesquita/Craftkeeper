package sqlite

import (
	"context"
	"database/sql"
	"log/slog"

	"github.com/bruno-mesquita/mesquita-panel/internal/domain"
)

type AuditLog struct {
	s   *Store
	log *slog.Logger
}

func NewAuditLog(s *Store, log *slog.Logger) *AuditLog { return &AuditLog{s: s, log: log} }

// Record stores e. Failures are logged here because callers deliberately do not fail
// the user-facing operation on an audit write error.
func (a *AuditLog) Record(ctx context.Context, e domain.AuditEvent) error {
	var userID sql.NullString
	if e.UserID != "" {
		userID = sql.NullString{String: e.UserID, Valid: true}
	}
	_, err := a.s.write.ExecContext(ctx,
		`INSERT INTO audit_log (created_at, user_id, action, target, ip, success) VALUES (?, ?, ?, ?, ?, ?)`,
		unix(e.CreatedAt), userID, e.Action, e.Target, e.IP, e.Success)
	if err != nil {
		a.log.Error("audit write failed", "action", e.Action, "err", err)
	}
	return err
}

func (a *AuditLog) Recent(ctx context.Context, limit int) ([]domain.AuditEvent, error) {
	rows, err := a.s.read.QueryContext(ctx,
		`SELECT id, created_at, COALESCE(user_id, ''), action, target, ip, success
		 FROM audit_log ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := make([]domain.AuditEvent, 0, limit)
	for rows.Next() {
		var e domain.AuditEvent
		var created int64
		if err := rows.Scan(&e.ID, &created, &e.UserID, &e.Action, &e.Target, &e.IP, &e.Success); err != nil {
			return nil, err
		}
		e.CreatedAt = fromUnix(created)
		events = append(events, e)
	}
	return events, rows.Err()
}
