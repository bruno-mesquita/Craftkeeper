package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/bruno-mesquita/mesquita-panel/internal/domain"
)

type Sessions struct{ s *Store }

func NewSessions(s *Store) *Sessions { return &Sessions{s: s} }

func (r *Sessions) Create(ctx context.Context, x domain.Session) error {
	_, err := r.s.write.ExecContext(ctx, `INSERT INTO sessions
		(token_hash, user_id, csrf_token, created_at, last_seen_at, expires_at, ip, user_agent)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		x.TokenHash, x.UserID, x.CSRFToken, unix(x.CreatedAt), unix(x.LastSeenAt), unix(x.ExpiresAt), x.IP, x.UserAgent)
	return err
}

func (r *Sessions) ByTokenHash(ctx context.Context, tokenHash string) (domain.Session, error) {
	var x domain.Session
	var created, seen, expires int64
	err := r.s.read.QueryRowContext(ctx, `SELECT token_hash, user_id, csrf_token, created_at, last_seen_at, expires_at, ip, user_agent
		FROM sessions WHERE token_hash = ?`, tokenHash).
		Scan(&x.TokenHash, &x.UserID, &x.CSRFToken, &created, &seen, &expires, &x.IP, &x.UserAgent)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Session{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Session{}, err
	}
	x.CreatedAt, x.LastSeenAt, x.ExpiresAt = fromUnix(created), fromUnix(seen), fromUnix(expires)
	return x, nil
}

func (r *Sessions) Touch(ctx context.Context, tokenHash string, at time.Time) error {
	_, err := r.s.write.ExecContext(ctx, `UPDATE sessions SET last_seen_at = ? WHERE token_hash = ?`, unix(at), tokenHash)
	return err
}

func (r *Sessions) Delete(ctx context.Context, tokenHash string) error {
	_, err := r.s.write.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, tokenHash)
	return err
}

func (r *Sessions) DeleteOthersForUser(ctx context.Context, userID, keepTokenHash string) error {
	_, err := r.s.write.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ? AND token_hash <> ?`, userID, keepTokenHash)
	return err
}

func (r *Sessions) DeleteExpired(ctx context.Context, now time.Time) error {
	_, err := r.s.write.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ? OR last_seen_at <= ?`,
		unix(now), unix(now.Add(-domain.SessionIdleTTL)))
	return err
}
