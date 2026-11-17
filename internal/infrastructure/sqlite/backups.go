package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/bruno-mesquita/mesquita-panel/internal/domain"
)

// Backups stores backup metadata and schedules. Rows are removed with their server
// (ON DELETE CASCADE); the archives themselves are files (filesystem.Backups).
type Backups struct{ s *Store }

func NewBackups(s *Store) *Backups { return &Backups{s: s} }

const backupColumns = `id, server_id, kind, status, size_bytes, failure, created_at, completed_at`

func (r *Backups) Create(ctx context.Context, b domain.Backup) error {
	_, err := r.s.write.ExecContext(ctx, `INSERT INTO backups (`+backupColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		b.ID, b.ServerID, string(b.Kind), string(b.Status), b.SizeBytes, b.Failure, unix(b.CreatedAt), nullUnix(b.CompletedAt))
	return err
}

// Finish stores the outcome of a backup: status, size, failure and completion time.
// domain.ErrNotFound means the backup (or its server) was deleted meanwhile.
func (r *Backups) Finish(ctx context.Context, b domain.Backup) error {
	res, err := r.s.write.ExecContext(ctx,
		`UPDATE backups SET status = ?, size_bytes = ?, failure = ?, completed_at = ? WHERE id = ?`,
		string(b.Status), b.SizeBytes, b.Failure, nullUnix(b.CompletedAt), b.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// ByID returns a backup of a server; a backup of another server is not found.
func (r *Backups) ByID(ctx context.Context, serverID, id string) (domain.Backup, error) {
	b, err := scanBackup(r.s.read.QueryRowContext(ctx,
		`SELECT `+backupColumns+` FROM backups WHERE id = ? AND server_id = ?`, id, serverID))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Backup{}, domain.ErrNotFound
	}
	return b, err
}

// List returns a server's backups, newest first.
func (r *Backups) List(ctx context.Context, serverID string) ([]domain.Backup, error) {
	return r.query(ctx, `SELECT `+backupColumns+` FROM backups WHERE server_id = ? ORDER BY created_at DESC, id DESC`, serverID)
}

func (r *Backups) Delete(ctx context.Context, id string) error {
	_, err := r.s.write.ExecContext(ctx, `DELETE FROM backups WHERE id = ?`, id)
	return err
}

// FailInterrupted marks every backup still "creating" as failed: run at startup, when no
// backup can be in progress.
func (r *Backups) FailInterrupted(ctx context.Context, at time.Time) error {
	_, err := r.s.write.ExecContext(ctx, `UPDATE backups SET status = 'failed', failure = ?, completed_at = ? WHERE status = 'creating'`,
		domain.BackupFailureInterrupted, unix(at))
	return err
}

// Prunable returns the scheduled backups retention removes after a successful scheduled
// backup: ready ones beyond the newest keep (none when keep is 0), and failed ones.
func (r *Backups) Prunable(ctx context.Context, serverID string, keep int) ([]domain.Backup, error) {
	failed, err := r.query(ctx, `SELECT `+backupColumns+` FROM backups
		WHERE server_id = ? AND kind = 'scheduled' AND status = 'failed'`, serverID)
	if err != nil || keep == 0 {
		return failed, err
	}
	old, err := r.query(ctx, `SELECT `+backupColumns+` FROM backups
		WHERE server_id = ? AND kind = 'scheduled' AND status = 'ready'
		ORDER BY created_at DESC, id DESC LIMIT -1 OFFSET ?`, serverID, keep)
	return append(failed, old...), err
}

func (r *Backups) query(ctx context.Context, q string, args ...any) ([]domain.Backup, error) {
	rows, err := r.s.read.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Backup{}
	for rows.Next() {
		b, err := scanBackup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func scanBackup(row interface{ Scan(...any) error }) (domain.Backup, error) {
	var b domain.Backup
	var kind, status string
	var created int64
	var completed sql.NullInt64
	if err := row.Scan(&b.ID, &b.ServerID, &kind, &status, &b.SizeBytes, &b.Failure, &created, &completed); err != nil {
		return domain.Backup{}, err
	}
	b.Kind, b.Status, b.CreatedAt = domain.BackupKind(kind), domain.BackupStatus(status), fromUnix(created)
	if completed.Valid {
		b.CompletedAt = fromUnix(completed.Int64)
	}
	return b, nil
}

// Schedule returns a server's schedule; a server without one has it off.
func (r *Backups) Schedule(ctx context.Context, serverID string) (domain.BackupSchedule, error) {
	var sc domain.BackupSchedule
	var next int64
	err := r.s.read.QueryRowContext(ctx,
		`SELECT server_id, interval_hours, retain, next_run_at FROM backup_schedules WHERE server_id = ?`, serverID).
		Scan(&sc.ServerID, &sc.IntervalHours, &sc.Retain, &next)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.BackupSchedule{ServerID: serverID}, nil
	}
	if err != nil {
		return domain.BackupSchedule{}, err
	}
	if next != 0 {
		sc.NextRunAt = fromUnix(next)
	}
	return sc, nil
}

func (r *Backups) PutSchedule(ctx context.Context, sc domain.BackupSchedule) error {
	_, err := r.s.write.ExecContext(ctx, `INSERT INTO backup_schedules (server_id, interval_hours, retain, next_run_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT (server_id) DO UPDATE SET interval_hours = excluded.interval_hours, retain = excluded.retain,
			next_run_at = excluded.next_run_at`,
		sc.ServerID, sc.IntervalHours, sc.Retain, zeroUnix(sc.NextRunAt))
	return err
}

// DueSchedules returns the active schedules whose next run is at or before now.
func (r *Backups) DueSchedules(ctx context.Context, now time.Time) ([]domain.BackupSchedule, error) {
	rows, err := r.s.read.QueryContext(ctx, `SELECT server_id, interval_hours, retain, next_run_at FROM backup_schedules
		WHERE interval_hours > 0 AND next_run_at <= ? ORDER BY next_run_at`, unix(now))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.BackupSchedule
	for rows.Next() {
		var sc domain.BackupSchedule
		var next int64
		if err := rows.Scan(&sc.ServerID, &sc.IntervalHours, &sc.Retain, &next); err != nil {
			return nil, err
		}
		sc.NextRunAt = fromUnix(next)
		out = append(out, sc)
	}
	return out, rows.Err()
}

func (r *Backups) SetNextRun(ctx context.Context, serverID string, at time.Time) error {
	_, err := r.s.write.ExecContext(ctx, `UPDATE backup_schedules SET next_run_at = ? WHERE server_id = ?`, unix(at), serverID)
	return err
}

func nullUnix(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return unix(t)
}

func zeroUnix(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return unix(t)
}
