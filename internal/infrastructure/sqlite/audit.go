package sqlite

import (
	"context"
	"database/sql"
	"log/slog"
	"strings"

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
	_, err := a.s.write.ExecContext(ctx,
		`INSERT INTO audit_log (created_at, user_id, server_id, action, target, ip, success) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		unix(e.CreatedAt), nullable(e.UserID), nullable(e.ServerID), e.Action, e.Target, e.IP, e.Success)
	if err != nil {
		a.log.Error("audit write failed", "action", e.Action, "err", err)
	}
	return err
}

// List returns the events matching f, newest first, with the user's name and the server's
// current name. f must already be validated (Limit > 0).
func (a *AuditLog) List(ctx context.Context, f domain.AuditFilter) ([]domain.AuditEvent, error) {
	var where []string
	var args []any
	if f.ServerID != "" {
		where, args = append(where, "a.server_id = ?"), append(args, f.ServerID)
	}
	if f.Action != "" {
		where, args = append(where, "a.action = ?"), append(args, f.Action)
	}
	if f.FailedOnly {
		where = append(where, "a.success = 0")
	}
	if f.BeforeID > 0 {
		where, args = append(where, "a.id < ?"), append(args, f.BeforeID)
	}
	query := `SELECT a.id, a.created_at, COALESCE(a.user_id, ''), COALESCE(a.server_id, ''), a.action,
		a.target, a.ip, a.success, COALESCE(u.username, ''), COALESCE(s.name, '')
		FROM audit_log a
		LEFT JOIN users u ON u.id = a.user_id
		LEFT JOIN servers s ON s.id = a.server_id`
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY a.id DESC LIMIT ?"
	args = append(args, f.Limit)

	rows, err := a.s.read.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := make([]domain.AuditEvent, 0, f.Limit)
	for rows.Next() {
		var e domain.AuditEvent
		var created int64
		if err := rows.Scan(&e.ID, &created, &e.UserID, &e.ServerID, &e.Action, &e.Target, &e.IP, &e.Success,
			&e.Username, &e.ServerName); err != nil {
			return nil, err
		}
		e.CreatedAt = fromUnix(created)
		events = append(events, e)
	}
	return events, rows.Err()
}

func nullable(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}
