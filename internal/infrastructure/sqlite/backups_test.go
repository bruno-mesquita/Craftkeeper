package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/bruno-mesquita/Craftkeeper/internal/domain"
	"github.com/bruno-mesquita/Craftkeeper/migrations"
)

func TestBackupsRepository(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "db.sqlite"), migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	servers, r := NewServers(s), NewBackups(s)

	now := time.Unix(1_800_000_000, 0).UTC()
	srv := domain.Server{ID: "AAAAAAAAAAAAAAAAAAAAAAAAAA", Name: "Survival", Game: domain.GameMinecraft, Loader: domain.LoaderVanilla,
		Version: "LATEST", MemoryMB: 2048, CPUMillicores: 2000, Port: 25601, CreatedAt: now, UpdatedAt: now}
	if err := servers.Create(ctx, srv); err != nil {
		t.Fatal(err)
	}
	backup := func(id string, kind domain.BackupKind, status domain.BackupStatus, at time.Time) domain.Backup {
		b := domain.Backup{ID: id, ServerID: srv.ID, Kind: kind, Status: status, CreatedAt: at}
		if err := r.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
		return b
	}

	a := backup("BACKUPAAAAAAAAAAAAAAAAAAAA", domain.BackupManual, domain.BackupCreating, now)
	got, err := r.ByID(ctx, srv.ID, a.ID)
	if err != nil || got != a {
		t.Fatalf("ByID = %+v, %v", got, err)
	}
	if _, err := r.ByID(ctx, "BBBBBBBBBBBBBBBBBBBBBBBBBB", a.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("backup of another server = %v", err)
	}
	a.Status, a.SizeBytes, a.CompletedAt = domain.BackupReady, 1234, now.Add(time.Minute)
	if err := r.Finish(ctx, a); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.ByID(ctx, srv.ID, a.ID); got != a {
		t.Fatalf("after Finish = %+v", got)
	}
	if err := r.Finish(ctx, domain.Backup{ID: "MISSINGAAAAAAAAAAAAAAAAAAA"}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Finish of a missing backup = %v", err)
	}

	// Interrupted backups fail at startup.
	c := backup("BACKUPCCCCCCCCCCCCCCCCCCCC", domain.BackupManual, domain.BackupCreating, now.Add(2*time.Hour))
	if err := r.FailInterrupted(ctx, now.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.ByID(ctx, srv.ID, c.ID); got.Status != domain.BackupFailed || got.Failure != domain.BackupFailureInterrupted ||
		!got.CompletedAt.Equal(now.Add(3*time.Hour)) {
		t.Fatalf("interrupted = %+v", got)
	}

	// Retention: only scheduled backups, newest kept, failed ones always.
	s1 := backup("SCHED1AAAAAAAAAAAAAAAAAAAA", domain.BackupScheduled, domain.BackupReady, now.Add(10*time.Hour))
	s2 := backup("SCHED2AAAAAAAAAAAAAAAAAAAA", domain.BackupScheduled, domain.BackupReady, now.Add(11*time.Hour))
	s3 := backup("SCHED3AAAAAAAAAAAAAAAAAAAA", domain.BackupScheduled, domain.BackupReady, now.Add(12*time.Hour))
	sf := backup("SCHEDFAAAAAAAAAAAAAAAAAAAA", domain.BackupScheduled, domain.BackupFailed, now.Add(9*time.Hour))
	ids := func(list []domain.Backup) (out []string) {
		for _, b := range list {
			out = append(out, b.ID)
		}
		return out
	}
	if p, err := r.Prunable(ctx, srv.ID, 2); err != nil || len(p) != 2 || p[0].ID != sf.ID || p[1].ID != s1.ID {
		t.Fatalf("Prunable(2) = %v, %v", ids(p), err)
	}
	if p, _ := r.Prunable(ctx, srv.ID, 0); len(p) != 1 || p[0].ID != sf.ID {
		t.Fatalf("Prunable(0) = %v", ids(p))
	}

	list, err := r.List(ctx, srv.ID)
	if err != nil || len(list) != 6 || list[0].ID != s3.ID || list[1].ID != s2.ID || list[5].ID != a.ID {
		t.Fatalf("List = %v, %v", ids(list), err)
	}
	if err := r.Delete(ctx, s1.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ByID(ctx, srv.ID, s1.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("after Delete = %v", err)
	}

	// Schedules.
	sc, err := r.Schedule(ctx, srv.ID)
	if err != nil || sc != (domain.BackupSchedule{ServerID: srv.ID}) {
		t.Fatalf("default schedule = %+v, %v", sc, err)
	}
	sc = domain.BackupSchedule{ServerID: srv.ID, IntervalHours: 6, Retain: 3, NextRunAt: now.Add(time.Hour)}
	if err := r.PutSchedule(ctx, sc); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.Schedule(ctx, srv.ID); got != sc {
		t.Fatalf("Schedule = %+v", got)
	}
	if due, _ := r.DueSchedules(ctx, now); len(due) != 0 {
		t.Fatalf("not yet due: %+v", due)
	}
	if due, _ := r.DueSchedules(ctx, now.Add(time.Hour)); len(due) != 1 || due[0] != sc {
		t.Fatalf("due = %+v", due)
	}
	if err := r.SetNextRun(ctx, srv.ID, now.Add(7*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if due, _ := r.DueSchedules(ctx, now.Add(2*time.Hour)); len(due) != 0 {
		t.Fatalf("after SetNextRun: %+v", due)
	}
	off := domain.BackupSchedule{ServerID: srv.ID, Retain: 3}
	if err := r.PutSchedule(ctx, off); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.Schedule(ctx, srv.ID); got != off {
		t.Fatalf("schedule off = %+v", got)
	}
	if due, _ := r.DueSchedules(ctx, now.Add(1000*time.Hour)); len(due) != 0 {
		t.Fatalf("an off schedule is never due: %+v", due)
	}

	// Rows go with their server.
	if err := servers.Delete(ctx, srv.ID); err != nil {
		t.Fatal(err)
	}
	if list, _ := r.List(ctx, srv.ID); len(list) != 0 {
		t.Fatalf("backups of a deleted server: %v", ids(list))
	}
	if got, _ := r.Schedule(ctx, srv.ID); got.Retain != 0 {
		t.Fatalf("schedule of a deleted server: %+v", got)
	}
}
