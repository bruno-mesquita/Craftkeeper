package httpapi_test

import (
	"archive/zip"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestChangeVersionContract(t *testing.T) {
	c := newServer(t)
	c.setupAndLogin()
	id, _ := c.createServer()
	base := "/api/v1/servers/" + id

	res, body := c.do("POST", base+"/version", map[string]string{"loader": "forge", "version": "1.20.1"}, nil)
	expect(t, res, body, http.StatusOK, "")
	if body["loader"] != "forge" || body["version"] != "1.20.1" || body["loader_version"] != "47.3.0" || body["status"] != "stopped" {
		t.Fatalf("server = %v", body)
	}
	cid := body["container_id"].(string)
	if env := strings.Join(c.rt.envs[cid], " "); !strings.Contains(env, "FORGE_VERSION=47.3.0") {
		t.Fatalf("env = %s", env)
	}
	res, body = c.do("GET", base+"/backups", nil, nil)
	expect(t, res, body, http.StatusOK, "")
	if list := body["backups"].([]any); len(list) != 1 || list[0].(map[string]any)["status"] != "ready" {
		t.Fatalf("backups = %v", body)
	}

	res, body = c.do("POST", base+"/version", map[string]string{"loader": "forge", "version": "LATEST"}, nil)
	expect(t, res, body, http.StatusUnprocessableEntity, "validation_failed")
	res, body = c.do("POST", base+"/start", nil, nil)
	expect(t, res, body, http.StatusOK, "")
	res, body = c.do("POST", base+"/version", map[string]string{"loader": "vanilla"}, nil)
	expect(t, res, body, http.StatusConflict, "server_running")
	expectAudited(t, c, "server.version_change", "backup.create")
}

func TestWorldContract(t *testing.T) {
	c := newServer(t)
	c.setupAndLogin()
	id, dir := c.createServer()
	base := "/api/v1/servers/" + id
	os.MkdirAll(filepath.Join(dir, "world"), 0o750)
	os.WriteFile(filepath.Join(dir, "world", "level.dat"), []byte("old"), 0o640)

	res, body := c.do("POST", base+"/world/reset", map[string]string{"seed": "12345"}, nil)
	expect(t, res, body, http.StatusNoContent, "")
	if _, err := os.Stat(filepath.Join(dir, "world")); !os.IsNotExist(err) {
		t.Fatalf("world kept: %v", err)
	}
	if p, _ := os.ReadFile(filepath.Join(dir, "server.properties")); string(p) != "level-seed=12345\n" {
		t.Fatalf("server.properties = %q", p)
	}

	// Import: the browser uploads the archive with the files API first; here it is written directly.
	writeWorldZip(t, filepath.Join(dir, "w.zip"), map[string]string{"Saved/level.dat": "new"})
	res, body = c.do("POST", base+"/world/import", map[string]string{"path": "w.zip"}, nil)
	expect(t, res, body, http.StatusNoContent, "")
	if b, _ := os.ReadFile(filepath.Join(dir, "world", "level.dat")); string(b) != "new" {
		t.Fatalf("imported level.dat = %q", b)
	}
	writeWorldZip(t, filepath.Join(dir, "x.zip"), map[string]string{"readme.txt": "no world"})
	res, body = c.do("POST", base+"/world/import", map[string]string{"path": "x.zip"}, nil)
	expect(t, res, body, http.StatusUnprocessableEntity, "invalid_world")
	res, body = c.do("POST", base+"/world/import", map[string]string{"path": "../x.zip"}, nil)
	expect(t, res, body, http.StatusUnprocessableEntity, "validation_failed")
	res, body = c.do("POST", base+"/world/reset", map[string]string{"seed": "a\nb"}, nil)
	expect(t, res, body, http.StatusUnprocessableEntity, "validation_failed")

	res, body = c.do("POST", base+"/start", nil, nil)
	expect(t, res, body, http.StatusOK, "")
	res, body = c.do("POST", base+"/world/reset", map[string]any{}, nil)
	expect(t, res, body, http.StatusConflict, "server_running")
	expectAudited(t, c, "world.reset", "world.import")
}

func TestOperatorsContract(t *testing.T) {
	c := newServer(t)
	c.setupAndLogin()
	id, dir := c.createServer()
	base := "/api/v1/servers/" + id + "/players"
	os.WriteFile(filepath.Join(dir, "server.properties"), []byte("online-mode=false\nop-permission-level=3\n"), 0o640)

	res, body := c.do("POST", base+"/ops", map[string]string{"name": "Notch"}, nil)
	expect(t, res, body, http.StatusNoContent, "")
	res, body = c.do("GET", base, nil, nil)
	expect(t, res, body, http.StatusOK, "")
	ops := body["ops"].([]any)
	if len(ops) != 1 || ops[0].(map[string]any)["name"] != "Notch" || ops[0].(map[string]any)["level"] != float64(3) ||
		ops[0].(map[string]any)["uuid"] != "b50ad385-829d-3141-a216-7e7d7539ba7f" {
		t.Fatalf("ops = %v", ops)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "ops.json")); !strings.Contains(string(b), `"bypassesPlayerLimit": false`) {
		t.Fatalf("ops.json = %s", b)
	}
	res, body = c.do("DELETE", base+"/ops", map[string]string{"name": "notch"}, nil)
	expect(t, res, body, http.StatusNoContent, "")
	res, body = c.do("GET", base, nil, nil)
	expect(t, res, body, http.StatusOK, "")
	if len(body["ops"].([]any)) != 0 {
		t.Fatalf("ops after deop = %v", body["ops"])
	}

	res, body = c.do("POST", "/api/v1/servers/"+id+"/start", nil, nil)
	expect(t, res, body, http.StatusOK, "")
	c.do("POST", base+"/ops", map[string]string{"name": "Notch"}, nil)
	c.do("DELETE", base+"/ops", map[string]string{"name": "Notch"}, nil)
	if got := strings.Join(c.console.sent(), "|"); got != "op Notch|deop Notch" {
		t.Fatalf("console = %q", got)
	}
	expectAudited(t, c, "player.op", "player.deop")
}

func writeWorldZip(t *testing.T, path string, files map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for n, content := range files {
		w, _ := zw.Create(n)
		w.Write([]byte(content))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}
