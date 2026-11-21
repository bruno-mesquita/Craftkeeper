// Package gameversions reads Minecraft releases and mod loader builds from their official
// sources over HTTPS. Every value it returns has passed the domain's version checks, because
// the versions end up in the game container's environment.
package gameversions

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bruno-mesquita/Craftkeeper/internal/domain"
)

// URLs are the endpoints of the version sources.
type URLs struct {
	Mojang          string // version manifest v2
	FabricGame      string
	FabricLoader    string
	ForgeMaven      string // maven-metadata.xml
	ForgePromotions string // promotions_slim.json
	NeoForgeMaven   string // maven-metadata.xml
}

// Official are the upstream sources. They are the same hosts the game image downloads from.
var Official = URLs{
	Mojang:          "https://piston-meta.mojang.com/mc/game/version_manifest_v2.json",
	FabricGame:      "https://meta.fabricmc.net/v2/versions/game",
	FabricLoader:    "https://meta.fabricmc.net/v2/versions/loader",
	ForgeMaven:      "https://maven.minecraftforge.net/net/minecraftforge/forge/maven-metadata.xml",
	ForgePromotions: "https://files.minecraftforge.net/net/minecraftforge/forge/promotions_slim.json",
	NeoForgeMaven:   "https://maven.neoforged.net/releases/net/neoforged/neoforge/maven-metadata.xml",
}

const (
	fetchTimeout = 15 * time.Second
	maxBody      = 8 << 20
	userAgent    = "craftkeeper"
)

type Sources struct {
	client *http.Client
	urls   URLs
}

func New(urls URLs) *Sources {
	return &Sources{client: &http.Client{Timeout: fetchTimeout}, urls: urls}
}

// Minecraft returns the game releases (no snapshots), newest first.
func (s *Sources) Minecraft(ctx context.Context) ([]string, error) {
	var manifest struct {
		Versions []struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		} `json:"versions"`
	}
	if err := s.getJSON(ctx, s.urls.Mojang, &manifest); err != nil {
		return nil, err
	}
	var out []string
	for _, v := range manifest.Versions {
		if v.Type == "release" && domain.IsMinecraftRelease(v.ID) {
			out = append(out, v.ID)
		}
	}
	return newestFirst(out), nil
}

// Fabric returns the stable loader builds for every stable game release. Fabric loaders are
// not tied to a game version, so every release gets the same list.
func (s *Sources) Fabric(ctx context.Context) (map[string]domain.LoaderVersions, error) {
	type entry struct {
		Version string `json:"version"`
		Stable  bool   `json:"stable"`
	}
	var games, loaders []entry
	if err := s.getJSON(ctx, s.urls.FabricGame, &games); err != nil {
		return nil, err
	}
	if err := s.getJSON(ctx, s.urls.FabricLoader, &loaders); err != nil {
		return nil, err
	}
	var builds []string
	for _, l := range loaders {
		if l.Stable && domain.ValidLoaderVersion(l.Version) && !strings.HasSuffix(l.Version, "-beta") {
			builds = append(builds, l.Version)
		}
	}
	out := map[string]domain.LoaderVersions{}
	if len(builds) == 0 {
		return out, nil
	}
	lv := domain.LoaderVersions{Versions: newestFirst(builds)}
	lv.Recommended = lv.Versions[0]
	for _, g := range games {
		if g.Stable && domain.IsMinecraftRelease(g.Version) {
			out[g.Version] = lv
		}
	}
	return out, nil
}

// Forge returns the Forge builds per game release. Maven versions are "<mc>-<forge>"; old
// entries with extra suffixes are skipped. The recommended build follows Forge's promotions.
func (s *Sources) Forge(ctx context.Context) (map[string]domain.LoaderVersions, error) {
	versions, err := s.mavenVersions(ctx, s.urls.ForgeMaven)
	if err != nil {
		return nil, err
	}
	var promos struct {
		Promos map[string]string `json:"promos"`
	}
	if err := s.getJSON(ctx, s.urls.ForgePromotions, &promos); err != nil {
		return nil, err
	}
	byMC := map[string][]string{}
	for _, v := range versions {
		mc, build, ok := strings.Cut(v, "-")
		if ok && domain.IsMinecraftRelease(mc) && domain.ValidLoaderVersion(build) {
			byMC[mc] = append(byMC[mc], build)
		}
	}
	return group(byMC, func(mc string, builds []string) string {
		for _, p := range []string{mc + "-recommended", mc + "-latest"} {
			if b := promos.Promos[p]; slices.Contains(builds, b) {
				return b
			}
		}
		return builds[0]
	}), nil
}

// NeoForge returns the NeoForge builds per game release. The release is encoded in the build
// number (see neoForgeMinecraft). The recommended build is the newest that is not a beta.
func (s *Sources) NeoForge(ctx context.Context) (map[string]domain.LoaderVersions, error) {
	versions, err := s.mavenVersions(ctx, s.urls.NeoForgeMaven)
	if err != nil {
		return nil, err
	}
	byMC := map[string][]string{}
	for _, v := range versions {
		if !domain.ValidLoaderVersion(v) {
			continue
		}
		if mc, ok := neoForgeMinecraft(v); ok {
			byMC[mc] = append(byMC[mc], v)
		}
	}
	return group(byMC, func(_ string, builds []string) string {
		for _, b := range builds {
			if !strings.HasSuffix(b, "-beta") {
				return b
			}
		}
		return builds[0]
	}), nil
}

// neoForgeMinecraft maps a NeoForge build to its game release. Up to 1.21.x the build is
// "<minor>.<patch>.<build>" for release 1.<minor>.<patch> (patch 0 is omitted: 21.0.x is
// 1.21). From 26.x on it is "<year>.<drop>.<patch>.<build>" for release <year>.<drop>[.<patch>].
func neoForgeMinecraft(build string) (string, bool) {
	parts := strings.Split(strings.TrimSuffix(build, "-beta"), ".")
	major, _ := strconv.Atoi(parts[0])
	var mc string
	switch {
	case major >= 20 && major < 26 && len(parts) == 3:
		mc = "1." + parts[0]
		if parts[1] != "0" {
			mc += "." + parts[1]
		}
	case major >= 26 && len(parts) == 4:
		mc = parts[0] + "." + parts[1]
		if parts[2] != "0" {
			mc += "." + parts[2]
		}
	default:
		return "", false
	}
	return mc, domain.IsMinecraftRelease(mc)
}

// group sorts every release's builds newest first and picks the recommended one.
func group(byMC map[string][]string, recommended func(mc string, builds []string) string) map[string]domain.LoaderVersions {
	out := make(map[string]domain.LoaderVersions, len(byMC))
	for mc, builds := range byMC {
		builds = newestFirst(builds)
		out[mc] = domain.LoaderVersions{Versions: builds, Recommended: recommended(mc, builds)}
	}
	return out
}

func newestFirst(v []string) []string {
	slices.SortFunc(v, func(a, b string) int { return domain.CompareVersions(b, a) })
	return slices.Compact(v)
}

func (s *Sources) mavenVersions(ctx context.Context, url string) ([]string, error) {
	body, err := s.get(ctx, url)
	if err != nil {
		return nil, err
	}
	var meta struct {
		Versions []string `xml:"versioning>versions>version"`
	}
	if err := xml.Unmarshal(body, &meta); err != nil {
		return nil, fmt.Errorf("%w: %s: %v", domain.ErrInstallerUnavailable, url, err)
	}
	return meta.Versions, nil
}

func (s *Sources) getJSON(ctx context.Context, url string, out any) error {
	body, err := s.get(ctx, url)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("%w: %s: %v", domain.ErrInstallerUnavailable, url, err)
	}
	return nil
}

func (s *Sources) get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", domain.ErrInstallerUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: %s: status %d", domain.ErrInstallerUnavailable, url, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", domain.ErrInstallerUnavailable, url, err)
	}
	if len(body) > maxBody {
		return nil, fmt.Errorf("%w: %s: response larger than %d bytes", domain.ErrInstallerUnavailable, url, maxBody)
	}
	return body, nil
}
