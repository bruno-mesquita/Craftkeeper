package curseforge

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bruno-mesquita/Craftkeeper/internal/domain"
)

const key = "$2a$10$test"

func newTest(t *testing.T, h http.HandlerFunc) (*Client, string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != userAgent {
			t.Errorf("User-Agent = %q", r.Header.Get("User-Agent"))
		}
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	return New(URLs{API: srv.URL, Media: srv.URL + "/media/"}), srv.URL
}

func TestSearchAndVersions(t *testing.T) {
	var base string
	c, base := newTest(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != key {
			http.Error(w, "no", http.StatusForbidden)
			return
		}
		switch r.URL.Path {
		case "/v1/mods/search":
			q := r.URL.Query()
			if q.Get("gameId") != "432" || q.Get("classId") != "4471" || q.Get("searchFilter") != "atm" || q.Get("index") != "20" {
				t.Errorf("search query = %v", q)
			}
			w.Write([]byte(`{"data":[
				{"id":1,"name":"ATM","slug":"atm","summary":"s","downloadCount":12.0,"logo":{"thumbnailUrl":"` + base + `/media/a.png"}},
				{"id":2,"name":"Bad slug","slug":"Bad Slug"},
				{"id":3,"name":"Elsewhere","slug":"elsewhere","logo":{"thumbnailUrl":"https://evil.example/x.png"}}
			],"pagination":{"index":20,"totalCount":99}}`))
		case "/v1/mods/1/files":
			w.Write([]byte(`{"data":[
				{"id":10,"displayName":"old","releaseType":1,"fileDate":"2026-01-01T00:00:00Z","gameVersions":["1.20.1","Forge"]},
				{"id":11,"displayName":"new","releaseType":2,"fileDate":"2026-02-01T00:00:00Z","gameVersions":["NeoForge","1.21.1"]},
				{"id":12,"displayName":"server","releaseType":1,"fileDate":"2026-03-01T00:00:00Z","gameVersions":["1.21.1","NeoForge"],"isServerPack":true},
				{"id":13,"displayName":"quilt","releaseType":1,"fileDate":"2026-03-01T00:00:00Z","gameVersions":["1.21.1","Quilt"]},
				{"id":14,"displayName":"gone","releaseType":1,"fileDate":"2026-03-01T00:00:00Z","gameVersions":["1.21.1","Forge"],"isAvailable":false}
			]}`))
		case "/v1/mods/1/files/13":
			w.Write([]byte(`{"data":{"id":13,"displayName":"quilt","gameVersions":["1.21.1","Quilt"]}}`))
		case "/v1/mods/1":
			w.Write([]byte(`{"data":{"id":1,"gameId":432,"classId":6,"name":"A mod","slug":"a-mod"}}`))
		default:
			http.NotFound(w, r)
		}
	})
	ctx := context.Background()

	res, err := c.SearchModpacks(ctx, key, "atm", 20)
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 99 || res.Offset != 20 || len(res.Hits) != 2 || res.Hits[0].ID != "1" || res.Hits[0].IconURL == "" ||
		res.Hits[1].IconURL != "" || res.Hits[0].Downloads != 12 {
		t.Fatalf("search = %+v", res)
	}

	list, err := c.Versions(ctx, key, "1")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != "11" || list[0].Loader != domain.LoaderNeoForge || list[0].Type != "beta" ||
		list[1].ID != "10" || list[1].GameVersion != "1.20.1" {
		t.Fatalf("versions = %+v", list)
	}
	if _, err := c.Version(ctx, key, "1", "13"); !errors.Is(err, domain.ErrModpackUnsupported) {
		t.Fatalf("quilt file: %v", err)
	}
	if _, err := c.Modpack(ctx, key, "1"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("a mod, not a modpack: %v", err)
	}
	if _, err := c.Modpack(ctx, key, "../1"); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("bad id: %v", err)
	}
	if _, err := c.SearchModpacks(ctx, "wrong", "", 0); !errors.Is(err, domain.ErrCurseForgeKeyInvalid) {
		t.Fatalf("wrong key: %v", err)
	}
	if _, err := c.SearchModpacks(ctx, "", "", 0); !errors.Is(err, domain.ErrCurseForgeNotConfigured) {
		t.Fatalf("no key: %v", err)
	}
}

func TestErrorsAndIcon(t *testing.T) {
	var png1 bytes.Buffer
	_ = png.Encode(&png1, image.NewRGBA(image.Rect(0, 0, 1, 1)))
	c, base := newTest(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/games/432":
			http.Error(w, "boom", http.StatusInternalServerError)
		case "/media/ok.png":
			if r.Header.Get("x-api-key") != "" {
				t.Error("the key was sent to the media CDN")
			}
			w.Write(png1.Bytes())
		case "/media/page.png":
			w.Write([]byte("<svg xmlns='http://www.w3.org/2000/svg'/>"))
		case "/media/redirect.png":
			http.Redirect(w, r, "https://evil.example/x.png", http.StatusFound)
		}
	})
	ctx := context.Background()
	if err := c.Check(ctx, key); !errors.Is(err, domain.ErrCurseForgeUnavailable) {
		t.Fatalf("5xx: %v", err)
	}
	if img, ct, err := c.Icon(ctx, base+"/media/ok.png"); err != nil || ct != "image/png" || len(img) == 0 {
		t.Fatalf("icon: %v %q", err, ct)
	}
	for _, u := range []string{base + "/media/page.png", "https://evil.example/x.png"} {
		if _, _, err := c.Icon(ctx, u); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("icon %s: %v", u, err)
		}
	}
	if _, _, err := c.Icon(ctx, base+"/media/redirect.png"); err == nil {
		t.Error("redirect followed")
	}
}
