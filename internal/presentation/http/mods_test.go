package httpapi_test

import (
	"bytes"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bruno-mesquita/mesquita-panel/internal/infrastructure/modrinth"
)

// fakeModrinth serves the parts of the Modrinth API and CDN the panel uses.
type fakeModrinth struct {
	urls     modrinth.URLs
	base     string
	mu       sync.Mutex
	down     bool
	titles   map[string]string // project ID → title
	versions []fakeModVersion  // oldest first
}

type fakeModVersion struct {
	id, project, number string
	published           time.Time
	data                []byte
	deps                []map[string]string
}

func (v fakeModVersion) sum() string {
	s := sha512.Sum512(v.data)
	return hex.EncodeToString(s[:])
}

const (
	modProject = "PROJAAAA"
	modDep     = "DEPBBBBB"
)

func newFakeModrinth(t *testing.T) *fakeModrinth {
	day := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	f := &fakeModrinth{
		titles: map[string]string{modProject: "Project A", modDep: "Dependency B"},
		versions: []fakeModVersion{
			{id: "VERAAAA1", project: modProject, number: "1.0", published: day, data: []byte("jar a 1.0"),
				deps: []map[string]string{{"project_id": modDep, "dependency_type": "required"}}},
			{id: "VERBBBB1", project: modDep, number: "2.0", published: day, data: []byte("jar b 2.0")},
			{id: "VERAAAA2", project: modProject, number: "1.1", published: day.Add(48 * time.Hour), data: []byte("jar a 1.1"),
				deps: []map[string]string{{"project_id": modDep, "dependency_type": "required"}}},
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	f.base = srv.URL
	f.urls = modrinth.URLs{API: srv.URL + "/v2", CDN: srv.URL + "/cdn/"}
	return f
}

func (f *fakeModrinth) setDown(down bool) {
	f.mu.Lock()
	f.down = down
	f.mu.Unlock()
}

func (f *fakeModrinth) versionJSON(v fakeModVersion) map[string]any {
	deps := []map[string]string{}
	deps = append(deps, v.deps...)
	return map[string]any{
		"id": v.id, "project_id": v.project, "name": v.number, "version_number": v.number, "version_type": "release",
		"loaders": []string{"forge"}, "game_versions": []string{"1.20.1"}, "date_published": v.published.Format(time.RFC3339),
		"files": []map[string]any{{
			"hashes": map[string]string{"sha512": v.sum()}, "url": f.base + "/cdn/" + v.id + ".jar",
			"filename": strings.ToLower(v.project) + "-" + v.number + ".jar", "primary": true, "size": len(v.data),
		}},
		"dependencies": deps,
	}
}

func (f *fakeModrinth) projectJSON(id string) map[string]any {
	return map[string]any{"id": id, "project_id": id, "slug": strings.ToLower(id), "title": f.titles[id], "description": "d",
		"icon_url": f.base + "/cdn/icon-" + id + ".png", "server_side": "required", "downloads": 10}
}

func (f *fakeModrinth) newest(project string) fakeModVersion {
	var out fakeModVersion
	for _, v := range f.versions {
		if v.project == project {
			out = v
		}
	}
	return out
}

func (f *fakeModrinth) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		http.Error(w, "down", http.StatusBadGateway)
		return
	}
	reply := func(v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	p := r.URL.Path
	switch {
	case p == "/v2/search":
		reply(map[string]any{"hits": []any{f.projectJSON(modProject), f.projectJSON(modDep)}, "offset": 0, "total_hits": 2})
	case p == "/v2/projects":
		var ids []string
		_ = json.Unmarshal([]byte(r.URL.Query().Get("ids")), &ids)
		out := []any{}
		for _, id := range ids {
			if _, ok := f.titles[id]; ok {
				out = append(out, f.projectJSON(id))
			}
		}
		reply(out)
	case strings.HasPrefix(p, "/v2/project/") && strings.HasSuffix(p, "/version"):
		id := strings.TrimSuffix(strings.TrimPrefix(p, "/v2/project/"), "/version")
		if _, ok := f.titles[id]; !ok {
			http.NotFound(w, r)
			return
		}
		out := []any{}
		for _, v := range slices.Backward(f.versions) {
			if v.project == id {
				out = append(out, f.versionJSON(v))
			}
		}
		reply(out)
	case strings.HasPrefix(p, "/v2/version/"):
		for _, v := range f.versions {
			if v.id == strings.TrimPrefix(p, "/v2/version/") {
				reply(f.versionJSON(v))
				return
			}
		}
		http.NotFound(w, r)
	case p == "/v2/version_files" || p == "/v2/version_files/update":
		var req struct {
			Hashes []string `json:"hashes"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		out := map[string]any{}
		for _, v := range f.versions {
			if slices.Contains(req.Hashes, v.sum()) {
				if p == "/v2/version_files" {
					out[v.sum()] = f.versionJSON(v)
				} else {
					out[v.sum()] = f.versionJSON(f.newest(v.project))
				}
			}
		}
		reply(out)
	case strings.HasPrefix(p, "/cdn/icon-"):
		var buf bytes.Buffer
		_ = png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 1, 1)))
		w.Write(buf.Bytes())
	case strings.HasPrefix(p, "/cdn/"):
		for _, v := range f.versions {
			if p == "/cdn/"+v.id+".jar" {
				w.Write(v.data)
				return
			}
		}
		http.NotFound(w, r)
	default:
		http.NotFound(w, r)
	}
}

func modFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, _ := os.ReadDir(filepath.Join(dir, "mods"))
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func TestModsContract(t *testing.T) {
	c := newServer(t)
	c.setupAndLogin()

	vanilla, _ := c.createServer()
	res, body := c.do("GET", "/api/v1/servers/"+vanilla+"/mods", nil, nil)
	expect(t, res, body, http.StatusConflict, "mods_unsupported")

	req := validServer("Modded")
	req["loader"], req["version"] = "forge", "1.20.1"
	id := c.createAndWait(req)["id"].(string)
	dir := filepath.Join(c.serversDir, id)
	base := "/api/v1/servers/" + id + "/mods"

	res, body = c.do("GET", base, nil, nil)
	expect(t, res, body, http.StatusOK, "")
	if mods := body["mods"].([]any); len(mods) != 0 || body["catalog_unavailable"] != false {
		t.Fatalf("empty list = %v", body)
	}

	res, body = c.do("GET", base+"/search?q=proj", nil, nil)
	expect(t, res, body, http.StatusOK, "")
	if hits := body["hits"].([]any); len(hits) != 2 || hits[0].(map[string]any)["id"] != modProject || hits[0].(map[string]any)["has_icon"] != true {
		t.Fatalf("search = %v", body)
	}
	res, body = c.do("GET", base+"/projects/"+modProject+"/versions", nil, nil)
	expect(t, res, body, http.StatusOK, "")
	if vs := body["versions"].([]any); len(vs) != 2 || vs[0].(map[string]any)["id"] != "VERAAAA2" {
		t.Fatalf("versions = %v", body)
	}
	res, body = c.do("GET", base+"/projects/bad/versions", nil, nil)
	expect(t, res, body, http.StatusUnprocessableEntity, "validation_failed")

	// The old version, with its required dependency.
	res, body = c.do("POST", base, map[string]string{"project_id": modProject, "version_id": "VERAAAA1"}, nil)
	expect(t, res, body, http.StatusOK, "")
	if got := modFiles(t, dir); !slices.Equal(got, []string{"depbbbbb-2.0.jar", "projaaaa-1.0.jar"}) {
		t.Fatalf("mods dir = %v (body %v)", got, body)
	}
	res, body = c.do("POST", base, map[string]string{"project_id": modProject}, nil)
	expect(t, res, body, http.StatusUnprocessableEntity, "validation_failed")

	res, body = c.do("GET", base, nil, nil)
	expect(t, res, body, http.StatusOK, "")
	mods := body["mods"].([]any)
	if len(mods) != 2 {
		t.Fatalf("list = %v", body)
	}
	a := mods[1].(map[string]any)
	if a["file"] != "projaaaa-1.0.jar" || a["enabled"] != true || a["project"].(map[string]any)["title"] != "Project A" ||
		a["version"].(map[string]any)["id"] != "VERAAAA1" || a["update"].(map[string]any)["id"] != "VERAAAA2" {
		t.Fatalf("mod a = %v", a)
	}
	if b := mods[0].(map[string]any); b["update"] != nil {
		t.Fatalf("dependency has no update: %v", b)
	}

	res, body = c.do("PATCH", base+"/files/projaaaa-1.0.jar", map[string]bool{"enabled": false}, nil)
	expect(t, res, body, http.StatusOK, "")
	if body["file"] != "projaaaa-1.0.jar.disabled" {
		t.Fatalf("disable = %v", body)
	}
	res, body = c.do("PATCH", base+"/files/server.properties", map[string]bool{"enabled": false}, nil)
	expect(t, res, body, http.StatusUnprocessableEntity, "validation_failed")

	// Update all: a backup first, the disabled mod stays disabled.
	res, body = c.do("POST", base+"/update", map[string]any{"backup": true}, nil)
	expect(t, res, body, http.StatusOK, "")
	if u := body["updated"].([]any); len(u) != 1 || u[0].(map[string]any)["to"] != "projaaaa-1.1.jar.disabled" {
		t.Fatalf("update = %v", body)
	}
	if got := modFiles(t, dir); !slices.Equal(got, []string{"depbbbbb-2.0.jar", "projaaaa-1.1.jar.disabled"}) {
		t.Fatalf("mods dir after update = %v", got)
	}
	res, body = c.do("GET", "/api/v1/servers/"+id+"/backups", nil, nil)
	if list := body["backups"].([]any); len(list) != 1 {
		t.Fatalf("update made no backup: %v", body)
	}

	res, body = c.do("DELETE", base+"/files/depbbbbb-2.0.jar", nil, nil)
	expect(t, res, body, http.StatusNoContent, "")
	res, body = c.do("DELETE", base+"/files/depbbbbb-2.0.jar", nil, nil)
	expect(t, res, body, http.StatusNotFound, "not_found")

	res, _ = c.do("GET", "/api/v1/mods/icons/"+modProject, nil, nil)
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "image/png" {
		t.Fatalf("icon: %d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}

	c.mr.setDown(true)
	res, body = c.do("GET", base, nil, nil)
	expect(t, res, body, http.StatusOK, "")
	if body["catalog_unavailable"] != true || len(body["mods"].([]any)) != 1 {
		t.Fatalf("degraded list = %v", body)
	}
	res, body = c.do("POST", base, map[string]string{"project_id": modDep}, nil)
	expect(t, res, body, http.StatusServiceUnavailable, "modrinth_unavailable")

	expectAudited(t, c, "mod.install", "mod.disable", "mod.update", "mod.remove")
}
