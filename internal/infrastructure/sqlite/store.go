// Package sqlite implements the persistence ports on SQLite (modernc.org/sqlite, pure Go).
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Store holds two pools on the same file: a single-connection writer (SQLite allows one
// writer at a time; serializing in Go avoids SQLITE_BUSY churn) and a reader pool that
// WAL lets run concurrently with the writer.
type Store struct {
	write *sql.DB
	read  *sql.DB
}

// Open opens (creating if needed) the database at path and applies pending migrations.
func Open(ctx context.Context, path string, migrations fs.FS) (*Store, error) {
	write, err := openPool(path, 1, true)
	if err != nil {
		return nil, err
	}
	if err := migrate(ctx, write, migrations); err != nil {
		write.Close()
		return nil, err
	}
	read, err := openPool(path, max(2, runtime.NumCPU()), false)
	if err != nil {
		write.Close()
		return nil, err
	}
	return &Store{write: write, read: read}, nil
}

func openPool(path string, conns int, writer bool) (*sql.DB, error) {
	q := url.Values{}
	for _, p := range []string{"busy_timeout(5000)", "journal_mode(WAL)", "foreign_keys(1)", "synchronous(NORMAL)"} {
		q.Add("_pragma", p)
	}
	if writer {
		q.Set("_txlock", "immediate")
	} else {
		q.Add("_pragma", "query_only(1)")
	}
	db, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, fmt.Errorf("sqlite: open: %w", err)
	}
	db.SetMaxOpenConns(conns)
	db.SetMaxIdleConns(conns)
	db.SetConnMaxIdleTime(5 * time.Minute)
	return db, nil
}

func (s *Store) Close() error {
	return errors.Join(s.read.Close(), s.write.Close())
}

// migrate applies every NNNN_*.sql file not yet recorded in schema_migrations, each in its
// own transaction. Re-running is a no-op.
func migrate(ctx context.Context, db *sql.DB, migrations fs.FS) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		name       TEXT    NOT NULL,
		applied_at INTEGER NOT NULL
	)`); err != nil {
		return fmt.Errorf("sqlite: create schema_migrations: %w", err)
	}

	names, err := fs.Glob(migrations, "*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)

	applied := map[int]bool{}
	rows, err := db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return err
		}
		applied[v] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	seen := map[int]string{}
	for _, name := range names {
		prefix, _, ok := strings.Cut(name, "_")
		version, err := strconv.Atoi(prefix)
		if !ok || err != nil || version <= 0 {
			return fmt.Errorf("sqlite: migration %q: name must be NNNN_description.sql", name)
		}
		if prev, dup := seen[version]; dup {
			return fmt.Errorf("sqlite: migrations %q and %q share version %d", prev, name, version)
		}
		seen[version] = name
		if applied[version] {
			continue
		}
		body, err := fs.ReadFile(migrations, name)
		if err != nil {
			return err
		}
		if err := applyMigration(ctx, db, version, name, string(body)); err != nil {
			return fmt.Errorf("sqlite: migration %s: %w", name, err)
		}
	}
	return nil
}

func applyMigration(ctx context.Context, db *sql.DB, version int, name, body string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, body); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)`,
		version, name, time.Now().Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

func unix(t time.Time) int64     { return t.Unix() }
func fromUnix(s int64) time.Time { return time.Unix(s, 0).UTC() }
