package httpapi_test

// Contract tests for the public HTTP API (docs/API.md). They exercise the real router,
// middleware, application services and SQLite store; only host metrics are stubbed.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	neturl "net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/bruno-mesquita/mesquita-panel/internal/application/auth"
	"github.com/bruno-mesquita/mesquita-panel/internal/application/backups"
	"github.com/bruno-mesquita/mesquita-panel/internal/application/dashboard"
	"github.com/bruno-mesquita/mesquita-panel/internal/application/files"
	"github.com/bruno-mesquita/mesquita-panel/internal/application/installers"
	"github.com/bruno-mesquita/mesquita-panel/internal/application/monitoring"
	"github.com/bruno-mesquita/mesquita-panel/internal/application/players"
	"github.com/bruno-mesquita/mesquita-panel/internal/application/servers"
	"github.com/bruno-mesquita/mesquita-panel/internal/domain"
	"github.com/bruno-mesquita/mesquita-panel/internal/infrastructure/docker"
	"github.com/bruno-mesquita/mesquita-panel/internal/infrastructure/filesystem"
	"github.com/bruno-mesquita/mesquita-panel/internal/infrastructure/password"
	"github.com/bruno-mesquita/mesquita-panel/internal/infrastructure/sqlite"
	httpapi "github.com/bruno-mesquita/mesquita-panel/internal/presentation/http"
	wspres "github.com/bruno-mesquita/mesquita-panel/internal/presentation/websocket"
	"github.com/bruno-mesquita/mesquita-panel/migrations"
)

const token = "SETUPTOKEN"

type stubCollector struct{}

func (stubCollector) Collect(context.Context) (domain.SystemStats, error) {
	return domain.SystemStats{
		CollectedAt: time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC),
		CPU:         domain.CPUStats{Cores: 12, UsagePercent: 7.5},
		Memory:      domain.MemoryStats{TotalBytes: 100, UsedBytes: 40, AvailableBytes: 60},
		Disk:        domain.DiskStats{Path: "/data", TotalBytes: 1000, UsedBytes: 250, FreeBytes: 750},
	}, nil
}

// stubContainers returns a fixed snapshot, or err when set.
type stubContainers struct {
	snap domain.ContainerSnapshot
	err  error
}

func (s stubContainers) CollectContainers(context.Context) (domain.ContainerSnapshot, error) {
	return s.snap, s.err
}

type client struct {
	t       *testing.T
	base    string
	http    *http.Client
	csrf    string
	rt      *stubRuntime
	src     *stubSource
	console *recordConsole
	pinger  *fakePinger

	serversDir string
}

func newServer(t *testing.T) *client {
	t.Helper()
	return newServerWith(t, stubContainers{snap: domain.ContainerSnapshot{CollectedAt: time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)}})
}

func newServerWith(t *testing.T, containers monitoring.ContainerCollector) *client {
	t.Helper()
	store, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "db.sqlite"), migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	audit := sqlite.NewAuditLog(store, log)
	authSvc, err := auth.NewService(sqlite.NewUsers(store), sqlite.NewSessions(store),
		password.NewArgon2id(password.Params{MemoryKiB: 64, Iterations: 1, Threads: 1}), audit, auth.Config{SetupToken: token})
	if err != nil {
		t.Fatal(err)
	}
	static := fstest.MapFS{
		"index.html":         {Data: []byte("<!doctype html><title>t</title>")},
		"assets/js/app.js":   {Data: []byte("// app")},
		"assets/css/app.css": {Data: []byte("body{}")},
	}
	rt, src, pc, pinger := newStubRuntime(), &stubSource{}, &recordConsole{}, &fakePinger{}
	installersSvc := installers.NewService(src, time.Hour)
	dataDir := t.TempDir()
	serversDir, backupsDir := filepath.Join(dataDir, "servers"), filepath.Join(dataDir, "backups")
	serversSvc := servers.NewService(sqlite.NewServers(store), rt, filesystem.NewServerDirs(serversDir, backupsDir, os.Getuid(), os.Getgid()),
		installersSvc, audit, servers.Config{FirstPort: 25601, LastPort: 25700, HostCPUs: 4, PortFree: func(int) bool { return true }})
	t.Cleanup(serversSvc.Close)
	serverFiles := filesystem.NewServerFiles(serversDir, os.Getuid(), os.Getgid())
	backupsSvc := backups.NewService(sqlite.NewBackups(store), filesystem.NewBackups(backupsDir, serverFiles),
		serversSvc, stubConsole{}, audit, log)
	t.Cleanup(backupsSvc.Close)
	api := httpapi.New(authSvc,
		monitoring.NewService(stubCollector{}, containers, time.Second),
		dashboard.NewService(docker.NewClient(filepath.Join(t.TempDir(), "no-docker.sock")), serversSvc, audit),
		serversSvc, installersSvc, files.NewService(serverFiles, serversSvc, audit), backupsSvc,
		players.NewService(serverFiles, serversSvc, pc, fakeAccounts{}, pinger, audit),
		wspres.NewHub(authSvc, nil, log), static, log)

	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	return &client{t: t, base: srv.URL, http: &http.Client{Jar: jar}, rt: rt, src: src, console: pc, pinger: pinger, serversDir: serversDir}
}

// do sends a same-origin request the way a browser would (Sec-Fetch-Site: same-origin).
func (c *client) do(method, path string, body any, headers map[string]string) (*http.Response, map[string]any) {
	c.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = strings.NewReader(string(b))
	}
	req, _ := http.NewRequest(method, c.base+path, r)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.csrf != "" && method != http.MethodGet {
		req.Header.Set("X-CSRF-Token", c.csrf)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := c.http.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	raw, _ := io.ReadAll(res.Body)
	if len(raw) > 0 && strings.HasPrefix(res.Header.Get("Content-Type"), "application/json") {
		if err := json.Unmarshal(raw, &out); err != nil {
			c.t.Fatalf("%s %s: invalid JSON %q", method, path, raw)
		}
	}
	return res, out
}

func expect(t *testing.T, res *http.Response, body map[string]any, status int, code string) {
	t.Helper()
	if res.StatusCode != status {
		t.Fatalf("%s %s: status %d, want %d (body %v)", res.Request.Method, res.Request.URL.Path, res.StatusCode, status, body)
	}
	if code != "" {
		e, _ := body["error"].(map[string]any)
		if e["code"] != code {
			t.Fatalf("%s %s: error code %v, want %q", res.Request.Method, res.Request.URL.Path, e["code"], code)
		}
	}
}

func (c *client) setupAndLogin() {
	c.t.Helper()
	res, body := c.do("POST", "/api/v1/setup", map[string]string{"token": token, "username": "admin", "password": "a-very-long-password"}, nil)
	expect(c.t, res, body, http.StatusCreated, "")
	res, body = c.do("POST", "/api/v1/auth/login", map[string]string{"username": "admin", "password": "a-very-long-password"}, nil)
	expect(c.t, res, body, http.StatusOK, "")
	c.csrf, _ = body["csrf_token"].(string)
}

func TestHealthAndStatic(t *testing.T) {
	c := newServer(t)
	res, body := c.do("GET", "/api/v1/health", nil, nil)
	expect(t, res, body, http.StatusOK, "")
	if body["status"] != "ok" {
		t.Fatalf("health body %v", body)
	}
	if res.Header.Get("Content-Security-Policy") == "" || res.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("missing security headers on API response")
	}

	res, _ = c.do("GET", "/", nil, nil)
	if res.StatusCode != http.StatusOK || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("index: %d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}
	res, _ = c.do("GET", "/assets/js/", nil, nil)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("directory listing must be disabled, got %d", res.StatusCode)
	}
	res, body = c.do("GET", "/api/v1/nope", nil, nil)
	expect(t, res, body, http.StatusNotFound, "not_found")
}

func TestSetupFlow(t *testing.T) {
	c := newServer(t)
	res, body := c.do("GET", "/api/v1/setup", nil, nil)
	expect(t, res, body, http.StatusOK, "")
	if body["required"] != true {
		t.Fatalf("setup should be required: %v", body)
	}

	res, body = c.do("POST", "/api/v1/setup", map[string]string{"token": "bad", "username": "admin", "password": "a-very-long-password"}, nil)
	expect(t, res, body, http.StatusForbidden, "setup_token_invalid")

	res, body = c.do("POST", "/api/v1/setup", map[string]string{"token": token, "username": "a", "password": "a-very-long-password"}, nil)
	expect(t, res, body, http.StatusUnprocessableEntity, "validation_failed")
	if e := body["error"].(map[string]any); e["field"] != "username" {
		t.Fatalf("validation field = %v", e["field"])
	}

	res, body = c.do("POST", "/api/v1/setup", map[string]any{"token": token, "extra": 1}, nil)
	expect(t, res, body, http.StatusBadRequest, "invalid_json")

	c.setupAndLogin()

	res, body = c.do("POST", "/api/v1/setup", map[string]string{"token": token, "username": "evil", "password": "a-very-long-password"}, nil)
	expect(t, res, body, http.StatusConflict, "setup_completed")
	res, body = c.do("GET", "/api/v1/setup", nil, nil)
	if body["required"] != false {
		t.Fatalf("setup should be completed: %v", body)
	}
}

func TestAuthSessionAndCSRF(t *testing.T) {
	c := newServer(t)

	res, body := c.do("GET", "/api/v1/auth/me", nil, nil)
	expect(t, res, body, http.StatusUnauthorized, "unauthenticated")

	c.setupAndLogin()
	if c.csrf == "" {
		t.Fatal("login must return csrf_token")
	}
	base, _ := neturl.Parse(c.base)
	if cookies := c.http.Jar.Cookies(base); len(cookies) != 1 || cookies[0].Name != "mesquita_session" {
		t.Fatalf("expected exactly the session cookie, got %v", cookies)
	}

	res, body = c.do("GET", "/api/v1/auth/me", nil, nil)
	expect(t, res, body, http.StatusOK, "")
	if user := body["user"].(map[string]any); user["username"] != "admin" || user["role"] != "admin" {
		t.Fatalf("me: %v", body)
	}
	if _, leaked := body["user"].(map[string]any)["password_hash"]; leaked {
		t.Fatal("password hash leaked")
	}

	// Mutating request without the CSRF header is rejected.
	csrf := c.csrf
	c.csrf = ""
	res, body = c.do("POST", "/api/v1/auth/logout", nil, nil)
	expect(t, res, body, http.StatusForbidden, "csrf_invalid")
	c.csrf = csrf

	res, _ = c.do("POST", "/api/v1/auth/logout", nil, nil)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("logout: %d", res.StatusCode)
	}
	res, body = c.do("GET", "/api/v1/auth/me", nil, nil)
	expect(t, res, body, http.StatusUnauthorized, "unauthenticated")
}

func TestLoginErrors(t *testing.T) {
	c := newServer(t)
	c.setupAndLogin()
	res, body := c.do("POST", "/api/v1/auth/login", map[string]string{"username": "admin", "password": "wrong-password-123"}, nil)
	expect(t, res, body, http.StatusUnauthorized, "invalid_credentials")
	for range 5 {
		c.do("POST", "/api/v1/auth/login", map[string]string{"username": "admin", "password": "wrong-password-123"}, nil)
	}
	res, body = c.do("POST", "/api/v1/auth/login", map[string]string{"username": "admin", "password": "a-very-long-password"}, nil)
	expect(t, res, body, http.StatusTooManyRequests, "rate_limited")
}

func TestCrossOriginMutationsAreRejected(t *testing.T) {
	c := newServer(t)
	res, _ := c.do("POST", "/api/v1/auth/login", map[string]string{"username": "admin", "password": "x"},
		map[string]string{"Sec-Fetch-Site": "cross-site"})
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-site login: %d, want 403", res.StatusCode)
	}
}

func TestOversizedBody(t *testing.T) {
	c := newServer(t)
	res, body := c.do("POST", "/api/v1/auth/login", map[string]string{"username": strings.Repeat("a", 70<<10)}, nil)
	expect(t, res, body, http.StatusRequestEntityTooLarge, "payload_too_large")
}

func TestStatsAndDashboard(t *testing.T) {
	c := newServer(t)
	res, body := c.do("GET", "/api/v1/system/stats", nil, nil)
	expect(t, res, body, http.StatusUnauthorized, "unauthenticated")

	c.setupAndLogin()
	res, body = c.do("GET", "/api/v1/system/stats", nil, nil)
	expect(t, res, body, http.StatusOK, "")
	cpu := body["cpu"].(map[string]any)
	if cpu["cores"] != float64(12) || cpu["usage_percent"] != 7.5 || body["collected_at"] != "2026-11-17T12:00:00Z" {
		t.Fatalf("stats: %v", body)
	}
	for _, k := range []string{"memory", "disk", "network"} {
		if _, ok := body[k].(map[string]any); !ok {
			t.Fatalf("stats missing %q", k)
		}
	}

	res, body = c.do("GET", "/api/v1/dashboard", nil, nil)
	expect(t, res, body, http.StatusOK, "")
	if body["docker"].(map[string]any)["reachable"] != false {
		t.Fatalf("docker should be unreachable in tests: %v", body)
	}
	events := body["recent_events"].([]any)
	if len(events) < 2 {
		t.Fatalf("expected setup and login audit events, got %v", events)
	}
	if first := events[0].(map[string]any); first["action"] != "auth.login" || first["success"] != true {
		t.Fatalf("most recent event: %v", first)
	}
}

func TestContainerStats(t *testing.T) {
	c := newServer(t)
	res, body := c.do("GET", "/api/v1/system/containers", nil, nil)
	expect(t, res, body, http.StatusUnauthorized, "unauthenticated")

	c.setupAndLogin()
	res, body = c.do("GET", "/api/v1/system/containers", nil, nil)
	expect(t, res, body, http.StatusOK, "")
	if list, ok := body["containers"].([]any); !ok || len(list) != 0 || body["collected_at"] != "2026-11-17T12:00:00Z" {
		t.Fatalf("empty snapshot must be an empty array: %v", body)
	}

	c = newServerWith(t, stubContainers{snap: domain.ContainerSnapshot{
		CollectedAt: time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC),
		Containers: []domain.ContainerStats{{
			ID: "abc", Name: "mesquita-srv", ServerID: "7", State: "running", CPUPercent: 12.5,
			MemoryUsedBytes: 100, MemoryLimitBytes: 200, NetRxBytes: 1, NetTxBytes: 2,
			NetRxBytesPerSecond: 3, NetTxBytesPerSecond: 4, BlockReadBytes: 5, BlockWriteBytes: 6,
		}},
	}})
	c.setupAndLogin()
	res, body = c.do("GET", "/api/v1/system/containers", nil, nil)
	expect(t, res, body, http.StatusOK, "")
	got, _ := json.Marshal(body["containers"])
	want := `[{"block_io":{"read_bytes":5,"write_bytes":6},"cpu_percent":12.5,"id":"abc",` +
		`"memory":{"limit_bytes":200,"used_bytes":100},"name":"mesquita-srv",` +
		`"network":{"rx_bytes":1,"rx_bytes_per_second":3,"tx_bytes":2,"tx_bytes_per_second":4},` +
		`"server_id":"7","state":"running"}]`
	if string(got) != want {
		t.Fatalf("containers:\n got %s\nwant %s", got, want)
	}

	c = newServerWith(t, stubContainers{err: fmt.Errorf("%w: dial unix: no such file", domain.ErrDockerUnavailable)})
	c.setupAndLogin()
	res, body = c.do("GET", "/api/v1/system/containers", nil, nil)
	expect(t, res, body, http.StatusServiceUnavailable, "docker_unavailable")
}

// The WebSocket endpoint requires a session; the handshake itself is covered by the
// presentation/websocket contract tests.
func TestWebSocketEndpointRequiresAuth(t *testing.T) {
	c := newServer(t)
	upgrade := map[string]string{
		"Connection": "Upgrade", "Upgrade": "websocket", "Sec-WebSocket-Version": "13",
		"Sec-WebSocket-Key": "dGhlIHNhbXBsZSBub25jZQ==", "Origin": c.base,
	}
	res, body := c.do("GET", "/api/v1/ws", nil, upgrade)
	expect(t, res, body, http.StatusUnauthorized, "unauthenticated")

	c.setupAndLogin()
	res, body = c.do("GET", "/api/v1/ws", nil, nil)
	expect(t, res, body, http.StatusBadRequest, "bad_handshake")
	upgrade["Origin"] = "http://evil.example"
	res, body = c.do("GET", "/api/v1/ws", nil, upgrade)
	expect(t, res, body, http.StatusForbidden, "origin_not_allowed")
}
