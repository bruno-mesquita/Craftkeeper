// Package mojang looks up Minecraft Java accounts at Mojang over HTTPS: the UUID behind a
// name, and the face of an account's skin. Hosts are fixed; nothing from a response is
// fetched unless it points at the official texture host.
package mojang

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/bruno-mesquita/mesquita-panel/internal/domain"
)

// URLs are the endpoint prefixes; a name, UUID or texture hash is appended.
type URLs struct {
	Profiles string // name → {"id","name"}
	Sessions string // UUID → profile with the textures property
	Textures string // texture hash → skin PNG
}

var Official = URLs{
	Profiles: "https://api.mojang.com/users/profiles/minecraft/",
	Sessions: "https://sessionserver.mojang.com/session/minecraft/profile/",
	Textures: "https://textures.minecraft.net/texture/",
}

const (
	fetchTimeout = 15 * time.Second
	maxJSON      = 16 << 10
	maxSkin      = 64 << 10
	userAgent    = "mesquita-panel"
	textureHost  = "textures.minecraft.net"
)

var textureHashRE = regexp.MustCompile(`^[0-9a-f]{1,128}$`)

type Profiles struct {
	client *http.Client
	urls   URLs
}

func New(urls URLs) *Profiles {
	return &Profiles{client: &http.Client{Timeout: fetchTimeout}, urls: urls}
}

// Lookup returns the account's UUID and the name as Mojang spells it.
func (p *Profiles) Lookup(ctx context.Context, name string) (domain.Player, error) {
	if err := domain.ValidatePlayerName(name); err != nil {
		return domain.Player{}, err
	}
	body, err := p.get(ctx, p.urls.Profiles+url.PathEscape(name), maxJSON)
	if err != nil {
		return domain.Player{}, err
	}
	var out struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return domain.Player{}, fmt.Errorf("%w: profile: %v", domain.ErrAccountLookupUnavailable, err)
	}
	uuid, err := domain.ParseUUID(out.ID)
	if err != nil || domain.ValidatePlayerName(out.Name) != nil {
		return domain.Player{}, fmt.Errorf("%w: profile has an invalid id or name", domain.ErrAccountLookupUnavailable)
	}
	return domain.Player{UUID: uuid, Name: out.Name}, nil
}

// Face returns the 8×8 face of the account's skin, hat layer included, as a PNG.
// An account without a custom skin is domain.ErrNotFound.
func (p *Profiles) Face(ctx context.Context, uuid string) ([]byte, error) {
	uuid, err := domain.ParseUUID(uuid)
	if err != nil {
		return nil, err
	}
	body, err := p.get(ctx, p.urls.Sessions+strings.ReplaceAll(uuid, "-", ""), maxJSON)
	if err != nil {
		return nil, notFound(err)
	}
	hash, err := skinHash(body)
	if err != nil {
		return nil, err
	}
	skin, err := p.get(ctx, p.urls.Textures+hash, maxSkin)
	if err != nil {
		return nil, notFound(err)
	}
	return face(skin)
}

// skinHash extracts the texture hash of the skin from a session profile, accepting only
// URLs on the official texture host.
func skinHash(profile []byte) (string, error) {
	var prof struct {
		Properties []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(profile, &prof); err != nil {
		return "", fmt.Errorf("%w: session profile: %v", domain.ErrAccountLookupUnavailable, err)
	}
	for _, prop := range prof.Properties {
		if prop.Name != "textures" {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(prop.Value)
		if err != nil {
			return "", fmt.Errorf("%w: textures: %v", domain.ErrAccountLookupUnavailable, err)
		}
		var tex struct {
			Textures struct {
				Skin struct {
					URL string `json:"url"`
				} `json:"SKIN"`
			} `json:"textures"`
		}
		if err := json.Unmarshal(raw, &tex); err != nil {
			return "", fmt.Errorf("%w: textures: %v", domain.ErrAccountLookupUnavailable, err)
		}
		if tex.Textures.Skin.URL == "" {
			return "", domain.ErrNotFound
		}
		u, err := url.Parse(tex.Textures.Skin.URL)
		if err != nil {
			return "", fmt.Errorf("%w: skin URL: %v", domain.ErrAccountLookupUnavailable, err)
		}
		// Mojang serves http:// texture URLs; only the hash is kept, fetched over HTTPS.
		hash, ok := strings.CutPrefix(u.Path, "/texture/")
		if (u.Scheme != "http" && u.Scheme != "https") || u.Host != textureHost || !ok || !textureHashRE.MatchString(hash) {
			return "", fmt.Errorf("%w: unexpected skin URL", domain.ErrAccountLookupUnavailable)
		}
		return hash, nil
	}
	return "", domain.ErrNotFound
}

// face crops the head front (8,8) and lays the hat (40,8) over it. Legacy 64×32 skins often
// fill the hat area with an opaque color; like the game, a fully opaque hat is ignored.
func face(skin []byte) ([]byte, error) {
	cfg, err := png.DecodeConfig(bytes.NewReader(skin))
	if err != nil || cfg.Width != 64 || (cfg.Height != 64 && cfg.Height != 32) {
		return nil, fmt.Errorf("%w: skin is not a 64×64 or 64×32 PNG", domain.ErrAccountLookupUnavailable)
	}
	src, err := png.Decode(bytes.NewReader(skin))
	if err != nil {
		return nil, fmt.Errorf("%w: skin: %v", domain.ErrAccountLookupUnavailable, err)
	}
	out := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	draw.Draw(out, out.Bounds(), src, image.Pt(8, 8), draw.Src)
	if cfg.Height == 64 || !opaque(src, image.Rect(40, 8, 48, 16)) {
		draw.Draw(out, out.Bounds(), src, image.Pt(40, 8), draw.Over)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, out); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func opaque(img image.Image, r image.Rectangle) bool {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if _, _, _, a := img.At(x, y).RGBA(); a != 0xffff {
				return false
			}
		}
	}
	return true
}

// notFound turns "no such account" into a plain not-found for the avatar path.
func notFound(err error) error {
	if errors.Is(err, domain.ErrPlayerNotFound) {
		return domain.ErrNotFound
	}
	return err
}

func (p *Profiles) get(ctx context.Context, u string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", domain.ErrAccountLookupUnavailable, err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNoContent, http.StatusNotFound:
		return nil, domain.ErrPlayerNotFound
	default:
		return nil, fmt.Errorf("%w: status %d", domain.ErrAccountLookupUnavailable, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", domain.ErrAccountLookupUnavailable, err)
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("%w: response larger than %d bytes", domain.ErrAccountLookupUnavailable, limit)
	}
	return body, nil
}
