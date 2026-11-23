package sqlite

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/bruno-mesquita/Craftkeeper/migrations"
)

func TestSettingsCurseForgeKey(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "db.sqlite"), migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r := NewSettings(s)

	if k, err := r.CurseForgeKey(ctx); k != "" || err != nil {
		t.Fatalf("unset = %q, %v", k, err)
	}
	for _, want := range []string{"$2a$10$first", "$2a$10$second"} {
		if err := r.SetCurseForgeKey(ctx, want); err != nil {
			t.Fatal(err)
		}
		if k, err := r.CurseForgeKey(ctx); k != want || err != nil {
			t.Fatalf("after set = %q, %v; want %q", k, err, want)
		}
	}
	if err := r.SetCurseForgeKey(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if k, err := r.CurseForgeKey(ctx); k != "" || err != nil {
		t.Fatalf("after removal = %q, %v", k, err)
	}
}

func TestSettingsPublicHost(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "db.sqlite"), migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r := NewSettings(s)

	if err := r.SetCurseForgeKey(ctx, "cf-key"); err != nil {
		t.Fatal(err)
	}
	if h, err := r.PublicHost(ctx); h != "" || err != nil {
		t.Fatalf("unset = %q, %v", h, err)
	}
	if err := r.SetPublicHost(ctx, "play.example.com"); err != nil {
		t.Fatal(err)
	}
	if h, err := r.PublicHost(ctx); h != "play.example.com" || err != nil {
		t.Fatalf("after set = %q, %v", h, err)
	}
	if err := r.SetPublicHost(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if h, err := r.PublicHost(ctx); h != "" || err != nil {
		t.Fatalf("after removal = %q, %v", h, err)
	}
	if k, _ := r.CurseForgeKey(ctx); k != "cf-key" {
		t.Fatalf("CurseForge key changed: %q", k)
	}
}
