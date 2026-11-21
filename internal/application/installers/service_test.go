package installers

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/bruno-mesquita/Craftkeeper/internal/domain"
)

type fakeSource struct {
	calls map[string]int
	fail  bool
}

func (f *fakeSource) hit(name string) error {
	f.calls[name]++
	if f.fail {
		return domain.ErrInstallerUnavailable
	}
	return nil
}

func (f *fakeSource) Minecraft(context.Context) ([]string, error) {
	return []string{"1.21.1", "1.20.1"}, f.hit("minecraft")
}

func (f *fakeSource) Fabric(context.Context) (map[string]domain.LoaderVersions, error) {
	return map[string]domain.LoaderVersions{"1.21.1": {Versions: []string{"0.20.1"}, Recommended: "0.20.1"}}, f.hit("fabric")
}

func (f *fakeSource) Forge(context.Context) (map[string]domain.LoaderVersions, error) {
	return map[string]domain.LoaderVersions{
		"1.20.1": {Versions: []string{"47.4.5", "47.3.0"}, Recommended: "47.3.0"},
		"1.9":    {Versions: []string{"12.16.1.1887"}, Recommended: "12.16.1.1887"},
		"1.21":   {Versions: []string{"51.0.33"}, Recommended: "51.0.33"},
	}, f.hit("forge")
}

func (f *fakeSource) NeoForge(context.Context) (map[string]domain.LoaderVersions, error) {
	return map[string]domain.LoaderVersions{"1.21.1": {Versions: []string{"21.1.267"}, Recommended: "21.1.267"}}, f.hit("neoforge")
}

func newService() (*Service, *fakeSource) {
	src := &fakeSource{calls: map[string]int{}}
	return NewService(src, time.Hour), src
}

func TestMinecraftVersions(t *testing.T) {
	s, src := newService()
	ctx := context.Background()
	got, err := s.MinecraftVersions(ctx, domain.LoaderVanilla)
	if err != nil || !reflect.DeepEqual(got, []string{"LATEST", "1.21.1", "1.20.1"}) {
		t.Fatalf("vanilla = %v, %v", got, err)
	}
	got, err = s.MinecraftVersions(ctx, domain.LoaderForge)
	if err != nil || !reflect.DeepEqual(got, []string{"1.21", "1.20.1", "1.9"}) {
		t.Fatalf("forge = %v, %v", got, err)
	}
	if _, err := s.MinecraftVersions(ctx, "paper"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown loader: err = %v", err)
	}
	// Cached: one fetch per source for repeated calls.
	_, _ = s.MinecraftVersions(ctx, domain.LoaderForge)
	_, _ = s.LoaderVersions(ctx, domain.LoaderForge, "1.20.1")
	if src.calls["forge"] != 1 || src.calls["minecraft"] != 1 {
		t.Fatalf("calls = %v", src.calls)
	}
}

func TestLoaderVersions(t *testing.T) {
	s, _ := newService()
	ctx := context.Background()
	lv, err := s.LoaderVersions(ctx, domain.LoaderForge, "1.20.1")
	if err != nil || lv.Recommended != "47.3.0" || len(lv.Versions) != 2 {
		t.Fatalf("LoaderVersions = %+v, %v", lv, err)
	}
	for _, c := range [][2]string{{"forge", "1.12.2"}, {"vanilla", "1.20.1"}, {"paper", "1.20.1"}} {
		if _, err := s.LoaderVersions(ctx, c[0], c[1]); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("%v: err = %v", c, err)
		}
	}
}

func TestResolve(t *testing.T) {
	s, src := newService()
	ctx := context.Background()

	ok := []struct{ loader, mc, lv, want string }{
		{"vanilla", "LATEST", "", ""},
		{"vanilla", "1.16.5", "", ""},
		{"forge", "1.20.1", "", "47.3.0"},
		{"forge", "1.20.1", "47.4.5", "47.4.5"},
		{"fabric", "1.21.1", "", "0.20.1"},
		{"neoforge", "1.21.1", "21.1.267", "21.1.267"},
	}
	for _, c := range ok {
		if got, err := s.Resolve(ctx, c.loader, c.mc, c.lv); err != nil || got != c.want {
			t.Errorf("Resolve(%s, %s, %q) = %q, %v; want %q", c.loader, c.mc, c.lv, got, err, c.want)
		}
	}
	if src.calls["minecraft"] != 0 {
		t.Fatal("vanilla must not query the version source")
	}

	bad := []struct{ loader, mc, lv, field string }{
		{"paper", "1.20.1", "", "loader"},
		{"vanilla", "1.20.1", "47.3.0", "loader_version"},
		{"vanilla", "1.20; rm", "", "version"},
		{"forge", "LATEST", "", "version"},
		{"forge", "1.12.2", "", "version"},
		{"forge", "1.20.1", "47.9.9", "loader_version"},
		{"forge", "1.20.1", "$(id)", "loader_version"},
		{"neoforge", "1.20.1", "", "version"},
	}
	for _, c := range bad {
		var ve *domain.ValidationError
		if _, err := s.Resolve(ctx, c.loader, c.mc, c.lv); !errors.As(err, &ve) || ve.Field != c.field {
			t.Errorf("Resolve(%s, %s, %q): err = %v, want field %s", c.loader, c.mc, c.lv, err, c.field)
		}
	}
}

func TestResolveSourceDown(t *testing.T) {
	s, src := newService()
	src.fail = true
	if _, err := s.Resolve(context.Background(), domain.LoaderForge, "1.20.1", ""); !errors.Is(err, domain.ErrInstallerUnavailable) {
		t.Fatalf("err = %v", err)
	}
	src.fail = false // failures are not cached
	if got, err := s.Resolve(context.Background(), domain.LoaderForge, "1.20.1", ""); err != nil || got != "47.3.0" {
		t.Fatalf("after recovery: %q, %v", got, err)
	}
}
