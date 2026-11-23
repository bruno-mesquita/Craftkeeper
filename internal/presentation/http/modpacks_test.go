package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bruno-mesquita/Craftkeeper/internal/infrastructure/curseforge"
)

const cfKey = "$2a$10$good-key-1234"

// fakeCurseForge serves the parts of the CurseForge API the panel uses: one modpack (ID 123,
// slug cf-pack) with a Forge pack file (456) and a server pack (457) that must be hidden.
type fakeCurseForge struct {
	urls curseforge.URLs
}

func newFakeCurseForge(t *testing.T) *fakeCurseForge {
	f := &fakeCurseForge{}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	f.urls = curseforge.URLs{API: srv.URL, Media: srv.URL + "/media/"}
	return f
}

func (f *fakeCurseForge) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("x-api-key") != cfKey {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	reply := func(v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": v, "pagination": map[string]any{"index": 0, "totalCount": 1}})
	}
	pack := map[string]any{"id": 123, "gameId": 432, "classId": 4471, "name": "CF Pack", "slug": "cf-pack", "summary": "s",
		"downloadCount": 1e6, "logo": map[string]any{"thumbnailUrl": f.urls.Media + "logo.png"}}
	file := map[string]any{"id": 456, "displayName": "CF Pack 1.0", "releaseType": 1, "fileDate": "2026-01-02T00:00:00Z",
		"gameVersions": []string{"1.20.1", "Forge"}}
	serverPack := map[string]any{"id": 457, "displayName": "Server 1.0", "releaseType": 1, "fileDate": "2026-01-03T00:00:00Z",
		"gameVersions": []string{"1.20.1", "Forge"}, "isServerPack": true}
	switch r.URL.Path {
	case "/v1/games/432":
		reply(map[string]any{"id": 432})
	case "/v1/mods/search":
		reply([]any{pack})
	case "/v1/mods/123":
		reply(pack)
	case "/v1/mods/123/files":
		reply([]any{file, serverPack})
	case "/v1/mods/123/files/456":
		reply(file)
	default:
		http.NotFound(w, r)
	}
}

func TestModpacksContract(t *testing.T) {
	c := newServer(t)
	c.setupAndLogin()

	// Catalog search and versions.
	res, body := c.do("GET", "/api/v1/modpacks/search?source=modrinth&q=pack", nil, nil)
	expect(t, res, body, http.StatusOK, "")
	if hits := body["hits"].([]any); len(hits) == 0 || hits[0].(map[string]any)["id"] != modProject {
		t.Fatalf("modrinth search = %v", body)
	}
	res, body = c.do("GET", "/api/v1/modpacks/search?source=nope", nil, nil)
	expect(t, res, body, http.StatusUnprocessableEntity, "validation_failed")
	res, body = c.do("GET", "/api/v1/modpacks/search?source=curseforge", nil, nil)
	expect(t, res, body, http.StatusConflict, "curseforge_not_configured")
	res, body = c.do("GET", "/api/v1/modpacks/modrinth/"+modProject+"/versions", nil, nil)
	expect(t, res, body, http.StatusOK, "")
	versions := body["versions"].([]any)
	if len(versions) != 2 || versions[0].(map[string]any)["id"] != "VERAAAA2" ||
		versions[0].(map[string]any)["loader"] != "forge" || versions[0].(map[string]any)["game_version"] != "1.20.1" {
		t.Fatalf("modrinth versions = %v", body)
	}

	// A server created from a Modrinth pack: the game comes from the pack.
	req := map[string]any{"name": "Pack", "memory_mb": 4096, "cpu_cores": 2, "accept_eula": true,
		"modpack": map[string]string{"source": "modrinth", "project_id": modProject, "version_id": "VERAAAA1"}}
	withLoader := map[string]any{"name": "Bad", "memory_mb": 4096, "cpu_cores": 2, "accept_eula": true, "loader": "forge",
		"modpack": req["modpack"]}
	res, body = c.do("POST", "/api/v1/servers", withLoader, nil)
	expect(t, res, body, http.StatusUnprocessableEntity, "validation_failed")
	srv := c.createAndWait(req)
	pack, _ := srv["modpack"].(map[string]any)
	if srv["loader"] != "forge" || srv["version"] != "1.20.1" || srv["loader_version"] != "" ||
		pack["source"] != "modrinth" || pack["version_id"] != "VERAAAA1" || pack["name"] != "Project A" {
		t.Fatalf("server = %v", srv)
	}
	id := srv["id"].(string)
	base := "/api/v1/servers/" + id
	env := strings.Join(c.rt.envs[srv["container_id"].(string)], " ")
	for _, want := range []string{"TYPE=MODRINTH", "MODRINTH_MODPACK=" + modProject, "MODRINTH_VERSION=VERAAAA1", "VERSION=1.20.1"} {
		if !strings.Contains(env, want) {
			t.Fatalf("env %q lacks %s", env, want)
		}
	}
	if strings.Contains(env, "FORGE_VERSION") {
		t.Fatalf("env pins a loader build: %s", env)
	}

	// The pack owns the mods and the version.
	res, body = c.do("GET", base+"/mods", nil, nil)
	expect(t, res, body, http.StatusConflict, "modpack_server")
	res, body = c.do("POST", base+"/version", map[string]string{"loader": "forge", "version": "1.20.1"}, nil)
	expect(t, res, body, http.StatusConflict, "modpack_server")

	// Update to the next version: backup first, new container with the new version.
	res, body = c.do("GET", base+"/modpack/versions", nil, nil)
	expect(t, res, body, http.StatusOK, "")
	if body["modpack"].(map[string]any)["version_id"] != "VERAAAA1" || len(body["versions"].([]any)) != 2 {
		t.Fatalf("server versions = %v", body)
	}
	res, body = c.do("POST", base+"/modpack/version", map[string]string{"version_id": "VERAAAA1"}, nil)
	expect(t, res, body, http.StatusUnprocessableEntity, "validation_failed")
	res, body = c.do("POST", base+"/modpack/version", map[string]string{"version_id": "VERBBBB1"}, nil)
	expect(t, res, body, http.StatusNotFound, "not_found") // a version of another project
	res, body = c.do("POST", base+"/modpack/version", map[string]string{"version_id": "VERAAAA2"}, nil)
	expect(t, res, body, http.StatusOK, "")
	if body["modpack"].(map[string]any)["version_id"] != "VERAAAA2" || body["status"] != "stopped" {
		t.Fatalf("updated server = %v", body)
	}
	if env := strings.Join(c.rt.envs[body["container_id"].(string)], " "); !strings.Contains(env, "MODRINTH_VERSION=VERAAAA2") {
		t.Fatalf("env after update = %s", env)
	}
	res, body = c.do("GET", base+"/backups", nil, nil)
	expect(t, res, body, http.StatusOK, "")
	if list := body["backups"].([]any); len(list) != 1 {
		t.Fatalf("backups = %v", body)
	}
	res, body = c.do("POST", base+"/start", nil, nil)
	expect(t, res, body, http.StatusOK, "")
	res, body = c.do("POST", base+"/modpack/version", map[string]string{"version_id": "VERAAAA1"}, nil)
	expect(t, res, body, http.StatusConflict, "server_running")
	expectAudited(t, c, "modpack.update", "backup.create")

	// A server without a catalog pack has no pack versions.
	plain, _ := c.createServer()
	res, body = c.do("GET", "/api/v1/servers/"+plain+"/modpack/versions", nil, nil)
	expect(t, res, body, http.StatusNotFound, "not_found")
}

func TestCurseForgeContract(t *testing.T) {
	c := newServer(t)
	c.setupAndLogin()

	res, body := c.do("GET", "/api/v1/settings/curseforge", nil, nil)
	expect(t, res, body, http.StatusOK, "")
	if body["configured"] != false {
		t.Fatalf("status = %v", body)
	}
	csrf := c.csrf
	c.csrf = ""
	res, body = c.do("PUT", "/api/v1/settings/curseforge", map[string]string{"api_key": cfKey}, nil)
	expect(t, res, body, http.StatusForbidden, "csrf_invalid")
	c.csrf = csrf
	res, body = c.do("PUT", "/api/v1/settings/curseforge", map[string]string{"api_key": "has space"}, nil)
	expect(t, res, body, http.StatusUnprocessableEntity, "validation_failed")
	res, body = c.do("PUT", "/api/v1/settings/curseforge", map[string]string{"api_key": "wrong-key"}, nil)
	expect(t, res, body, http.StatusBadRequest, "curseforge_key_invalid")
	res, body = c.do("PUT", "/api/v1/settings/curseforge", map[string]string{"api_key": cfKey}, nil)
	expect(t, res, body, http.StatusOK, "")
	if body["configured"] != true || body["hint"] != "1234" {
		t.Fatalf("status after save = %v", body)
	}
	res, body = c.do("GET", "/api/v1/settings/curseforge", nil, nil)
	expect(t, res, body, http.StatusOK, "")
	if raw, _ := json.Marshal(body); strings.Contains(string(raw), "good-key") {
		t.Fatalf("the key leaked: %s", raw)
	}

	res, body = c.do("GET", "/api/v1/modpacks/search?source=curseforge", nil, nil)
	expect(t, res, body, http.StatusOK, "")
	if hits := body["hits"].([]any); len(hits) != 1 || hits[0].(map[string]any)["id"] != "123" || hits[0].(map[string]any)["has_icon"] != true {
		t.Fatalf("curseforge search = %v", body)
	}
	res, body = c.do("GET", "/api/v1/modpacks/curseforge/123/versions", nil, nil)
	expect(t, res, body, http.StatusOK, "")
	if v := body["versions"].([]any); len(v) != 1 || v[0].(map[string]any)["id"] != "456" { // server pack hidden
		t.Fatalf("curseforge versions = %v", body)
	}
	res, body = c.do("GET", "/api/v1/modpacks/curseforge/abc/versions", nil, nil)
	expect(t, res, body, http.StatusUnprocessableEntity, "validation_failed")

	srv := c.createAndWait(map[string]any{"name": "CF", "memory_mb": 4096, "cpu_cores": 2, "accept_eula": true,
		"modpack": map[string]string{"source": "curseforge", "project_id": "123", "version_id": "456"}})
	if srv["loader"] != "forge" || srv["version"] != "1.20.1" || srv["modpack"].(map[string]any)["slug"] != "cf-pack" {
		t.Fatalf("server = %v", srv)
	}
	env := strings.Join(c.rt.envs[srv["container_id"].(string)], " ")
	for _, want := range []string{"TYPE=AUTO_CURSEFORGE", "CF_API_KEY=" + cfKey, "CF_SLUG=cf-pack", "CF_FILE_ID=456", "CF_DOWNLOADS_REPO=/data/downloads"} {
		if !strings.Contains(env, want) {
			t.Fatalf("env %q lacks %s", env, want)
		}
	}

	res, _ = c.do("DELETE", "/api/v1/settings/curseforge", nil, nil)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("remove key: %d", res.StatusCode)
	}
	res, body = c.do("POST", "/api/v1/servers", map[string]any{"name": "CF2", "memory_mb": 4096, "cpu_cores": 2, "accept_eula": true,
		"modpack": map[string]string{"source": "curseforge", "project_id": "123", "version_id": "456"}}, nil)
	expect(t, res, body, http.StatusConflict, "curseforge_not_configured")
	expectAudited(t, c, "settings.curseforge_key")
}
