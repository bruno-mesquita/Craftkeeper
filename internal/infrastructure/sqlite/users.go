package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/bruno-mesquita/mesquita-panel/internal/domain"
)

const settingSetupCompleted = "setup_completed"

type Users struct{ s *Store }

func NewUsers(s *Store) *Users { return &Users{s: s} }

func (r *Users) SetupCompleted(ctx context.Context) (bool, error) {
	var v string
	err := r.s.read.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, settingSetupCompleted).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return v == "true", err
}

func (r *Users) CreateInitialAdmin(ctx context.Context, u domain.User) error {
	tx, err := r.s.write.BeginTx(ctx, nil) // BEGIN IMMEDIATE: serializes concurrent setups
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var v string
	err = tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, settingSetupCompleted).Scan(&v)
	switch {
	case err == nil && v == "true":
		return domain.ErrSetupCompleted
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return err
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO users (id, username, password_hash, role, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		u.ID, u.Username, u.PasswordHash, string(u.Role), unix(u.CreatedAt), unix(u.UpdatedAt)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO settings (key, value) VALUES (?, 'true') ON CONFLICT (key) DO UPDATE SET value = excluded.value`,
		settingSetupCompleted); err != nil {
		return err
	}
	return tx.Commit()
}

const userColumns = `id, username, password_hash, role, created_at, updated_at`

func (r *Users) ByUsername(ctx context.Context, username string) (domain.User, error) {
	return scanUser(r.s.read.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE username = ?`, username))
}

func (r *Users) ByID(ctx context.Context, id string) (domain.User, error) {
	return scanUser(r.s.read.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id = ?`, id))
}

func (r *Users) UpdatePassword(ctx context.Context, userID, passwordHash string, at time.Time) error {
	res, err := r.s.write.ExecContext(ctx, `UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?`,
		passwordHash, unix(at), userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func scanUser(row *sql.Row) (domain.User, error) {
	var u domain.User
	var role string
	var created, updated int64
	err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &role, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.User{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.User{}, err
	}
	u.Role, u.CreatedAt, u.UpdatedAt = domain.Role(role), fromUnix(created), fromUnix(updated)
	return u, nil
}
