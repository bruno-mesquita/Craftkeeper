package httpapi_test

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bruno-mesquita/mesquita-panel/internal/domain"
)

// stubRuntime is an in-memory container runtime. Docker itself is covered by the adapter's
// tests against a fake daemon; here only the HTTP contract matters.
type stubRuntime struct {
	mu     sync.Mutex
	states map[string]*domain.ContainerState
	down   bool // simulate an unreachable daemon
}

func newStubRuntime() *stubRuntime { return &stubRuntime{states: map[string]*domain.ContainerState{}} }

func (s *stubRuntime) get(id, serverID string) (*domain.ContainerState, error) {
	if s.down {
		return nil, domain.ErrDockerUnavailable
	}
	c, ok := s.states[id]
	if !ok {
		return nil, domain.ErrContainerMissing
	}
	if c.ServerID != serverID {
		return nil, domain.ErrNotManaged
	}
	return c, nil
}

func (s *stubRuntime) set(state string) func(context.Context, string, string) error {
	return func(_ context.Context, id, serverID string) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		c, err := s.get(id, serverID)
		if err == nil {
			c.State = state
		}
		return err
	}
}

func (s *stubRuntime) EnsureImage(context.Context, string) error { return nil }

func (s *stubRuntime) Create(_ context.Context, spec domain.ContainerSpec) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := strings.Repeat("f", 63) + string(rune('0'+len(s.states)))
	s.states[id] = &domain.ContainerState{ID: id, ServerID: spec.ServerID, State: "created"}
	return id, nil
}

func (s *stubRuntime) Inspect(_ context.Context, id, serverID string) (domain.ContainerState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.get(id, serverID)
	if err != nil {
		return domain.ContainerState{}, err
	}
	return *c, nil
}

func (s *stubRuntime) States(context.Context) ([]domain.ContainerState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.down {
		return nil, domain.ErrDockerUnavailable
	}
	var out []domain.ContainerState
	for _, c := range s.states {
		out = append(out, *c)
	}
	return out, nil
}

func (s *stubRuntime) Start(ctx context.Context, id, sid string) error {
	return s.set("running")(ctx, id, sid)
}
func (s *stubRuntime) Stop(ctx context.Context, id, sid string) error {
	return s.set("exited")(ctx, id, sid)
}
func (s *stubRuntime) Restart(ctx context.Context, id, sid string) error {
	return s.set("running")(ctx, id, sid)
}
func (s *stubRuntime) Kill(ctx context.Context, id, sid string) error {
	return s.set("exited")(ctx, id, sid)
}

func (s *stubRuntime) Remove(_ context.Context, id, serverID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.get(id, serverID); err != nil {
		return err
	}
	delete(s.states, id)
	return nil
}

func (s *stubRuntime) UpdateLimits(_ context.Context, id, serverID string, _, _ int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.get(id, serverID)
	return err
}

func validServer(name string) map[string]any {
	return map[string]any{"name": name, "version": "1.21.4", "memory_mb": 2048, "cpu_cores": 1.5, "accept_eula": true}
}

// createAndWait creates a server, checks the 202 creating answer and waits until the
// background build ends. It returns the server as GET reports it then.
func (c *client) createAndWait(req map[string]any) map[string]any {
	c.t.Helper()
	res, body := c.do("POST", "/api/v1/servers", req, nil)
	expect(c.t, res, body, http.StatusAccepted, "")
	if body["status"] != "creating" || body["container_id"] != "" {
		c.t.Fatalf("create answer: %v", body)
	}
	id := body["id"].(string)
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		res, body = c.do("GET", "/api/v1/servers/"+id, nil, nil)
		expect(c.t, res, body, http.StatusOK, "")
		if body["status"] != "creating" {
			return body
		}
		if time.Now().After(deadline) {
			c.t.Fatalf("server %s still creating", id)
		}
	}
}

func TestServersRequireAuthAndCSRF(t *testing.T) {
	c := newServer(t)
	res, body := c.do("GET", "/api/v1/servers", nil, nil)
	expect(t, res, body, http.StatusUnauthorized, "unauthenticated")

	c.setupAndLogin()
	csrf := c.csrf
	c.csrf = ""
	res, body = c.do("POST", "/api/v1/servers", validServer("S"), nil)
	expect(t, res, body, http.StatusForbidden, "csrf_invalid")
	c.csrf = csrf

	res, body = c.do("POST", "/api/v1/servers", validServer("S"), map[string]string{"Sec-Fetch-Site": "cross-site"})
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-site create: %d", res.StatusCode)
	}
}

func TestServerLifecycleContract(t *testing.T) {
	c := newServer(t)
	c.setupAndLogin()

	res, body := c.do("GET", "/api/v1/servers", nil, nil)
	expect(t, res, body, http.StatusOK, "")
	if list, ok := body["servers"].([]any); !ok || len(list) != 0 {
		t.Fatalf("empty list must be an empty array: %v", body)
	}

	body = c.createAndWait(validServer("Survival"))
	id, _ := body["id"].(string)
	if !domain.ValidServerID(id) || body["name"] != "Survival" || body["game"] != "minecraft" || body["loader"] != "vanilla" ||
		body["version"] != "1.21.4" || body["status"] != "stopped" || body["failure"] != "" || body["memory_mb"] != float64(2048) ||
		body["cpu_cores"] != 1.5 || body["port"] != float64(25601) || body["description"] != "" || body["loader_version"] != "" {
		t.Fatalf("created: %v", body)
	}
	for _, k := range []string{"container_id", "created_at", "updated_at"} {
		if v, _ := body[k].(string); v == "" {
			t.Fatalf("missing %s: %v", k, body)
		}
	}

	for _, step := range []struct{ action, status string }{
		{"start", "running"}, {"stop", "stopped"}, {"restart", "running"}, {"kill", "stopped"},
	} {
		res, body = c.do("POST", "/api/v1/servers/"+id+"/"+step.action, nil, nil)
		expect(t, res, body, http.StatusOK, "")
		if body["status"] != step.status {
			t.Fatalf("%s: status %v", step.action, body["status"])
		}
	}

	res, body = c.do("GET", "/api/v1/servers/"+id, nil, nil)
	expect(t, res, body, http.StatusOK, "")

	res, body = c.do("PATCH", "/api/v1/servers/"+id, map[string]any{"name": "Renamed", "memory_mb": 4096, "cpu_cores": 2}, nil)
	expect(t, res, body, http.StatusOK, "")
	if body["name"] != "Renamed" || body["memory_mb"] != float64(4096) || body["cpu_cores"] != float64(2) {
		t.Fatalf("patched: %v", body)
	}

	c.do("POST", "/api/v1/servers/"+id+"/start", nil, nil)
	res, body = c.do("PATCH", "/api/v1/servers/"+id, map[string]any{"memory_mb": 1024}, nil)
	expect(t, res, body, http.StatusConflict, "server_running")

	res, body = c.do("GET", "/api/v1/dashboard", nil, nil)
	expect(t, res, body, http.StatusOK, "")
	if counts := body["servers"].(map[string]any); counts["total"] != float64(1) || counts["running"] != float64(1) || counts["stopped"] != float64(0) {
		t.Fatalf("dashboard servers: %v", counts)
	}

	res, body = c.do("DELETE", "/api/v1/servers/"+id+"?delete_files=maybe", nil, nil)
	expect(t, res, body, http.StatusUnprocessableEntity, "validation_failed")
	res, body = c.do("DELETE", "/api/v1/servers/"+id+"?delete_files=true", nil, nil)
	expect(t, res, body, http.StatusNoContent, "")
	res, body = c.do("GET", "/api/v1/servers/"+id, nil, nil)
	expect(t, res, body, http.StatusNotFound, "not_found")

	events := c.auditActions()
	for _, want := range []string{"server.create", "server.start", "server.update", "server.delete"} {
		if !strings.Contains(events, want) {
			t.Fatalf("audit log missing %s: %s", want, events)
		}
	}
}

// auditActions returns the recent audit actions from the dashboard, comma-separated.
func (c *client) auditActions() string {
	c.t.Helper()
	_, body := c.do("GET", "/api/v1/dashboard", nil, nil)
	var actions []string
	for _, e := range body["recent_events"].([]any) {
		actions = append(actions, e.(map[string]any)["action"].(string))
	}
	return strings.Join(actions, ",")
}

func TestServerErrors(t *testing.T) {
	c := newServer(t)
	c.setupAndLogin()

	for path, method := range map[string]string{
		"/api/v1/servers/not-an-id":                        "GET",
		"/api/v1/servers/AAAAAAAAAAAAAAAAAAAAAAAAAA":       "GET",
		"/api/v1/servers/AAAAAAAAAAAAAAAAAAAAAAAAAA/start": "POST",
		"/api/v1/servers/..%2F..%2Fetc/start":              "POST",
	} {
		res, body := c.do(method, path, nil, nil)
		expect(t, res, body, http.StatusNotFound, "")
	}

	bad := validServer("S")
	bad["accept_eula"] = false
	res, body := c.do("POST", "/api/v1/servers", bad, nil)
	expect(t, res, body, http.StatusUnprocessableEntity, "validation_failed")
	if body["error"].(map[string]any)["field"] != "accept_eula" {
		t.Fatalf("field: %v", body)
	}
	bad = validServer("S")
	bad["image"] = "alpine" // unknown fields are rejected, never passed through
	res, body = c.do("POST", "/api/v1/servers", bad, nil)
	expect(t, res, body, http.StatusBadRequest, "invalid_json")

	body = c.createAndWait(validServer("S"))
	id := body["id"].(string)
	res, body = c.do("POST", "/api/v1/servers", validServer("s"), nil)
	expect(t, res, body, http.StatusUnprocessableEntity, "validation_failed")

	// Container removed behind the panel's back.
	c.rt.mu.Lock()
	clear(c.rt.states)
	c.rt.mu.Unlock()
	res, body = c.do("GET", "/api/v1/servers/"+id, nil, nil)
	expect(t, res, body, http.StatusOK, "")
	if body["status"] != "missing" {
		t.Fatalf("status: %v", body["status"])
	}
	res, body = c.do("POST", "/api/v1/servers/"+id+"/start", nil, nil)
	expect(t, res, body, http.StatusConflict, "container_missing")

	c.rt.mu.Lock()
	c.rt.down = true
	c.rt.mu.Unlock()
	res, body = c.do("GET", "/api/v1/servers", nil, nil)
	expect(t, res, body, http.StatusOK, "")
	if s := body["servers"].([]any)[0].(map[string]any); s["status"] != "unknown" {
		t.Fatalf("docker down: %v", s)
	}
	res, body = c.do("POST", "/api/v1/servers/"+id+"/start", nil, nil)
	expect(t, res, body, http.StatusServiceUnavailable, "docker_unavailable")
}
