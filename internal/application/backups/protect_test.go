package backups

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/bruno-mesquita/Craftkeeper/internal/domain"
)

func TestProtectBacksUpThenChanges(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	var during error
	err := e.svc.Protect(ctx, serverID, actor, func(context.Context) error {
		e.record("change")
		// The server stays held: no other backup or restore can start meanwhile.
		_, during = e.svc.Create(ctx, serverID, actor)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(during, domain.ErrBackupInProgress) {
		t.Fatalf("backup during the change: %v", during)
	}
	list, _ := e.repo.List(ctx, serverID)
	if len(list) != 1 || list[0].Status != domain.BackupReady || list[0].Kind != domain.BackupManual {
		t.Fatalf("backups = %+v", list)
	}
	if ev := e.events(); !slices.Equal(ev, []string{"write " + list[0].ID, "change"}) {
		t.Fatalf("events = %v", ev)
	}
	if _, err := e.svc.Create(ctx, serverID, actor); err != nil {
		t.Fatalf("server not released: %v", err)
	}
}

func TestProtectFailedBackupSkipsChange(t *testing.T) {
	e := newEnv(t)
	e.store.writeErr = errors.New("disk on fire")
	changed := false
	err := e.svc.Protect(context.Background(), serverID, actor, func(context.Context) error { changed = true; return nil })
	if !errors.Is(err, domain.ErrBackupFailed) || changed {
		t.Fatalf("err = %v, changed = %v", err, changed)
	}
	if !strings.Contains(err.Error(), domain.BackupFailureError) {
		t.Fatalf("failure reason missing: %v", err)
	}
}

func TestProtectRefusals(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		status domain.ServerStatus
		want   error
	}{
		{domain.StatusRunning, domain.ErrServerRunning},
		{domain.StatusStarting, domain.ErrServerBusy},
		{domain.StatusUnknown, domain.ErrDockerUnavailable},
	} {
		e := newEnv(t)
		e.srv.set(tc.status)
		changed := false
		err := e.svc.Protect(ctx, serverID, actor, func(context.Context) error { changed = true; return nil })
		if !errors.Is(err, tc.want) || changed {
			t.Errorf("%s: err = %v, changed = %v", tc.status, err, changed)
		}
		if list, _ := e.repo.List(ctx, serverID); len(list) != 0 {
			t.Errorf("%s: backups = %+v", tc.status, list)
		}
	}

	// A cancelled request keeps the backup but skips the change.
	e := newEnv(t)
	cctx, cancel := context.WithCancel(ctx)
	e.store.onWrite = func(context.Context) { cancel() }
	changed := false
	err := e.svc.Protect(cctx, serverID, actor, func(context.Context) error { changed = true; return nil })
	if !errors.Is(err, context.Canceled) || changed {
		t.Fatalf("cancelled: err = %v, changed = %v", err, changed)
	}
	if list, _ := e.repo.List(ctx, serverID); len(list) != 1 || list[0].Status != domain.BackupReady {
		t.Fatalf("cancelled: backups = %+v", list)
	}
}
