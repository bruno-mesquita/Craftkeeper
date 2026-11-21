package gameversions

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/bruno-mesquita/Craftkeeper/internal/domain"
)

// Reduced copies of the real responses (shapes checked on 2026-11-17).
const mojangJSON = `{"latest":{"release":"26.3","snapshot":"26.4-snapshot-10"},"versions":[
		{"id":"26.4-snapshot-10","type":"snapshot"},{"id":"26.3","type":"release"},{"id":"26.1.2","type":"release"},
		{"id":"1.21.11","type":"release"},{"id":"1.21.9","type":"release"},{"id":"1.20.1","type":"release"},
		{"id":"b1.7.3","type":"old_beta"},{"id":"1.RV-Pre1","type":"snapshot"}]}`
const fabricGameJSON = `[{"version":"26.4-snapshot-10","stable":false},{"version":"26.3","stable":true},
		{"version":"1.21.11","stable":true},{"version":"1.20.1","stable":true},{"version":"23w13a_or_b","stable":false}]`
const fabricLoaderJSON = `[{"separator":".","build":1,"maven":"net.fabricmc:fabric-loader:0.20.1","version":"0.20.1","stable":true},
		{"version":"0.20.0","stable":true},{"version":"0.20.2","stable":false},{"version":"0.9.3+build.207","stable":true}]`
const forgeMavenXML = `<?xml version="1.0" encoding="UTF-8"?>
<metadata><groupId>net.minecraftforge</groupId><artifactId>forge</artifactId><versioning><versions>
<version>1.21-51.0.33</version><version>1.21-51.0.32</version>
<version>1.20.1-47.4.5</version><version>1.20.1-47.3.0</version><version>1.20.1-47.4.4</version>
<version>1.7.10-10.13.4.1614-1.7.10</version><version>1.7.10_pre4-10.12.2.1149-prerelease</version>
<version>1.12.2-14.23.5.2859</version><version>1.12.2-14.23.4.2720-4627</version>
<version>26.3-66.1.8</version>
</versions></versioning></metadata>`
const forgePromotionsJSON = `{"homepage":"x","promos":{"1.20.1-latest":"47.4.5","1.20.1-recommended":"47.3.0",
		"1.21-latest":"51.0.33","26.3-recommended":"66.1.9"}}`
const neoForgeMavenXML = `<?xml version="1.0" encoding="UTF-8"?>
<metadata><versioning><versions>
<version>20.2.3-beta</version><version>20.2.86</version><version>20.4.80-beta</version>
<version>21.0.0-beta</version><version>21.1.77</version><version>21.1.267</version>
<version>26.1.2.127</version><version>26.3.0.109</version><version>26.3.0.110-beta</version>
<version>1.20.1-47.1.106</version>
</versions></versioning></metadata>`

var fixtures = map[string]string{
	"/mojang":           mojangJSON,
	"/fabric/game":      fabricGameJSON,
	"/fabric/loader":    fabricLoaderJSON,
	"/forge/maven":      forgeMavenXML,
	"/forge/promotions": forgePromotionsJSON,
	"/neoforge/maven":   neoForgeMavenXML,
}

func newSources(t *testing.T, override map[string]func(http.ResponseWriter)) *Sources {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != userAgent {
			t.Errorf("User-Agent = %q", r.Header.Get("User-Agent"))
		}
		if f, ok := override[r.URL.Path]; ok {
			f(w)
			return
		}
		body, ok := fixtures[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return New(URLs{
		Mojang: srv.URL + "/mojang", FabricGame: srv.URL + "/fabric/game", FabricLoader: srv.URL + "/fabric/loader",
		ForgeMaven: srv.URL + "/forge/maven", ForgePromotions: srv.URL + "/forge/promotions", NeoForgeMaven: srv.URL + "/neoforge/maven",
	})
}

func TestMinecraft(t *testing.T) {
	got, err := newSources(t, nil).Minecraft(context.Background())
	want := []string{"26.3", "26.1.2", "1.21.11", "1.21.9", "1.20.1"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("Minecraft = %v, %v; want %v", got, err, want)
	}
}

func TestFabric(t *testing.T) {
	got, err := newSources(t, nil).Fabric(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := domain.LoaderVersions{Versions: []string{"0.20.1", "0.20.0"}, Recommended: "0.20.1"}
	if len(got) != 3 || !reflect.DeepEqual(got["1.20.1"], want) || !reflect.DeepEqual(got["26.3"], want) {
		t.Fatalf("Fabric = %+v", got)
	}
}

func TestForge(t *testing.T) {
	got, err := newSources(t, nil).Forge(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]domain.LoaderVersions{
		"1.21":   {Versions: []string{"51.0.33", "51.0.32"}, Recommended: "51.0.33"}, // latest promo
		"1.20.1": {Versions: []string{"47.4.5", "47.4.4", "47.3.0"}, Recommended: "47.3.0"},
		"1.12.2": {Versions: []string{"14.23.5.2859"}, Recommended: "14.23.5.2859"}, // no promo
		"26.3":   {Versions: []string{"66.1.8"}, Recommended: "66.1.8"},             // promo not in maven
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Forge = %+v\nwant %+v", got, want)
	}
}

func TestNeoForge(t *testing.T) {
	got, err := newSources(t, nil).NeoForge(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]domain.LoaderVersions{
		"1.20.2": {Versions: []string{"20.2.86", "20.2.3-beta"}, Recommended: "20.2.86"},
		"1.20.4": {Versions: []string{"20.4.80-beta"}, Recommended: "20.4.80-beta"},
		"1.21":   {Versions: []string{"21.0.0-beta"}, Recommended: "21.0.0-beta"},
		"1.21.1": {Versions: []string{"21.1.267", "21.1.77"}, Recommended: "21.1.267"},
		"26.1.2": {Versions: []string{"26.1.2.127"}, Recommended: "26.1.2.127"},
		"26.3":   {Versions: []string{"26.3.0.110-beta", "26.3.0.109"}, Recommended: "26.3.0.109"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("NeoForge = %+v\nwant %+v", got, want)
	}
}

func TestNeoForgeMinecraft(t *testing.T) {
	ok := map[string]string{
		"20.2.86": "1.20.2", "21.0.0-beta": "1.21", "21.1.77": "1.21.1",
		"26.1.2.127": "26.1.2", "26.3.0.110": "26.3",
	}
	for build, want := range ok {
		if got, valid := neoForgeMinecraft(build); !valid || got != want {
			t.Errorf("%s → %q, %v; want %q", build, got, valid, want)
		}
	}
	for _, build := range []string{"19.2.1", "21.1", "26.3.0", "21.1.1.1"} {
		if got, valid := neoForgeMinecraft(build); valid {
			t.Errorf("%s accepted as %q", build, got)
		}
	}
}

func TestSourceFailures(t *testing.T) {
	ctx := context.Background()
	cases := map[string]func(http.ResponseWriter){
		"status": func(w http.ResponseWriter) { w.WriteHeader(http.StatusBadGateway) },
		"json":   func(w http.ResponseWriter) { _, _ = w.Write([]byte(`{"versions":`)) },
		"large":  func(w http.ResponseWriter) { _, _ = w.Write([]byte(strings.Repeat(" ", maxBody+1))) },
	}
	for name, f := range cases {
		s := newSources(t, map[string]func(http.ResponseWriter){"/mojang": f, "/forge/maven": f})
		if _, err := s.Minecraft(ctx); !errors.Is(err, domain.ErrInstallerUnavailable) {
			t.Errorf("%s: Minecraft err = %v", name, err)
		}
		if _, err := s.Forge(ctx); !errors.Is(err, domain.ErrInstallerUnavailable) {
			t.Errorf("%s: Forge err = %v", name, err)
		}
	}
	s := New(URLs{Mojang: "http://127.0.0.1:1/unreachable"})
	if _, err := s.Minecraft(ctx); !errors.Is(err, domain.ErrInstallerUnavailable) {
		t.Errorf("unreachable: err = %v", err)
	}
}
