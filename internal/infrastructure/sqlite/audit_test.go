package sqlite

import (
	"context"
	"io"
	"io/fs"
	"log/slog"
	"path/filepath"
	"slices"
	"testing"
	"testing/fstest"
	"time"

	"github.com/bruno-mesquita/Craftkeeper/internal/domain"
	"github.com/bruno-mesquita/Craftkeeper/migrations"
)

const (
	auditServerA = "AAAAAAAAAAAAAAAAAAAAAAAAAA"
	auditServerB = "BBBBBBBBBBBBBBBBBBBBBBBBBB" // never registered: a deleted server
)

func TestAuditLogList(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "db.sqlite"), migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.write.ExecContext(ctx, `INSERT INTO users (id, username, password_hash, role, created_at, updated_at)
		VALUES ('U1', 'bruno', 'x', 'admin', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.write.ExecContext(ctx, `INSERT INTO servers (id, name, game, loader, version, memory_mb, cpu_millicores,
		port, created_at, updated_at) VALUES (?, 'Survival', 'minecraft', 'vanilla', 'LATEST', 2048, 1000, 25601, 1, 1)`, auditServerA); err != nil {
		t.Fatal(err)
	}

	a := NewAuditLog(s, slog.New(slog.NewTextHandler(io.Discard, nil)))
	at := time.Unix(1_800_000_000, 0).UTC()
	for _, e := range []domain.AuditEvent{ // ids 1 to 6
		{Action: domain.AuditLogin, Target: "bruno", UserID: "U1", Success: true},
		{Action: domain.AuditLogin, Target: "eve", Success: false},
		{Action: domain.AuditConsoleCommand, Target: "list", UserID: "U1", ServerID: auditServerA, Success: true},
		{Action: domain.AuditServerStart, Target: "Survival", UserID: "U1", ServerID: auditServerA, Success: false},
		{Action: domain.AuditServerDelete, Target: "Old", UserID: "U1", ServerID: auditServerB, Success: true},
		{Action: domain.AuditServerCrashLoop, Target: "Survival: 3 restarts", ServerID: auditServerA, Success: true},
	} {
		e.CreatedAt, e.IP = at, "127.0.0.1"
		if err := a.Record(ctx, e); err != nil {
			t.Fatal(err)
		}
	}

	ids := func(f domain.AuditFilter) []int64 {
		t.Helper()
		events, err := a.List(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		out := []int64{}
		for _, e := range events {
			out = append(out, e.ID)
		}
		return out
	}
	for name, tc := range map[string]struct {
		f    domain.AuditFilter
		want []int64
	}{
		"all":            {domain.AuditFilter{Limit: 50}, []int64{6, 5, 4, 3, 2, 1}},
		"limit":          {domain.AuditFilter{Limit: 2}, []int64{6, 5}},
		"before":         {domain.AuditFilter{Limit: 2, BeforeID: 5}, []int64{4, 3}},
		"server":         {domain.AuditFilter{Limit: 50, ServerID: auditServerA}, []int64{6, 4, 3}},
		"action":         {domain.AuditFilter{Limit: 50, Action: domain.AuditLogin}, []int64{2, 1}},
		"failed":         {domain.AuditFilter{Limit: 50, FailedOnly: true}, []int64{4, 2}},
		"combined":       {domain.AuditFilter{Limit: 50, ServerID: auditServerA, Action: domain.AuditConsoleCommand}, []int64{3}},
		"deleted server": {domain.AuditFilter{Limit: 50, ServerID: auditServerB}, []int64{5}},
		"no match":       {domain.AuditFilter{Limit: 50, ServerID: auditServerA, FailedOnly: true, BeforeID: 4}, []int64{}},
	} {
		if got := ids(tc.f); !slices.Equal(got, tc.want) {
			t.Errorf("%s: ids = %v, want %v", name, got, tc.want)
		}
	}

	events, err := a.List(ctx, domain.AuditFilter{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[int64]domain.AuditEvent{}
	for _, e := range events {
		byID[e.ID] = e
	}
	if e := byID[3]; e.Username != "bruno" || e.ServerID != auditServerA || e.ServerName != "Survival" ||
		e.Target != "list" || !e.CreatedAt.Equal(at) || e.IP != "127.0.0.1" || !e.Success {
		t.Errorf("console event = %+v", e)
	}
	if e := byID[2]; e.Username != "" || e.UserID != "" || e.ServerID != "" || e.ServerName != "" || e.Success {
		t.Errorf("failed login = %+v", e)
	}
	if e := byID[5]; e.ServerID != auditServerB || e.ServerName != "" {
		t.Errorf("deleted server event = %+v", e)
	}
	if e := byID[6]; e.UserID != "" || e.Username != "" || e.ServerName != "Survival" {
		t.Errorf("panel event = %+v", e)
	}
}

// Events recorded before 0006 read back without a server.
func TestAuditServerMigration(t *testing.T) {
	ctx := context.Background()
	old := fstest.MapFS{}
	for _, name := range []string{"0001_init.sql", "0002_servers.sql", "0003_backups.sql", "0004_loader_version.sql", "0005_server_crash.sql"} {
		data, err := fs.ReadFile(migrations.FS, name)
		if err != nil {
			t.Fatal(err)
		}
		old[name] = &fstest.MapFile{Data: data}
	}
	path := filepath.Join(t.TempDir(), "db.sqlite")
	s, err := Open(ctx, path, old)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.write.ExecContext(ctx, `INSERT INTO audit_log (created_at, action, target, ip, success)
		VALUES (1, 'server.start', 'Survival', '127.0.0.1', 1)`); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s, err = Open(ctx, path, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	events, err := NewAuditLog(s, slog.New(slog.NewTextHandler(io.Discard, nil))).List(ctx, domain.AuditFilter{Limit: 10})
	if err != nil || len(events) != 1 || events[0].ServerID != "" || events[0].Target != "Survival" {
		t.Fatalf("events = %+v, %v", events, err)
	}
}
