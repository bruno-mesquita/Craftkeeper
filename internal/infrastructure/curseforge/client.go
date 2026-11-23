// Package curseforge reads Minecraft modpacks from the CurseForge API over HTTPS. Every call
// takes the user's API key. Only metadata is read: the game image downloads the pack itself.
// Hosts are fixed: icons are only fetched from the CurseForge media CDN.
package curseforge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/bruno-mesquita/Craftkeeper/internal/domain"
)

// URLs are the API base and the prefix every icon URL must start with.
type URLs struct {
	API   string
	Media string
}

var Official = URLs{
	API:   "https://api.curseforge.com",
	Media: "https://media.forgecdn.net/",
}

const (
	userAgent     = "craftkeeper"
	fetchTimeout  = 15 * time.Second
	maxJSON       = 8 << 20
	maxIcon       = 256 << 10
	searchLimit   = 20
	filesPageSize = 50

	gameMinecraft  = "432"  // CurseForge game ID of Minecraft
	classModpacks  = "4471" // CurseForge class ID of Minecraft modpacks
	sortPopularity = "2"
)

type Client struct {
	http *http.Client
	urls URLs
}

func New(urls URLs) *Client {
	noRedirect := func(*http.Request, []*http.Request) error {
		return fmt.Errorf("%w: unexpected redirect", domain.ErrCurseForgeUnavailable)
	}
	return &Client{urls: urls, http: &http.Client{Timeout: fetchTimeout, CheckRedirect: noRedirect}}
}

// Check validates an API key with a request that needs one.
func (c *Client) Check(ctx context.Context, key string) error {
	var out struct {
		Data struct {
			ID int64 `json:"id"`
		} `json:"data"`
	}
	return c.getJSON(ctx, key, "/v1/games/"+gameMinecraft, &out)
}

// SearchModpacks finds Minecraft modpacks: most relevant first, or most popular first when
// query is empty.
func (c *Client) SearchModpacks(ctx context.Context, key, query string, offset int) (domain.ModSearch, error) {
	q := url.Values{
		"gameId":    {gameMinecraft},
		"classId":   {classModpacks},
		"index":     {strconv.Itoa(offset)},
		"pageSize":  {strconv.Itoa(searchLimit)},
		"sortOrder": {"desc"},
	}
	if query == "" {
		q.Set("sortField", sortPopularity)
	} else {
		q.Set("searchFilter", query)
	}
	var out struct {
		Data       []mod `json:"data"`
		Pagination struct {
			Index      int `json:"index"`
			TotalCount int `json:"totalCount"`
		} `json:"pagination"`
	}
	if err := c.getJSON(ctx, key, "/v1/mods/search?"+q.Encode(), &out); err != nil {
		return domain.ModSearch{}, err
	}
	res := domain.ModSearch{Hits: []domain.ModProject{}, Offset: out.Pagination.Index, Total: out.Pagination.TotalCount}
	for _, m := range out.Data {
		if p, ok := m.toDomain(c.urls.Media); ok {
			res.Hits = append(res.Hits, p)
		}
	}
	return res, nil
}

// Modpack returns one modpack. An unknown ID, or a project that is not a Minecraft modpack,
// is domain.ErrNotFound.
func (c *Client) Modpack(ctx context.Context, key, id string) (domain.ModProject, error) {
	if err := domain.ValidateModpackID(domain.ModpackSourceCurseForge, "project_id", id); err != nil {
		return domain.ModProject{}, err
	}
	var out struct {
		Data mod `json:"data"`
	}
	if err := c.getJSON(ctx, key, "/v1/mods/"+id, &out); err != nil {
		return domain.ModProject{}, err
	}
	p, ok := out.Data.toDomain(c.urls.Media)
	if !ok || strconv.FormatInt(out.Data.GameID, 10) != gameMinecraft || strconv.FormatInt(out.Data.ClassID, 10) != classModpacks {
		return domain.ModProject{}, domain.ErrNotFound
	}
	return p, nil
}

// Versions lists the pack files of a modpack that the panel can run, newest first. Server
// packs are left out: the game image installs from the regular pack file.
func (c *Client) Versions(ctx context.Context, key, id string) ([]domain.ModpackVersion, error) {
	if err := domain.ValidateModpackID(domain.ModpackSourceCurseForge, "project_id", id); err != nil {
		return nil, err
	}
	q := url.Values{"index": {"0"}, "pageSize": {strconv.Itoa(filesPageSize)}}
	var out struct {
		Data []file `json:"data"`
	}
	if err := c.getJSON(ctx, key, "/v1/mods/"+id+"/files?"+q.Encode(), &out); err != nil {
		return nil, err
	}
	list := []domain.ModpackVersion{}
	for _, f := range out.Data {
		if v, ok := f.toDomain(); ok {
			list = append(list, v)
		}
	}
	domain.SortModpackVersions(list)
	return list, nil
}

// Version returns one pack file of a modpack. An unknown file, or one the panel cannot run,
// is domain.ErrNotFound or domain.ErrModpackUnsupported.
func (c *Client) Version(ctx context.Context, key, id, fileID string) (domain.ModpackVersion, error) {
	if err := domain.ValidateModpackID(domain.ModpackSourceCurseForge, "project_id", id); err != nil {
		return domain.ModpackVersion{}, err
	}
	if err := domain.ValidateModpackID(domain.ModpackSourceCurseForge, "version_id", fileID); err != nil {
		return domain.ModpackVersion{}, err
	}
	var out struct {
		Data file `json:"data"`
	}
	if err := c.getJSON(ctx, key, "/v1/mods/"+id+"/files/"+fileID, &out); err != nil {
		return domain.ModpackVersion{}, err
	}
	v, ok := out.Data.toDomain()
	if !ok {
		return domain.ModpackVersion{}, domain.ErrModpackUnsupported
	}
	return v, nil
}

// Icon downloads a modpack logo from the media CDN and returns it with its sniffed content
// type. Only raster formats are accepted.
func (c *Client) Icon(ctx context.Context, iconURL string) ([]byte, string, error) {
	if !strings.HasPrefix(iconURL, c.urls.Media) {
		return nil, "", domain.ErrNotFound
	}
	resp, err := c.do(ctx, "", iconURL)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	img, err := io.ReadAll(io.LimitReader(resp.Body, maxIcon+1))
	if err != nil {
		return nil, "", fmt.Errorf("%w: icon: %v", domain.ErrCurseForgeUnavailable, err)
	}
	if len(img) > maxIcon {
		return nil, "", fmt.Errorf("%w: icon too large", domain.ErrNotFound)
	}
	switch ct := http.DetectContentType(img); ct {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return img, ct, nil
	}
	return nil, "", fmt.Errorf("%w: icon is not a raster image", domain.ErrNotFound)
}

func (c *Client) getJSON(ctx context.Context, key, path string, dst any) error {
	if key == "" {
		return domain.ErrCurseForgeNotConfigured
	}
	resp, err := c.do(ctx, key, c.urls.API+path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxJSON+1))
	if err != nil {
		return fmt.Errorf("%w: %v", domain.ErrCurseForgeUnavailable, err)
	}
	if len(body) > maxJSON {
		return fmt.Errorf("%w: response too large", domain.ErrCurseForgeUnavailable)
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return fmt.Errorf("%w: %v", domain.ErrCurseForgeUnavailable, err)
	}
	return nil
}

// do sends one GET; key "" sends none (media CDN). A non-2xx answer is closed and turned into
// an error: 404 is domain.ErrNotFound, 401/403 domain.ErrCurseForgeKeyInvalid.
func (c *Client) do(ctx context.Context, key, u string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", domain.ErrCurseForgeUnavailable, err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	if key != "" {
		req.Header.Set("x-api-key", key)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", domain.ErrCurseForgeUnavailable, err)
	}
	if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
		return resp, nil
	}
	resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotFound:
		return nil, domain.ErrNotFound
	case http.StatusUnauthorized, http.StatusForbidden:
		if key != "" {
			return nil, domain.ErrCurseForgeKeyInvalid
		}
	}
	return nil, fmt.Errorf("%w: GET %s: %s", domain.ErrCurseForgeUnavailable, req.URL.Path, resp.Status)
}

type mod struct {
	ID            int64   `json:"id"`
	GameID        int64   `json:"gameId"`
	ClassID       int64   `json:"classId"`
	Name          string  `json:"name"`
	Slug          string  `json:"slug"`
	Summary       string  `json:"summary"`
	DownloadCount float64 `json:"downloadCount"`
	Logo          *struct {
		ThumbnailURL string `json:"thumbnailUrl"`
	} `json:"logo"`
}

// toDomain keeps a mod only if its slug can select it in the game image.
func (m mod) toDomain(media string) (domain.ModProject, bool) {
	if m.ID <= 0 || !domain.ValidProjectSlug(m.Slug) {
		return domain.ModProject{}, false
	}
	icon := ""
	if m.Logo != nil && strings.HasPrefix(m.Logo.ThumbnailURL, media) {
		icon = m.Logo.ThumbnailURL
	}
	return domain.ModProject{
		ID: strconv.FormatInt(m.ID, 10), Slug: m.Slug, Title: m.Name, Description: m.Summary,
		IconURL: icon, ServerSide: "required", Downloads: int64(m.DownloadCount),
	}, true
}

type file struct {
	ID           int64     `json:"id"`
	DisplayName  string    `json:"displayName"`
	ReleaseType  int       `json:"releaseType"` // 1 release, 2 beta, 3 alpha
	FileDate     time.Time `json:"fileDate"`
	GameVersions []string  `json:"gameVersions"`
	IsServerPack bool      `json:"isServerPack"`
	IsAvailable  *bool     `json:"isAvailable"`
}

var releaseTypes = map[int]string{1: "release", 2: "beta", 3: "alpha"}

// toDomain keeps a regular, available pack file whose loader and Minecraft version the panel
// can run.
func (f file) toDomain() (domain.ModpackVersion, bool) {
	if f.ID <= 0 || f.IsServerPack || (f.IsAvailable != nil && !*f.IsAvailable) {
		return domain.ModpackVersion{}, false
	}
	loader, mc, ok := domain.ModpackGame(f.GameVersions)
	if !ok {
		return domain.ModpackVersion{}, false
	}
	typ, ok := releaseTypes[f.ReleaseType]
	if !ok {
		typ = "release"
	}
	return domain.ModpackVersion{
		ID: strconv.FormatInt(f.ID, 10), Name: f.DisplayName, Type: typ, Published: f.FileDate.UTC(),
		Loader: loader, GameVersion: mc,
	}, true
}
