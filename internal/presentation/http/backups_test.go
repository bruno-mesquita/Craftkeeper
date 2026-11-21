package httpapi_test

import (
	"archive/zip"
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bruno-mesquita/Craftkeeper/internal/application/console"
	"github.com/bruno-mesquita/Craftkeeper/internal/domain"
)

// stubConsole stands for a game that never runs: the contract tests only back up stopped
// servers (online backups are covered by the backups service tests).
type stubConsole struct{}

func (stubConsole) Subscribe(context.Context, string, console.Sink) (func(), error) {
	return nil, domain.ErrServerNotRunning
}

func (stubConsole) Send(context.Context, string, string) error {
	return domain.ErrServerNotRunning
}

// waitBackup polls the list until backup id is no longer "creating" and returns it.
func (c *client) waitBackup(base, id string) map[string]any {
	c.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		res, body := c.do("GET", base, nil, nil)
		expect(c.t, res, body, http.StatusOK, "")
		for _, b := range body["backups"].([]any) {
			b := b.(map[string]any)
			if b["id"] == id && b["status"] != "creating" {
				return b
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	c.t.Fatalf("backup %s still creating", id)
	return nil
}

func TestBackupsRequireAuthAndCSRF(t *testing.T) {
	c := newServer(t)
	res, body := c.do("GET", "/api/v1/servers/ABCDEFGHIJKLMNOPQRSTUVWXYZ/backups", nil, nil)
	expect(t, res, body, http.StatusUnauthorized, "unauthenticated")

	c.setupAndLogin()
	id, _ := c.createServer()
	base := "/api/v1/servers/" + id + "/backups"
	csrf := c.csrf
	c.csrf = ""
	res, body = c.do("POST", base, nil, nil)
	expect(t, res, body, http.StatusForbidden, "csrf_invalid")
	res, body = c.do("PUT", base+"/schedule", map[string]int{"interval_hours": 1, "retain": 1}, nil)
	expect(t, res, body, http.StatusForbidden, "csrf_invalid")
	c.csrf = csrf

	res, body = c.do("GET", "/api/v1/servers/ZZZZZZZZZZZZZZZZZZZZZZZZZZ/backups", nil, nil)
	expect(t, res, body, http.StatusNotFound, "not_found")
	res, body = c.do("POST", base+"/not-an-id/restore", nil, nil)
	expect(t, res, body, http.StatusNotFound, "not_found")
	res, body = c.do("DELETE", base+"/ZZZZZZZZZZZZZZZZZZZZZZZZZZ", nil, nil)
	expect(t, res, body, http.StatusNotFound, "not_found")
}

func TestBackupsContract(t *testing.T) {
	c := newServer(t)
	c.setupAndLogin()
	id, dir := c.createServer()
	base := "/api/v1/servers/" + id + "/backups"
	if err := os.MkdirAll(filepath.Join(dir, "world"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "world", "level.dat"), []byte("original"), 0o640); err != nil {
		t.Fatal(err)
	}

	res, body := c.do("GET", base, nil, nil)
	expect(t, res, body, http.StatusOK, "")
	if list, ok := body["backups"].([]any); !ok || len(list) != 0 {
		t.Fatalf("empty list must be an empty array: %v", body)
	}

	// Create: accepted at once, then ready.
	res, body = c.do("POST", base, nil, nil)
	expect(t, res, body, http.StatusAccepted, "")
	backupID, _ := body["id"].(string)
	if !domain.ValidBackupID(backupID) || body["kind"] != "manual" || body["status"] != "creating" ||
		body["completed_at"] != nil || body["created_at"] == "" {
		t.Fatalf("created: %v", body)
	}
	b := c.waitBackup(base, backupID)
	if b["status"] != "ready" || b["failure"] != "" || b["size_bytes"].(float64) <= 0 || b["completed_at"] == nil {
		t.Fatalf("ready backup: %v", b)
	}

	// Download.
	res, raw := c.raw("GET", base+"/"+backupID+"/download", nil, "")
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "application/zip" ||
		!strings.Contains(res.Header.Get("Content-Disposition"), `filename=Files-`) {
		t.Fatalf("download: %d %v", res.StatusCode, res.Header)
	}
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}
	if !names["world/"] || !names["world/level.dat"] || len(names) != 2 {
		t.Fatalf("archive entries: %v", names)
	}

	// Restore needs a stopped server.
	res, body = c.do("POST", "/api/v1/servers/"+id+"/start", nil, nil)
	expect(t, res, body, http.StatusOK, "")
	res, body = c.do("POST", base+"/"+backupID+"/restore", nil, nil)
	expect(t, res, body, http.StatusConflict, "server_running")
	res, body = c.do("POST", "/api/v1/servers/"+id+"/stop", nil, nil)
	expect(t, res, body, http.StatusOK, "")

	if err := os.WriteFile(filepath.Join(dir, "world", "level.dat"), []byte("broken"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	res, body = c.do("POST", base+"/"+backupID+"/restore", nil, nil)
	expect(t, res, body, http.StatusNoContent, "")
	if got, err := os.ReadFile(filepath.Join(dir, "world", "level.dat")); err != nil || string(got) != "original" {
		t.Fatalf("restored level.dat = %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("files newer than the backup must be gone: %v", err)
	}
	old, _ := filepath.Glob(filepath.Join(c.serversDir, ".deleted", id+"-*-pre-restore", "new.txt"))
	if len(old) != 1 {
		t.Fatalf("previous files not kept in .deleted: %v", old)
	}

	res, body = c.do("POST", base+"/ZZZZZZZZZZZZZZZZZZZZZZZZZZ/restore", nil, nil)
	expect(t, res, body, http.StatusNotFound, "not_found")

	// Schedule.
	res, body = c.do("GET", base+"/schedule", nil, nil)
	expect(t, res, body, http.StatusOK, "")
	if body["interval_hours"] != float64(0) || body["retain"] != float64(0) || body["next_run_at"] != nil {
		t.Fatalf("default schedule: %v", body)
	}
	res, body = c.do("PUT", base+"/schedule", map[string]int{"interval_hours": 24, "retain": 3}, nil)
	expect(t, res, body, http.StatusOK, "")
	if body["interval_hours"] != float64(24) || body["retain"] != float64(3) || body["next_run_at"] == nil {
		t.Fatalf("schedule: %v", body)
	}
	res, body = c.do("PUT", base+"/schedule", map[string]int{"interval_hours": 721, "retain": 3}, nil)
	expect(t, res, body, http.StatusUnprocessableEntity, "validation_failed")
	res, body = c.do("PUT", base+"/schedule", map[string]int{"interval_hours": 24}, nil)
	expect(t, res, body, http.StatusUnprocessableEntity, "validation_failed")

	// Delete.
	res, body = c.do("DELETE", base+"/"+backupID, nil, nil)
	expect(t, res, body, http.StatusNoContent, "")
	res, body = c.do("DELETE", base+"/"+backupID, nil, nil)
	expect(t, res, body, http.StatusNotFound, "not_found")
	res, body = c.do("GET", base+"/"+backupID+"/download", nil, nil)
	expect(t, res, body, http.StatusNotFound, "not_found")
	expectAudited(t, c, "backup.create", "backup.restore", "backup.schedule_update", "backup.delete")

	// Backups of a deleted server go with its files.
	res, body = c.do("POST", base, nil, nil)
	expect(t, res, body, http.StatusAccepted, "")
	c.waitBackup(base, body["id"].(string))
	res, body = c.do("DELETE", "/api/v1/servers/"+id+"?delete_files=true", nil, nil)
	expect(t, res, body, http.StatusNoContent, "")
	if _, err := os.Stat(filepath.Join(filepath.Dir(c.serversDir), "backups", id)); !os.IsNotExist(err) {
		t.Fatalf("backups of a deleted server: %v", err)
	}
}
