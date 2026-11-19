package modrinth

import (
	"context"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bruno-mesquita/mesquita-panel/internal/domain"
)

func newTest(t *testing.T, h http.HandlerFunc) (*Client, string) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return New(URLs{API: srv.URL + "/v2", CDN: srv.URL + "/cdn/"}), srv.URL
}

func sum(b []byte) string {
	s := sha512.Sum512(b)
	return hex.EncodeToString(s[:])
}

func TestDownloadVerifies(t *testing.T) {
	data := []byte("a mod jar")
	c, base := newTest(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != userAgent {
			t.Errorf("User-Agent = %q", r.Header.Get("User-Agent"))
		}
		switch r.URL.Path {
		case "/cdn/ok.jar":
			w.Write(data)
		case "/cdn/redirect.jar":
			http.Redirect(w, r, "https://example.com/evil.jar", http.StatusFound)
		}
	})
	file := domain.ModFile{URL: base + "/cdn/ok.jar", Name: "ok.jar", SHA512: sum(data), Size: int64(len(data))}
	read := func(f domain.ModFile) error {
		rc, err := c.Download(context.Background(), f)
		if err != nil {
			return err
		}
		defer rc.Close()
		_, err = io.ReadAll(rc)
		return err
	}
	if err := read(file); err != nil {
		t.Fatalf("valid download: %v", err)
	}

	bad := file
	bad.SHA512 = sum([]byte("other"))
	if err := read(bad); !errors.Is(err, domain.ErrModrinthUnavailable) {
		t.Fatalf("hash mismatch: %v", err)
	}
	short := file
	short.Size = 3 // the server sends more than announced
	if err := read(short); !errors.Is(err, domain.ErrModrinthUnavailable) {
		t.Fatalf("oversize: %v", err)
	}
	outside := file
	outside.URL = "https://example.com/cdn/ok.jar"
	if err := read(outside); !errors.Is(err, domain.ErrModrinthUnavailable) {
		t.Fatalf("host outside the CDN: %v", err)
	}
	redirect := file
	redirect.URL = base + "/cdn/redirect.jar"
	if err := read(redirect); !errors.Is(err, domain.ErrModrinthUnavailable) {
		t.Fatalf("redirect away from the CDN: %v", err)
	}
}

func TestVersionsAreValidated(t *testing.T) {
	var base string
	c, b := newTest(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		h := strings.Repeat("ab", 64)
		io.WriteString(w, `[
		  {"id":"VERSION1","project_id":"PROJECT1","version_number":"1.0","version_type":"release",
		   "loaders":["fabric"],"game_versions":["1.21.1"],"date_published":"2026-01-02T03:04:05Z",
		   "files":[{"hashes":{"sha512":"`+strings.Repeat("cd", 64)+`"},"url":"`+base+`/cdn/extra.jar","filename":"extra.jar","primary":false,"size":5},
		            {"hashes":{"sha512":"`+strings.ToUpper(h)+`"},"url":"`+base+`/cdn/main.jar","filename":"main.jar","primary":true,"size":7}],
		   "dependencies":[{"project_id":"DEPPROJ1","dependency_type":"required"},{"project_id":"../x","dependency_type":"required"},{"dependency_type":"optional"}]},
		  {"id":"VERSION2","project_id":"PROJECT1","files":[{"hashes":{"sha512":"`+h+`"},"url":"https://evil.example/x.jar","filename":"x.jar","size":7}]},
		  {"id":"bad id","project_id":"PROJECT1","files":[]}
		]`)
	})
	base = b
	list, err := c.ProjectVersions(context.Background(), "PROJECT1", "fabric", "1.21.1")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("versions = %+v", list)
	}
	v := list[0]
	if v.File.Name != "main.jar" || v.File.SHA512 != strings.Repeat("ab", 64) || v.File.Size != 7 || !v.Supports("fabric", "1.21.1") {
		t.Fatalf("version = %+v", v)
	}
	if len(v.Dependencies) != 1 || v.Dependencies[0].ProjectID != "DEPPROJ1" {
		t.Fatalf("dependencies = %+v", v.Dependencies)
	}
	if _, err := c.ProjectVersions(context.Background(), "../etc", "fabric", "1.21.1"); err == nil {
		t.Fatal("invalid project ID accepted")
	}
}

func TestErrors(t *testing.T) {
	c, base := newTest(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/version/MISSING1":
			http.NotFound(w, r)
		case "/cdn/icon.svg":
			io.WriteString(w, `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
		default:
			http.Error(w, "boom", http.StatusInternalServerError)
		}
	})
	if _, err := c.Version(context.Background(), "MISSING1"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("404: %v", err)
	}
	if _, err := c.Version(context.Background(), "BROKEN11"); !errors.Is(err, domain.ErrModrinthUnavailable) {
		t.Fatalf("500: %v", err)
	}
	if _, _, err := c.Icon(context.Background(), base+"/cdn/icon.svg"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("SVG icon accepted: %v", err)
	}
	if _, _, err := c.Icon(context.Background(), "https://example.com/icon.png"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("icon outside the CDN: %v", err)
	}
}
