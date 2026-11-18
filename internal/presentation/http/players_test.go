package httpapi_test

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/bruno-mesquita/mesquita-panel/internal/application/servers"
	"github.com/bruno-mesquita/mesquita-panel/internal/domain"
)

// recordConsole remembers the commands sent to the "running" game.
type recordConsole struct {
	mu   sync.Mutex
	cmds []string
}

func (r *recordConsole) SendCommand(_ context.Context, _, cmd string, _ servers.Actor) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cmds = append(r.cmds, cmd)
	return nil
}

func (r *recordConsole) sent() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.cmds...)
}

// fakeAccounts stands in for Mojang: "Notch" exists, "Unreachable" fails, others are unknown.
type fakeAccounts struct{}

const notchUUID = "069a79f4-44e9-4726-a5be-fca90e38aaf5"

func (fakeAccounts) Lookup(_ context.Context, name string) (domain.Player, error) {
	switch strings.ToLower(name) {
	case "notch":
		return domain.Player{UUID: notchUUID, Name: "Notch"}, nil
	case "unreachable":
		return domain.Player{}, domain.ErrAccountLookupUnavailable
	}
	return domain.Player{}, domain.ErrPlayerNotFound
}

func (fakeAccounts) Face(_ context.Context, uuid string) ([]byte, error) {
	if uuid == notchUUID {
		return []byte("\x89PNG fake"), nil
	}
	return nil, domain.ErrNotFound
}

// fakePinger reports Notch online, or that the game does not answer while down is set.
type fakePinger struct {
	mu   sync.Mutex
	down bool
	port int
}

func (f *fakePinger) Status(_ context.Context, port int) (domain.OnlinePlayers, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.port = port
	if f.down {
		return domain.OnlinePlayers{}, errors.New("connection refused")
	}
	return domain.OnlinePlayers{Online: 2, Max: 20, Players: []domain.Player{{UUID: notchUUID, Name: "Notch"}}}, nil
}

func TestOnlinePlayers(t *testing.T) {
	c := newServer(t)
	res, body := c.do("GET", "/api/v1/servers/0123456789abcdef/players/online", nil, nil)
	expect(t, res, body, http.StatusUnauthorized, "unauthenticated")
	c.setupAndLogin()
	id, _ := c.createServer()
	path := "/api/v1/servers/" + id + "/players/online"

	res, body = c.do("GET", path, nil, nil)
	expect(t, res, body, http.StatusConflict, "server_not_running")

	res, body = c.do("POST", "/api/v1/servers/"+id+"/start", nil, nil)
	expect(t, res, body, http.StatusOK, "")
	res, body = c.do("GET", path, nil, nil)
	expect(t, res, body, http.StatusOK, "")
	p := body["players"].([]any)
	if body["online"] != float64(2) || body["max"] != float64(20) || len(p) != 1 ||
		p[0].(map[string]any)["uuid"] != notchUUID || p[0].(map[string]any)["name"] != "Notch" {
		t.Fatalf("online = %v", body)
	}
	c.pinger.mu.Lock()
	if c.pinger.port < 25601 || c.pinger.port > 25700 {
		t.Errorf("pinged port %d, want the server's game port", c.pinger.port)
	}
	c.pinger.down = true
	c.pinger.mu.Unlock()
	res, body = c.do("GET", path, nil, nil)
	expect(t, res, body, http.StatusServiceUnavailable, "game_unreachable")
}

func TestPlayersRequireAuthAndCSRF(t *testing.T) {
	c := newServer(t)
	res, body := c.do("GET", "/api/v1/servers/0123456789abcdef/players", nil, nil)
	expect(t, res, body, http.StatusUnauthorized, "unauthenticated")
	c.setupAndLogin()
	id, _ := c.createServer()
	res, body = c.do("PUT", "/api/v1/servers/"+id+"/players/whitelist", map[string]bool{"enabled": true}, map[string]string{"X-CSRF-Token": "wrong"})
	expect(t, res, body, http.StatusForbidden, "csrf_invalid")
	res, body = c.do("GET", "/api/v1/servers/not-an-id/players", nil, nil)
	expect(t, res, body, http.StatusNotFound, "not_found")
}

func TestPlayersContractStopped(t *testing.T) {
	c := newServer(t)
	c.setupAndLogin()
	id, dir := c.createServer()
	base := "/api/v1/servers/" + id + "/players"

	// Empty server: nothing on disk yet.
	res, body := c.do("GET", base, nil, nil)
	expect(t, res, body, http.StatusOK, "")
	wl := body["whitelist"].(map[string]any)
	if wl["enabled"] != false || len(wl["players"].([]any)) != 0 || len(body["banned_players"].([]any)) != 0 || len(body["banned_ips"].([]any)) != 0 {
		t.Fatalf("empty lists = %v", body)
	}

	// Online-mode (the default): the account is looked up at Mojang.
	res, body = c.do("POST", base+"/whitelist", map[string]string{"name": "notch"}, nil)
	expect(t, res, body, http.StatusNoContent, "")
	res, body = c.do("POST", base+"/whitelist", map[string]string{"name": "Nobody"}, nil)
	expect(t, res, body, http.StatusUnprocessableEntity, "validation_failed")
	res, body = c.do("POST", base+"/whitelist", map[string]string{"name": "Unreachable"}, nil)
	expect(t, res, body, http.StatusServiceUnavailable, "account_lookup_unavailable")
	res, body = c.do("GET", base, nil, nil)
	expect(t, res, body, http.StatusOK, "")
	if p := body["whitelist"].(map[string]any)["players"].([]any); len(p) != 1 ||
		p[0].(map[string]any)["uuid"] != notchUUID || p[0].(map[string]any)["name"] != "Notch" {
		t.Fatalf("online whitelist = %v", p)
	}
	res, body = c.do("DELETE", base+"/whitelist", map[string]string{"name": "Notch"}, nil)
	expect(t, res, body, http.StatusNoContent, "")

	// Offline-mode: the UUID is computed, no lookup.
	os.WriteFile(filepath.Join(dir, "server.properties"), []byte("#Minecraft server properties\nonline-mode=false\nwhite-list=false\n"), 0o640)

	res, body = c.do("POST", base+"/whitelist", map[string]string{"name": "Notch"}, nil)
	expect(t, res, body, http.StatusNoContent, "")
	res, body = c.do("POST", base+"/whitelist", map[string]string{"name": "bad name!"}, nil)
	expect(t, res, body, http.StatusUnprocessableEntity, "validation_failed")
	res, body = c.do("PUT", base+"/whitelist", map[string]bool{"enabled": true}, nil)
	expect(t, res, body, http.StatusNoContent, "")
	props, _ := os.ReadFile(filepath.Join(dir, "server.properties"))
	if string(props) != "#Minecraft server properties\nonline-mode=false\nwhite-list=true\nenforce-whitelist=true\n" {
		t.Fatalf("server.properties = %q", props)
	}

	res, body = c.do("POST", base+"/bans", map[string]string{"name": "Griefer", "reason": "griefing"}, nil)
	expect(t, res, body, http.StatusNoContent, "")
	res, body = c.do("POST", base+"/ip-bans", map[string]string{"ip": "203.0.113.7"}, nil)
	expect(t, res, body, http.StatusNoContent, "")
	res, body = c.do("POST", base+"/ip-bans", map[string]string{"ip": "999.1.1.1"}, nil)
	expect(t, res, body, http.StatusUnprocessableEntity, "validation_failed")

	res, body = c.do("GET", base, nil, nil)
	expect(t, res, body, http.StatusOK, "")
	wl = body["whitelist"].(map[string]any)
	p := wl["players"].([]any)[0].(map[string]any)
	if wl["enabled"] != true || p["name"] != "Notch" || p["uuid"] != "b50ad385-829d-3141-a216-7e7d7539ba7f" {
		t.Fatalf("whitelist = %v", wl)
	}
	ban := body["banned_players"].([]any)[0].(map[string]any)
	ip := body["banned_ips"].([]any)[0].(map[string]any)
	if ban["name"] != "Griefer" || ban["reason"] != "griefing" || ban["expires"] != "forever" || ip["ip"] != "203.0.113.7" {
		t.Fatalf("bans = %v", body)
	}

	// Removal is by name (case-insensitive) or IP, and idempotent.
	for _, step := range []struct{ path, key, val string }{
		{"/whitelist", "name", "notch"}, {"/bans", "name", "Griefer"}, {"/ip-bans", "ip", "203.0.113.7"}, {"/ip-bans", "ip", "203.0.113.7"},
	} {
		res, body = c.do("DELETE", base+step.path, map[string]string{step.key: step.val}, nil)
		expect(t, res, body, http.StatusNoContent, "")
	}
	res, body = c.do("GET", base, nil, nil)
	expect(t, res, body, http.StatusOK, "")
	if len(body["whitelist"].(map[string]any)["players"].([]any)) != 0 || len(body["banned_players"].([]any)) != 0 || len(body["banned_ips"].([]any)) != 0 {
		t.Fatalf("lists after removal = %v", body)
	}
	if got := c.console.sent(); len(got) != 0 {
		t.Fatalf("stopped server received console commands: %v", got)
	}
	expectAudited(t, c, "whitelist.add", "whitelist.toggle", "player.ban", "ip.ban", "whitelist.remove", "player.pardon", "ip.pardon")

	// A corrupt list is reported, not overwritten.
	os.WriteFile(filepath.Join(dir, "whitelist.json"), []byte("{oops"), 0o640)
	res, body = c.do("GET", base, nil, nil)
	expect(t, res, body, http.StatusUnprocessableEntity, "validation_failed")
	res, body = c.do("POST", base+"/whitelist", map[string]string{"name": "Notch"}, nil)
	expect(t, res, body, http.StatusUnprocessableEntity, "validation_failed")
	if b, _ := os.ReadFile(filepath.Join(dir, "whitelist.json")); string(b) != "{oops" {
		t.Fatalf("corrupt whitelist overwritten: %q", b)
	}
}

func TestPlayersContractRunning(t *testing.T) {
	c := newServer(t)
	c.setupAndLogin()
	id, dir := c.createServer()
	base := "/api/v1/servers/" + id + "/players"
	res, body := c.do("POST", "/api/v1/servers/"+id+"/start", nil, nil)
	expect(t, res, body, http.StatusOK, "")

	for _, step := range []struct {
		method, path string
		body         map[string]any
	}{
		{"PUT", "/whitelist", map[string]any{"enabled": true}},
		{"POST", "/whitelist", map[string]any{"name": "Notch"}},
		{"DELETE", "/whitelist", map[string]any{"name": "Notch"}},
		{"POST", "/bans", map[string]any{"name": "Griefer", "reason": "griefing"}},
		{"DELETE", "/bans", map[string]any{"name": "Griefer"}},
		{"POST", "/ip-bans", map[string]any{"ip": "203.0.113.7"}},
		{"DELETE", "/ip-bans", map[string]any{"ip": "203.0.113.7"}},
	} {
		res, body = c.do(step.method, base+step.path, step.body, nil)
		expect(t, res, body, http.StatusNoContent, "")
	}
	want := "whitelist on|whitelist add Notch|whitelist remove Notch|ban Griefer griefing|pardon Griefer|ban-ip 203.0.113.7|pardon-ip 203.0.113.7"
	if got := strings.Join(c.console.sent(), "|"); got != want {
		t.Fatalf("console commands = %q, want %q", got, want)
	}
	// The running game owns the files; the panel must not touch them.
	for _, f := range []string{"server.properties", "whitelist.json", "banned-players.json", "banned-ips.json"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
			t.Errorf("%s written while the server runs", f)
		}
	}
}

func TestPlayerAvatar(t *testing.T) {
	c := newServer(t)
	res, body := c.do("GET", "/api/v1/players/"+notchUUID+"/avatar", nil, nil)
	expect(t, res, body, http.StatusUnauthorized, "unauthenticated")
	c.setupAndLogin()
	res, _ = c.do("GET", "/api/v1/players/"+notchUUID+"/avatar", nil, nil)
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "image/png" || res.Header.Get("Cache-Control") != "private, max-age=86400" {
		t.Fatalf("avatar: %d %s %s", res.StatusCode, res.Header.Get("Content-Type"), res.Header.Get("Cache-Control"))
	}
	// Offline-mode UUIDs have no account: answered without a lookup.
	res, body = c.do("GET", "/api/v1/players/b50ad385-829d-3141-a216-7e7d7539ba7f/avatar", nil, nil)
	expect(t, res, body, http.StatusNotFound, "not_found")
	res, body = c.do("GET", "/api/v1/players/not-a-uuid/avatar", nil, nil)
	expect(t, res, body, http.StatusUnprocessableEntity, "validation_failed")
}
