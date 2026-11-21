package servers

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bruno-mesquita/Craftkeeper/internal/domain"
)

func TestChangeVersion(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	in := validInput("S")
	in.Port = 25602
	v, err := e.create(ctx, in, e.actor)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.repo.MarkCrashed(ctx, v.ID, time.Now(), 3); err != nil {
		t.Fatal(err)
	}
	oldCID := v.ContainerID

	got, err := e.svc.ChangeVersion(ctx, v.ID, VersionInput{Loader: domain.LoaderFabric, Version: "1.21.1"}, e.actor)
	if err != nil {
		t.Fatal(err)
	}
	if got.ContainerID == oldCID || got.Loader != domain.LoaderFabric || got.Version != "1.21.1" ||
		got.LoaderVersion != "0.20.1" || got.Status != domain.StatusStopped {
		t.Fatalf("view = %+v", got)
	}
	if _, ok := e.rt.containers[oldCID]; ok {
		t.Fatal("old container kept")
	}
	spec := e.rt.specs[got.ContainerID]
	env := strings.Join(spec.Env, " ")
	if spec.Java != domain.Java21 || spec.Port != 25602 || spec.MemoryMB != in.MemoryMB || spec.CPUMillicores != in.CPUMillicores ||
		!strings.Contains(env, "TYPE=FABRIC") || !strings.Contains(env, "FABRIC_LOADER_VERSION=0.20.1") {
		t.Fatalf("spec = %+v", spec)
	}
	stored, _ := e.repo.ByID(ctx, v.ID)
	if stored.ContainerID != got.ContainerID || stored.LoaderVersion != "0.20.1" || !stored.CrashedAt.IsZero() {
		t.Fatalf("stored = %+v", stored)
	}
	calls := strings.Join(e.rt.calls, ",")
	if !strings.Contains(calls, "rename-replaced,create") {
		t.Fatalf("calls = %s", calls)
	}
	events, _ := e.auditDB.List(ctx, domain.AuditFilter{Action: domain.AuditServerVersionChange, Limit: 10})
	if len(events) != 1 || !events[0].Success || events[0].Target != "S: vanilla LATEST → fabric 1.21.1 0.20.1" {
		t.Fatalf("audit = %+v", events)
	}

	// Downgrade back to vanilla is allowed.
	if got, err = e.svc.ChangeVersion(ctx, v.ID, VersionInput{Loader: domain.LoaderVanilla, Version: "1.20.1"}, e.actor); err != nil ||
		got.LoaderVersion != "" || e.rt.specs[got.ContainerID].Java != domain.Java17 {
		t.Fatalf("downgrade: %+v, %v", got, err)
	}
}

func TestChangeVersionRefusals(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	v, _ := e.create(ctx, validInput("S"), e.actor)
	fabric := VersionInput{Loader: domain.LoaderFabric, Version: "1.21.1"}

	if _, err := e.svc.Start(ctx, v.ID, e.actor); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.ChangeVersion(ctx, v.ID, fabric, e.actor); !errors.Is(err, domain.ErrServerRunning) {
		t.Fatalf("running: %v", err)
	}
	if _, err := e.svc.Stop(ctx, v.ID, e.actor); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.ChangeVersion(ctx, v.ID, VersionInput{Loader: domain.LoaderForge, Version: "LATEST"}, e.actor); fieldOf(err) != "version" {
		t.Fatalf("bad version: %v", err)
	}
	if _, ok := e.rt.containers[v.ContainerID]; !ok || len(e.rt.containers) != 1 {
		t.Fatal("refusals must not touch the container")
	}
}

func TestChangeVersionRollsBack(t *testing.T) {
	boom := errors.New("boom")
	ctx := context.Background()
	for _, op := range []string{"pull", "create", "remove"} {
		e := newEnv(t)
		v, _ := e.create(ctx, validInput("S"), e.actor)
		e.rt.calls = nil
		e.rt.fail[op] = boom
		if _, err := e.svc.ChangeVersion(ctx, v.ID, VersionInput{Loader: domain.LoaderFabric, Version: "1.21.1"}, e.actor); !errors.Is(err, boom) {
			t.Fatalf("%s: err = %v", op, err)
		}
		stored, _ := e.repo.ByID(ctx, v.ID)
		if stored.ContainerID != v.ContainerID || stored.Loader != domain.LoaderVanilla || stored.Version != "LATEST" {
			t.Fatalf("%s: stored = %+v", op, stored)
		}
		if _, ok := e.rt.containers[v.ContainerID]; !ok {
			t.Fatalf("%s: old container lost", op)
		}
		calls := strings.Join(e.rt.calls, ",")
		if op != "pull" && !strings.HasSuffix(calls, "rename-back") {
			t.Fatalf("%s: old container not renamed back: %s", op, calls)
		}
		if _, err := e.svc.Start(ctx, v.ID, e.actor); err != nil {
			t.Fatalf("%s: server unusable after rollback: %v", op, err)
		}
	}
}

func TestChangeVersionBlocksActions(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	v, _ := e.create(ctx, validInput("S"), e.actor)
	if err := e.svc.beginReplace(v.ID); err != nil {
		t.Fatal(err)
	}
	name := "Other"
	vanilla := VersionInput{Loader: "vanilla", Version: "1.21.1"}
	for what, err := range map[string]error{
		"start":   func() error { _, err := e.svc.Start(ctx, v.ID, e.actor); return err }(),
		"update":  func() error { _, err := e.svc.Update(ctx, v.ID, UpdateInput{Name: &name}, e.actor); return err }(),
		"delete":  e.svc.Delete(ctx, v.ID, true, e.actor),
		"version": func() error { _, err := e.svc.ChangeVersion(ctx, v.ID, vanilla, e.actor); return err }(),
	} {
		if !errors.Is(err, domain.ErrServerBusy) {
			t.Errorf("%s while replacing: %v", what, err)
		}
	}
	e.svc.endReplace(v.ID)
	if _, err := e.svc.Start(ctx, v.ID, e.actor); err != nil {
		t.Fatal(err)
	}
}

func TestRecoverRemovesReplacedContainer(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	v, _ := e.create(ctx, validInput("S"), e.actor)
	// What an interrupted version change leaves: a second container of the same server.
	stale, err := e.rt.Create(ctx, domain.ContainerSpec{ServerID: v.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.rt.containers[stale]; ok {
		t.Fatal("stale container kept")
	}
	if _, ok := e.rt.containers[v.ContainerID]; !ok {
		t.Fatal("recorded container removed")
	}
}
