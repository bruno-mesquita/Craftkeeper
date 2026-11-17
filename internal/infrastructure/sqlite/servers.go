package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/bruno-mesquita/mesquita-panel/internal/domain"
)

type Servers struct{ s *Store }

func NewServers(s *Store) *Servers { return &Servers{s: s} }

const serverColumns = `id, name, description, game, loader, version, loader_version, memory_mb, cpu_millicores,
	port, container_id, created_at, updated_at`

// Create stores x. A duplicate name or port is reported as a ValidationError on that field.
func (r *Servers) Create(ctx context.Context, x domain.Server) error {
	_, err := r.s.write.ExecContext(ctx, `INSERT INTO servers (`+serverColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		x.ID, x.Name, x.Description, x.Game, x.Loader, x.Version, x.LoaderVersion, x.MemoryMB, x.CPUMillicores, x.Port,
		x.ContainerID, unix(x.CreatedAt), unix(x.UpdatedAt))
	return uniqueViolation(err)
}

func (r *Servers) ByID(ctx context.Context, id string) (domain.Server, error) {
	x, err := scanServer(r.s.read.QueryRowContext(ctx, `SELECT `+serverColumns+` FROM servers WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Server{}, domain.ErrNotFound
	}
	return x, err
}

// List returns every server ordered by name.
func (r *Servers) List(ctx context.Context) ([]domain.Server, error) {
	rows, err := r.s.read.QueryContext(ctx, `SELECT `+serverColumns+` FROM servers ORDER BY name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Server{}
	for rows.Next() {
		x, err := scanServer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// Update stores the editable fields of x: name, description, limits and container ID.
func (r *Servers) Update(ctx context.Context, x domain.Server) error {
	res, err := r.s.write.ExecContext(ctx,
		`UPDATE servers SET name = ?, description = ?, memory_mb = ?, cpu_millicores = ?, container_id = ?, updated_at = ?
		 WHERE id = ?`,
		x.Name, x.Description, x.MemoryMB, x.CPUMillicores, x.ContainerID, unix(x.UpdatedAt), x.ID)
	if err != nil {
		return uniqueViolation(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (r *Servers) Delete(ctx context.Context, id string) error {
	_, err := r.s.write.ExecContext(ctx, `DELETE FROM servers WHERE id = ?`, id)
	return err
}

// UsedPorts returns the game ports already assigned to servers.
func (r *Servers) UsedPorts(ctx context.Context) (map[int]bool, error) {
	rows, err := r.s.read.QueryContext(ctx, `SELECT port FROM servers`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	used := map[int]bool{}
	for rows.Next() {
		var p int
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		used[p] = true
	}
	return used, rows.Err()
}

func scanServer(row interface{ Scan(...any) error }) (domain.Server, error) {
	var x domain.Server
	var created, updated int64
	err := row.Scan(&x.ID, &x.Name, &x.Description, &x.Game, &x.Loader, &x.Version, &x.LoaderVersion, &x.MemoryMB, &x.CPUMillicores,
		&x.Port, &x.ContainerID, &created, &updated)
	if err != nil {
		return domain.Server{}, err
	}
	x.CreatedAt, x.UpdatedAt = fromUnix(created), fromUnix(updated)
	return x, nil
}

// uniqueViolation turns a UNIQUE failure on servers.name or servers.port into a field error.
func uniqueViolation(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "UNIQUE constraint failed: servers.name"):
		return &domain.ValidationError{Field: "name", Message: "is already used by another server"}
	case strings.Contains(msg, "UNIQUE constraint failed: servers.port"):
		return &domain.ValidationError{Field: "port", Message: "is already used by another server"}
	}
	return err
}
