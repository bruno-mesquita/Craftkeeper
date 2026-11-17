package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/bruno-mesquita/mesquita-panel/internal/domain"
	"github.com/bruno-mesquita/mesquita-panel/migrations"
)

func TestServersRepository(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "db.sqlite"), migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r := NewServers(s)

	now := time.Unix(1_800_000_000, 0).UTC()
	a := domain.Server{ID: "AAAAAAAAAAAAAAAAAAAAAAAAAA", Name: "Survival", Game: domain.GameMinecraft, Loader: domain.LoaderVanilla,
		Version: "LATEST", MemoryMB: 2048, CPUMillicores: 2000, Port: 25601, CreatedAt: now, UpdatedAt: now}
	if err := r.Create(ctx, a); err != nil {
		t.Fatal(err)
	}

	var ve *domain.ValidationError
	dupName := a
	dupName.ID, dupName.Name, dupName.Port = "BBBBBBBBBBBBBBBBBBBBBBBBBB", "survival", 25602
	if err := r.Create(ctx, dupName); !errors.As(err, &ve) || ve.Field != "name" {
		t.Fatalf("duplicate name (case-insensitive): err = %v", err)
	}
	dupPort := a
	dupPort.ID, dupPort.Name = "BBBBBBBBBBBBBBBBBBBBBBBBBB", "Creative"
	if err := r.Create(ctx, dupPort); !errors.As(err, &ve) || ve.Field != "port" {
		t.Fatalf("duplicate port: err = %v", err)
	}

	a.Description, a.MemoryMB, a.ContainerID, a.UpdatedAt = "desc", 4096, "cid", now.Add(time.Hour)
	if err := r.Update(ctx, a); err != nil {
		t.Fatal(err)
	}
	got, err := r.ByID(ctx, a.ID)
	if err != nil || got != a {
		t.Fatalf("ByID = %+v, %v; want %+v", got, err, a)
	}

	list, err := r.List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("List = %v, %v", list, err)
	}
	used, err := r.UsedPorts(ctx)
	if err != nil || !used[25601] || len(used) != 1 {
		t.Fatalf("UsedPorts = %v, %v", used, err)
	}

	if err := r.Delete(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ByID(ctx, a.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("after delete: err = %v", err)
	}
	if err := r.Update(ctx, a); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("update after delete: err = %v", err)
	}
}
