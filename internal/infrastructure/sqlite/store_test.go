package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/bruno-mesquita/mesquita-panel/migrations"
)

func TestMigrationsApplyFromScratchAndAreIdempotent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "db.sqlite")

	for range 2 { // second open must be a no-op
		s, err := Open(ctx, path, migrations.FS)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		var n int
		if err := s.read.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 3 {
			t.Fatalf("schema_migrations rows = %d, want 3", n)
		}
		for _, table := range []string{"users", "sessions", "settings", "audit_log", "servers", "backups", "backup_schedules"} {
			if err := s.read.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&n); err != nil {
				t.Fatalf("table %s: %v", table, err)
			}
		}
		s.Close()
	}
}

func TestMigrationFailureRollsBack(t *testing.T) {
	ctx := context.Background()
	bad := fstest.MapFS{
		"0001_ok.sql":     {Data: []byte(`CREATE TABLE a (x INTEGER);`)},
		"0002_broken.sql": {Data: []byte(`CREATE TABLE b (x INTEGER); THIS IS NOT SQL;`)},
	}
	path := filepath.Join(t.TempDir(), "db.sqlite")
	if _, err := Open(ctx, path, bad); err == nil {
		t.Fatal("expected migration error")
	}
	db, err := openPool(path, 1, true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&n)
	if n != 1 {
		t.Fatalf("applied migrations = %d, want 1 (only 0001)", n)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE name = 'b'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("table b should not exist after failed migration (n=%d, err=%v)", n, err)
	}
}

func TestMigrationNamesAreValidated(t *testing.T) {
	bad := fstest.MapFS{"init.sql": {Data: []byte(`SELECT 1;`)}}
	if _, err := Open(context.Background(), filepath.Join(t.TempDir(), "db.sqlite"), bad); err == nil {
		t.Fatal("expected error for badly named migration")
	}
}
