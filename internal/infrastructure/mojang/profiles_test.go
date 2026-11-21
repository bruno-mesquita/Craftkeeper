package mojang

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bruno-mesquita/Craftkeeper/internal/domain"
)

const notchHex = "069a79f444e94726a5befca90e38aaf5"

func skinPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 8; y < 16; y++ {
		for x := 8; x < 16; x++ {
			img.Set(x, y, color.NRGBA{R: 200, A: 255}) // face: red
		}
	}
	img.Set(40, 8, color.NRGBA{B: 200, A: 255}) // one hat pixel: blue
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func session(skinURL string) string {
	tex := `{"textures":{}}`
	if skinURL != "" {
		tex = `{"textures":{"SKIN":{"url":"` + skinURL + `"}}}`
	}
	return `{"id":"` + notchHex + `","name":"Notch","properties":[{"name":"textures","value":"` +
		base64.StdEncoding.EncodeToString([]byte(tex)) + `"}]}`
}

func newTest(t *testing.T, sessionBody string, skin []byte) *Profiles {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/profiles/Notch", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"id":"` + notchHex + `","name":"Notch"}`))
	})
	mux.HandleFunc("/profiles/Bad", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"id":"nope","name":"Bad"}`))
	})
	mux.HandleFunc("/profiles/Down", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	})
	mux.HandleFunc("/sessions/"+notchHex, func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(sessionBody))
	})
	mux.HandleFunc("/textures/abc123", func(w http.ResponseWriter, _ *http.Request) {
		w.Write(skin)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return New(URLs{Profiles: srv.URL + "/profiles/", Sessions: srv.URL + "/sessions/", Textures: srv.URL + "/textures/"})
}

func TestLookup(t *testing.T) {
	p := newTest(t, "", nil)
	ctx := context.Background()
	got, err := p.Lookup(ctx, "Notch")
	if err != nil || got.UUID != "069a79f4-44e9-4726-a5be-fca90e38aaf5" || got.Name != "Notch" {
		t.Fatalf("Lookup = %+v, %v", got, err)
	}
	if _, err := p.Lookup(ctx, "Nobody"); !errors.Is(err, domain.ErrPlayerNotFound) {
		t.Errorf("unknown name: %v", err)
	}
	for _, name := range []string{"Bad", "Down"} {
		if _, err := p.Lookup(ctx, name); !errors.Is(err, domain.ErrAccountLookupUnavailable) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := p.Lookup(ctx, "../x"); !errors.Is(err, domain.ErrInvalidInput) {
		t.Errorf("invalid name: %v", err)
	}
}

func TestFace(t *testing.T) {
	ctx := context.Background()
	p := newTest(t, session("http://textures.minecraft.net/texture/abc123"), skinPNG(t, 64, 64))
	out, err := p.Face(ctx, notchHex)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(out))
	if err != nil || img.Bounds().Dx() != 8 || img.Bounds().Dy() != 8 {
		t.Fatalf("face = %v, %v", img.Bounds(), err)
	}
	if r, _, b, _ := img.At(0, 0).RGBA(); b == 0 || r != 0 {
		t.Errorf("hat pixel not laid over the face: %v", img.At(0, 0))
	}
	if r, _, _, _ := img.At(1, 1).RGBA(); r == 0 {
		t.Errorf("face pixel missing: %v", img.At(1, 1))
	}

	// Fully opaque hat on a legacy skin is ignored.
	legacy := image.NewNRGBA(image.Rect(0, 0, 64, 32))
	for i := range legacy.Pix {
		legacy.Pix[i] = 255
	}
	var buf bytes.Buffer
	png.Encode(&buf, legacy)
	if _, err := newTest(t, session("https://textures.minecraft.net/texture/abc123"), buf.Bytes()).Face(ctx, notchHex); err != nil {
		t.Errorf("legacy skin: %v", err)
	}

	for name, tc := range map[string]struct {
		session string
		skin    []byte
		want    error
	}{
		"no skin":        {session(""), nil, domain.ErrNotFound},
		"foreign host":   {session("http://evil.example/texture/abc123"), nil, domain.ErrAccountLookupUnavailable},
		"path traversal": {session("http://textures.minecraft.net/texture/../x"), nil, domain.ErrAccountLookupUnavailable},
		"wrong size":     {session("http://textures.minecraft.net/texture/abc123"), skinPNG(t, 32, 32), domain.ErrAccountLookupUnavailable},
		"not png":        {session("http://textures.minecraft.net/texture/abc123"), []byte("GIF89a"), domain.ErrAccountLookupUnavailable},
	} {
		if _, err := newTest(t, tc.session, tc.skin).Face(ctx, notchHex); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", name, err, tc.want)
		}
	}
	if _, err := p.Face(ctx, "00000000000000000000000000000000"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("unknown account: %v", err)
	}
	if _, err := p.Face(ctx, strings.Repeat("z", 32)); !errors.Is(err, domain.ErrInvalidInput) {
		t.Errorf("invalid uuid: %v", err)
	}
}
