package httpapi_test

import (
	"context"
	"net/http"
	"reflect"
	"sync"
	"testing"

	"github.com/bruno-mesquita/mesquita-panel/internal/domain"
)

// stubSource is the official version catalog for HTTP tests. Its adapter is tested against
// recorded responses; here only the contract matters.
type stubSource struct {
	mu   sync.Mutex
	down bool
}

func (s *stubSource) err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.down {
		return domain.ErrInstallerUnavailable
	}
	return nil
}

func (s *stubSource) Minecraft(context.Context) ([]string, error) {
	return []string{"1.21.1", "1.20.1"}, s.err()
}

func (s *stubSource) Fabric(context.Context) (map[string]domain.LoaderVersions, error) {
	return map[string]domain.LoaderVersions{}, s.err()
}

func (s *stubSource) Forge(context.Context) (map[string]domain.LoaderVersions, error) {
	return map[string]domain.LoaderVersions{
		"1.20.1": {Versions: []string{"47.4.5", "47.3.0"}, Recommended: "47.3.0"},
		"1.21":   {Versions: []string{"51.0.33"}, Recommended: "51.0.33"},
	}, s.err()
}

func (s *stubSource) NeoForge(context.Context) (map[string]domain.LoaderVersions, error) {
	return map[string]domain.LoaderVersions{"1.21.1": {Versions: []string{"21.1.267"}, Recommended: "21.1.267"}}, s.err()
}

func TestInstallersContract(t *testing.T) {
	c := newServer(t)
	res, body := c.do("GET", "/api/v1/installers/forge/versions", nil, nil)
	expect(t, res, body, http.StatusUnauthorized, "unauthenticated")
	c.setupAndLogin()

	res, body = c.do("GET", "/api/v1/installers/vanilla/versions", nil, nil)
	expect(t, res, body, http.StatusOK, "")
	if !reflect.DeepEqual(body["versions"], []any{"LATEST", "1.21.1", "1.20.1"}) {
		t.Fatalf("vanilla: %v", body)
	}
	res, body = c.do("GET", "/api/v1/installers/forge/versions", nil, nil)
	expect(t, res, body, http.StatusOK, "")
	if !reflect.DeepEqual(body["versions"], []any{"1.21", "1.20.1"}) {
		t.Fatalf("forge: %v", body)
	}
	res, body = c.do("GET", "/api/v1/installers/fabric/versions", nil, nil)
	expect(t, res, body, http.StatusOK, "")
	if list, ok := body["versions"].([]any); !ok || len(list) != 0 {
		t.Fatalf("empty catalog must be an empty array: %v", body)
	}
	res, body = c.do("GET", "/api/v1/installers/forge/versions/1.20.1", nil, nil)
	expect(t, res, body, http.StatusOK, "")
	if !reflect.DeepEqual(body["loader_versions"], []any{"47.4.5", "47.3.0"}) || body["recommended"] != "47.3.0" {
		t.Fatalf("forge 1.20.1: %v", body)
	}

	for _, path := range []string{
		"/api/v1/installers/paper/versions",
		"/api/v1/installers/forge/versions/1.12.2",
		"/api/v1/installers/vanilla/versions/1.20.1",
		"/api/v1/installers/forge/versions/..%2F..",
	} {
		res, body = c.do("GET", path, nil, nil)
		expect(t, res, body, http.StatusNotFound, "not_found")
	}

	c.src.mu.Lock()
	c.src.down = true
	c.src.mu.Unlock()
	res, body = c.do("GET", "/api/v1/installers/neoforge/versions", nil, nil) // not cached yet
	expect(t, res, body, http.StatusServiceUnavailable, "installer_unavailable")
	res, body = c.do("GET", "/api/v1/installers/forge/versions", nil, nil) // cached
	expect(t, res, body, http.StatusOK, "")
}

func TestCreateModdedServerContract(t *testing.T) {
	c := newServer(t)
	c.setupAndLogin()

	req := validServer("Modded")
	req["loader"], req["version"] = "forge", "1.20.1"
	body := c.createAndWait(req)
	var res *http.Response
	if body["loader"] != "forge" || body["version"] != "1.20.1" || body["loader_version"] != "47.3.0" {
		t.Fatalf("created: %v", body)
	}

	req = validServer("Pinned")
	req["loader"], req["version"], req["loader_version"] = "forge", "1.20.1", "47.4.5"
	body = c.createAndWait(req)
	if body["loader_version"] != "47.4.5" {
		t.Fatalf("pinned: %v", body)
	}

	bad := []struct {
		field                          string
		loader, version, loaderVersion any
	}{
		{"loader", "paper", "1.21.4", nil},
		{"version", "forge", nil, nil}, // no "LATEST" default for modded servers
		{"loader_version", "forge", "1.20.1", "1.0.0"},
	}
	for _, b := range bad {
		req = validServer("Bad")
		req["loader"], req["version"], req["loader_version"] = b.loader, b.version, b.loaderVersion
		res, body = c.do("POST", "/api/v1/servers", req, nil)
		expect(t, res, body, http.StatusUnprocessableEntity, "validation_failed")
		if body["error"].(map[string]any)["field"] != b.field {
			t.Errorf("%s: %v", b.field, body)
		}
	}

	c.src.mu.Lock()
	c.src.down = true
	c.src.mu.Unlock()
	req = validServer("Down")
	req["loader"], req["version"] = "neoforge", "1.21.1"
	res, body = c.do("POST", "/api/v1/servers", req, nil)
	expect(t, res, body, http.StatusServiceUnavailable, "installer_unavailable")
}
