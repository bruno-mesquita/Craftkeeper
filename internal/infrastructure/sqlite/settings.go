package sqlite

import (
	"context"
	"database/sql"
	"errors"
)

const settingCurseForgeKey = "curseforge_api_key"

// Settings stores panel-wide values in the settings table.
type Settings struct{ s *Store }

func NewSettings(s *Store) *Settings { return &Settings{s: s} }

// CurseForgeKey returns the stored CurseForge API key, "" when none.
func (r *Settings) CurseForgeKey(ctx context.Context) (string, error) {
	var v string
	err := r.s.read.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, settingCurseForgeKey).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// SetCurseForgeKey stores the CurseForge API key; "" removes it.
func (r *Settings) SetCurseForgeKey(ctx context.Context, key string) error {
	if key == "" {
		_, err := r.s.write.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, settingCurseForgeKey)
		return err
	}
	_, err := r.s.write.ExecContext(ctx,
		`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value`,
		settingCurseForgeKey, key)
	return err
}
