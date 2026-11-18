package httpapi_test

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type upload struct{ name, content string }

// raw sends a request with an arbitrary body and returns the response with its full body.
func (c *client) raw(method, path string, body io.Reader, contentType string) (*http.Response, []byte) {
	c.t.Helper()
	req, _ := http.NewRequest(method, c.base+path, body)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if c.csrf != "" && method != http.MethodGet {
		req.Header.Set("X-CSRF-Token", c.csrf)
	}
	res, err := c.http.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res, b
}

func (c *client) upload(path string, files ...upload) (*http.Response, map[string]any) {
	c.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("note", "ignored")
	for _, f := range files {
		w, _ := mw.CreateFormFile("files", f.name)
		io.WriteString(w, f.content)
	}
	mw.Close()
	res, b := c.raw("POST", path, &buf, mw.FormDataContentType())
	var out map[string]any
	json.Unmarshal(b, &out)
	return res, out
}

func (c *client) createServer() (id, dir string) {
	c.t.Helper()
	id = c.createAndWait(validServer("Files"))["id"].(string)
	return id, filepath.Join(c.serversDir, id)
}

func TestFilesRequireAuthAndCSRF(t *testing.T) {
	c := newServer(t)
	res, body := c.do("GET", "/api/v1/servers/ABCDEFGHIJKLMNOPQRSTUVWXYZ/files", nil, nil)
	expect(t, res, body, http.StatusUnauthorized, "unauthenticated")

	c.setupAndLogin()
	id, _ := c.createServer()
	csrf := c.csrf
	c.csrf = ""
	res, body = c.upload("/api/v1/servers/"+id+"/files/upload", upload{"a.txt", "x"})
	expect(t, res, body, http.StatusForbidden, "csrf_invalid")
	res, body = c.do("DELETE", "/api/v1/servers/"+id+"/files?path=a.txt", nil, nil)
	expect(t, res, body, http.StatusForbidden, "csrf_invalid")
	c.csrf = csrf

	res, body = c.do("GET", "/api/v1/servers/ZZZZZZZZZZZZZZZZZZZZZZZZZZ/files", nil, nil)
	expect(t, res, body, http.StatusNotFound, "not_found")
	res, body = c.do("GET", "/api/v1/servers/not-an-id/files", nil, nil)
	expect(t, res, body, http.StatusNotFound, "not_found")
}

func TestFilesContract(t *testing.T) {
	c := newServer(t)
	c.setupAndLogin()
	id, dir := c.createServer()
	base := "/api/v1/servers/" + id + "/files"

	// Upload: several files per request, conflicts, overwrite, bad names.
	res, body := c.upload(base+"/upload?path=/", upload{"a.txt", "hello"}, upload{"b.jar", "jar"})
	expect(t, res, body, http.StatusCreated, "")
	if files := body["files"].([]any); len(files) != 2 || files[0].(map[string]any)["name"] != "a.txt" ||
		files[0].(map[string]any)["size"] != float64(5) {
		t.Fatalf("upload = %v", body)
	}
	res, body = c.upload(base+"/upload", upload{"a.txt", "again"})
	expect(t, res, body, http.StatusConflict, "file_exists")
	res, body = c.upload(base+"/upload?overwrite=true", upload{"a.txt", "hello world"})
	expect(t, res, body, http.StatusCreated, "")
	res, body = c.upload(base+"/upload?overwrite=maybe", upload{"a.txt", "x"})
	expect(t, res, body, http.StatusUnprocessableEntity, "validation_failed")
	res, body = c.upload(base+"/upload", upload{"..", "x"})
	expect(t, res, body, http.StatusUnprocessableEntity, "validation_failed")
	res, body = c.upload(base + "/upload")
	expect(t, res, body, http.StatusUnprocessableEntity, "validation_failed")
	res, b := c.raw("POST", base+"/upload", strings.NewReader("{}"), "application/json")
	if res.StatusCode != http.StatusBadRequest || !strings.Contains(string(b), "invalid_multipart") {
		t.Fatalf("non-multipart upload: %d %s", res.StatusCode, b)
	}

	// Directories, rename, listing.
	res, body = c.do("POST", base+"/directory", map[string]string{"path": "plugins"}, nil)
	expect(t, res, body, http.StatusNoContent, "")
	res, body = c.do("POST", base+"/directory", map[string]string{"path": "plugins"}, nil)
	expect(t, res, body, http.StatusConflict, "file_exists")
	res, body = c.do("POST", base+"/rename", map[string]string{"from": "b.jar", "to": "plugins/b.jar"}, nil)
	expect(t, res, body, http.StatusNoContent, "")
	res, body = c.do("POST", base+"/rename", map[string]string{"from": "plugins", "to": "plugins/inner"}, nil)
	expect(t, res, body, http.StatusUnprocessableEntity, "validation_failed")
	expectAudited(t, c, "file.upload", "file.mkdir", "file.rename")

	res, body = c.do("GET", base+"?path=/", nil, nil)
	expect(t, res, body, http.StatusOK, "")
	entries := body["entries"].([]any)
	first, second := entries[0].(map[string]any), entries[1].(map[string]any)
	if body["path"] != "" || body["truncated"] != false || len(entries) != 2 ||
		first["name"] != "plugins" || first["type"] != "dir" || second["name"] != "a.txt" || second["size"] != float64(11) ||
		second["modified_at"] == "" {
		t.Fatalf("list = %v", body)
	}
	res, body = c.do("GET", base+"?path=a.txt", nil, nil)
	expect(t, res, body, http.StatusConflict, "not_a_directory")
	res, body = c.do("GET", base+"?path=../..", nil, nil)
	expect(t, res, body, http.StatusUnprocessableEntity, "validation_failed")
	res, body = c.do("GET", base+"?path=missing", nil, nil)
	expect(t, res, body, http.StatusNotFound, "not_found")

	// Text editing with conflict detection.
	res, body = c.do("GET", base+"/content?path=a.txt", nil, nil)
	expect(t, res, body, http.StatusOK, "")
	v1, _ := body["version"].(string)
	if body["content"] != "hello world" || v1 == "" || body["path"] != "a.txt" {
		t.Fatalf("content = %v", body)
	}
	res, body = c.do("PUT", base+"/content", map[string]string{"path": "a.txt", "content": "edited", "version": v1}, nil)
	expect(t, res, body, http.StatusOK, "")
	if body["version"] == v1 || body["version"] == "" {
		t.Fatalf("new version = %v", body)
	}
	res, body = c.do("PUT", base+"/content", map[string]string{"path": "a.txt", "content": "stale", "version": v1}, nil)
	expect(t, res, body, http.StatusConflict, "file_changed")
	res, body = c.do("PUT", base+"/content", map[string]string{"path": "plugins", "content": "x"}, nil)
	expect(t, res, body, http.StatusConflict, "is_a_directory")
	res, body = c.do("PUT", base+"/content", map[string]string{"path": "big.txt", "content": strings.Repeat("x", 1<<20+1)}, nil)
	expect(t, res, body, http.StatusRequestEntityTooLarge, "file_too_large")
	os.WriteFile(filepath.Join(dir, "level.dat"), []byte{0, 1, 2}, 0o640)
	res, body = c.do("GET", base+"/content?path=level.dat", nil, nil)
	expect(t, res, body, http.StatusUnsupportedMediaType, "not_text")

	// Downloads: a file as-is, a directory as a zip.
	res, b = c.raw("GET", base+"/download?path=a.txt", nil, "")
	if res.StatusCode != http.StatusOK || string(b) != "edited" ||
		res.Header.Get("Content-Type") != "application/octet-stream" ||
		res.Header.Get("Content-Disposition") != `attachment; filename=a.txt` {
		t.Fatalf("download: %d %q %v", res.StatusCode, b, res.Header)
	}
	res, b = c.raw("GET", base+"/download?path=plugins", nil, "")
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "application/zip" ||
		res.Header.Get("Content-Disposition") != `attachment; filename=plugins.zip` {
		t.Fatalf("zip download: %d %v", res.StatusCode, res.Header)
	}
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil || len(zr.File) != 2 || zr.File[1].Name != "plugins/b.jar" {
		t.Fatalf("zip = %v, %v", zr, err)
	}

	// Extraction.
	var zbuf bytes.Buffer
	zw := zip.NewWriter(&zbuf)
	w, _ := zw.Create("pack/x.txt")
	io.WriteString(w, "x")
	zw.Close()
	os.WriteFile(filepath.Join(dir, "pack.zip"), zbuf.Bytes(), 0o640)
	res, body = c.do("POST", base+"/extract", map[string]any{"path": "pack.zip", "destination": "/"}, nil)
	expect(t, res, body, http.StatusOK, "")
	if body["entries"] != float64(1) {
		t.Fatalf("extract = %v", body)
	}
	res, body = c.do("POST", base+"/extract", map[string]any{"path": "pack.zip", "destination": ""}, nil)
	expect(t, res, body, http.StatusConflict, "file_exists")
	res, body = c.do("POST", base+"/extract", map[string]any{"path": "a.txt", "destination": ""}, nil)
	expect(t, res, body, http.StatusUnprocessableEntity, "invalid_archive")

	// Deletion.
	res, body = c.do("DELETE", base+"?path=plugins", nil, nil)
	expect(t, res, body, http.StatusNoContent, "")
	if _, err := os.Stat(filepath.Join(dir, "plugins")); !os.IsNotExist(err) {
		t.Fatalf("plugins still exists: %v", err)
	}
	res, body = c.do("DELETE", base+"?path=/", nil, nil)
	expect(t, res, body, http.StatusUnprocessableEntity, "validation_failed")
	res, body = c.do("DELETE", base+"?path=plugins", nil, nil)
	expect(t, res, body, http.StatusNotFound, "not_found")

	expectAudited(t, c, "file.write", "file.extract", "file.delete")
}

// expectAudited checks the dashboard's recent audit events, which only holds the latest few.
func expectAudited(t *testing.T, c *client, actions ...string) {
	t.Helper()
	recent := c.auditActions()
	for _, a := range actions {
		if !strings.Contains(recent, a) {
			t.Errorf("recent audit events lack %s: %s", a, recent)
		}
	}
}
