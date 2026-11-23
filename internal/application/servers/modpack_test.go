package servers

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/bruno-mesquita/Craftkeeper/internal/domain"
)

func TestMinecraftEnv(t *testing.T) {
	base := domain.Server{Loader: domain.LoaderForge, Version: "1.20.1", LoaderVersion: "47.3.0"}
	modrinth := base
	modrinth.LoaderVersion = ""
	modrinth.Modpack = domain.ModpackRef{Source: domain.ModpackSourceModrinth, ProjectID: "AAAAAAAA", VersionID: "BBBBBBBB"}
	cf := modrinth
	cf.Modpack = domain.ModpackRef{Source: domain.ModpackSourceCurseForge, ProjectID: "123", Slug: "pack", VersionID: "456"}
	for _, tc := range []struct {
		name       string
		x          domain.Server
		want, deny []string
	}{
		{"loader", base, []string{"TYPE=FORGE", "VERSION=1.20.1", "FORGE_VERSION=47.3.0"}, []string{"MODRINTH", "CF_"}},
		{"modrinth", modrinth, []string{"TYPE=MODRINTH", "VERSION=1.20.1", "MODRINTH_MODPACK=AAAAAAAA", "MODRINTH_VERSION=BBBBBBBB"},
			[]string{"FORGE_VERSION", "CF_"}},
		{"curseforge", cf, []string{"TYPE=AUTO_CURSEFORGE", "CF_API_KEY=key", "CF_SLUG=pack", "CF_FILE_ID=456", "CF_DOWNLOADS_REPO=/data/downloads"},
			[]string{"FORGE_VERSION", "VERSION=", "MODRINTH"}},
	} {
		name := tc.name
		env := minecraftEnv(tc.x, "key")
		if !slices.Contains(env, "EULA=TRUE") {
			t.Errorf("%s: no EULA in %v", name, env)
		}
		for _, w := range tc.want {
			if !slices.Contains(env, w) {
				t.Errorf("%s: %v lacks %s", name, env, w)
			}
		}
		for _, d := range tc.deny {
			for _, v := range env {
				if strings.HasPrefix(v, d) {
					t.Errorf("%s: unexpected %s", name, v)
				}
			}
		}
	}
}

func packInput(name string, ref domain.ModpackRef) CreateInput {
	in := validInput(name)
	in.Loader, in.Version, in.Modpack = domain.LoaderFabric, "1.21.1", ref
	return in
}

func TestCreateAndChangeModpack(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	ref := domain.ModpackRef{Source: domain.ModpackSourceModrinth, ProjectID: "AAAAAAAA", VersionID: "BBBBBBBB", Name: "Pack", VersionName: "1.0"}
	v, err := e.create(ctx, packInput("S", ref), e.actor)
	if err != nil {
		t.Fatal(err)
	}
	if v.Modpack != ref || v.LoaderVersion != "" || v.Status != domain.StatusStopped {
		t.Fatalf("created = %+v", v)
	}
	if env := e.rt.specs[v.ContainerID].Env; !slices.Contains(env, "MODRINTH_VERSION=BBBBBBBB") {
		t.Fatalf("env = %v", env)
	}

	next := ref
	next.VersionID, next.VersionName = "CCCCCCCC", "2.0"
	got, err := e.svc.ChangeModpack(ctx, v.ID, next, VersionInput{Loader: domain.LoaderFabric, Version: "1.21.4"}, e.actor)
	if err != nil {
		t.Fatal(err)
	}
	if got.ContainerID == v.ContainerID || got.Modpack != next || got.Version != "1.21.4" {
		t.Fatalf("changed = %+v", got)
	}
	if env := e.rt.specs[got.ContainerID].Env; !slices.Contains(env, "MODRINTH_VERSION=CCCCCCCC") {
		t.Fatalf("env = %v", env)
	}
	if stored, _ := e.repo.ByID(ctx, v.ID); stored.Modpack != next || stored.ContainerID != got.ContainerID {
		t.Fatalf("stored = %+v", stored)
	}
	events, _ := e.auditDB.List(ctx, domain.AuditFilter{Action: domain.AuditModpackUpdate, Limit: 10})
	if len(events) != 1 || !events[0].Success || events[0].Target != "S: Pack 1.0 → 2.0" {
		t.Fatalf("audit = %+v", events)
	}

	// Refused: a running server, a bad reference, a loader the panel cannot run.
	if _, err := e.svc.Start(ctx, v.ID, e.actor); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.ChangeModpack(ctx, v.ID, ref, VersionInput{Loader: domain.LoaderFabric, Version: "1.21.1"}, e.actor); !errors.Is(err, domain.ErrServerRunning) {
		t.Fatalf("running: %v", err)
	}
	bad := ref
	bad.VersionID = "x;rm"
	if _, err := e.svc.ChangeModpack(ctx, v.ID, bad, VersionInput{Loader: domain.LoaderFabric, Version: "1.21.1"}, e.actor); fieldOf(err) != "version_id" {
		t.Fatalf("bad ref: %v", err)
	}
	if _, err := e.svc.ChangeModpack(ctx, v.ID, ref, VersionInput{Loader: domain.LoaderVanilla, Version: "1.21.1"}, e.actor); !errors.Is(err, domain.ErrModpackUnsupported) {
		t.Fatalf("vanilla pack: %v", err)
	}
}

func TestCreateModpackRefusals(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	cf := domain.ModpackRef{Source: domain.ModpackSourceCurseForge, ProjectID: "123", Slug: "pack", VersionID: "456"}
	if _, err := e.svc.Create(ctx, packInput("A", cf), e.actor); !errors.Is(err, domain.ErrCurseForgeNotConfigured) {
		t.Fatalf("no key: %v", err)
	}
	withBuild := packInput("B", domain.ModpackRef{Source: domain.ModpackSourceModrinth, ProjectID: "AAAAAAAA", VersionID: "BBBBBBBB"})
	withBuild.LoaderVersion = "0.16.10"
	if _, err := e.svc.Create(ctx, withBuild, e.actor); fieldOf(err) != "loader_version" {
		t.Fatalf("loader build with a pack: %v", err)
	}
	noSlug := cf
	noSlug.Slug = "Bad Slug"
	if _, err := e.svc.Create(ctx, packInput("C", noSlug), e.actor); fieldOf(err) != "slug" {
		t.Fatalf("bad slug: %v", err)
	}

	e.svc.cfg.CurseForgeKey = func(context.Context) (string, error) { return "key", nil }
	v, err := e.create(ctx, packInput("D", cf), e.actor)
	if err != nil {
		t.Fatal(err)
	}
	if env := e.rt.specs[v.ContainerID].Env; !slices.Contains(env, "CF_API_KEY=key") || !slices.Contains(env, "CF_SLUG=pack") {
		t.Fatalf("env = %v", env)
	}
}
