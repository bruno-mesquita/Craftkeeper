package sqlite

import (
	"context"
	"database/sql"
	"errors"
)

const (
	settingCurseForgeKey = "curseforge_api_key"
	settingPublicHost    = "public_host"
)

// Settings stores panel-wide values in the settings table.
type Settings struct{ s *Store }

func NewSettings(s *Store) *Settings { return &Settings{s: s} }

// CurseForgeKey returns the stored CurseForge API key, "" when none.
func (r *Settings) CurseForgeKey(ctx context.Context) (string, error) {
	return r.get(ctx, settingCurseForgeKey)
}

// SetCurseForgeKey stores the CurseForge API key; "" removes it.
func (r *Settings) SetCurseForgeKey(ctx context.Context, key string) error {
	return r.set(ctx, settingCurseForgeKey, key)
}

// PublicHost returns the host players use to reach the game servers, "" when none.
func (r *Settings) PublicHost(ctx context.Context) (string, error) {
	return r.get(ctx, settingPublicHost)
}

// SetPublicHost stores the public host; "" removes it.
func (r *Settings) SetPublicHost(ctx context.Context, host string) error {
	return r.set(ctx, settingPublicHost, host)
}

func (r *Settings) get(ctx context.Context, key string) (string, error) {
	var v string
	err := r.s.read.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// set stores a value; "" deletes the key.
func (r *Settings) set(ctx context.Context, key, value string) error {
	if value == "" {
		_, err := r.s.write.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, key)
		return err
	}
	_, err := r.s.write.ExecContext(ctx,
		`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value`,
		key, value)
	return err
}
