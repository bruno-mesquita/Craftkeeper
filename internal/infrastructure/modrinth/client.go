// Package modrinth reads mods from the Modrinth API over HTTPS and downloads their files from
// the Modrinth CDN, verifying each file's SHA-512 while it streams. Hosts are fixed: nothing a
// response points at is fetched unless it is on the CDN.
package modrinth

import (
	"bytes"
	"context"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/bruno-mesquita/Craftkeeper/internal/domain"
)

// URLs are the API base (".../v2") and the prefix every file and icon URL must start with.
type URLs struct {
	API string
	CDN string
}

var Official = URLs{
	API: "https://api.modrinth.com/v2",
	CDN: "https://cdn.modrinth.com/",
}

// userAgent identifies the panel, as Modrinth asks of API clients.
const userAgent = "craftkeeper"

const (
	fetchTimeout = 15 * time.Second
	maxJSON      = 8 << 20
	maxIcon      = 256 << 10
	searchLimit  = 20
)

type Client struct {
	api      *http.Client // whole request bounded by fetchTimeout
	download *http.Client // bounded by the caller's context: a jar can take minutes
	urls     URLs
}

func New(urls URLs) *Client {
	c := &Client{urls: urls}
	sameCDN := func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 || !strings.HasPrefix(req.URL.String(), urls.CDN) {
			return errors.New("redirect away from the Modrinth CDN")
		}
		return nil
	}
	c.api = &http.Client{Timeout: fetchTimeout}
	c.download = &http.Client{
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			TLSHandshakeTimeout:   fetchTimeout,
			ResponseHeaderTimeout: fetchTimeout,
		},
		CheckRedirect: sameCDN,
	}
	return c
}

// Search finds server-side mods for loader on Minecraft mc: most relevant first, or most
// downloaded first when query is empty.
func (c *Client) Search(ctx context.Context, query, loader, mc string, offset int) (domain.ModSearch, error) {
	return c.search(ctx, query, offset, [][]string{
		{"project_type:mod"},
		{"categories:" + loader},
		{"versions:" + mc},
		{"server_side:required", "server_side:optional"},
	})
}

// SearchModpacks finds modpacks that run on a server with a loader the panel installs, ordered
// as Search orders mods.
func (c *Client) SearchModpacks(ctx context.Context, query string, offset int) (domain.ModSearch, error) {
	return c.search(ctx, query, offset, [][]string{
		{"project_type:modpack"},
		{"categories:" + domain.LoaderFabric, "categories:" + domain.LoaderForge, "categories:" + domain.LoaderNeoForge},
		{"server_side:required", "server_side:optional"},
	})
}

// search runs one search page; facets is Modrinth's AND-of-ORs filter.
func (c *Client) search(ctx context.Context, query string, offset int, facets [][]string) (domain.ModSearch, error) {
	index := "relevance"
	if query == "" {
		index = "downloads"
	}
	enc, _ := json.Marshal(facets)
	q := url.Values{
		"query":  {query},
		"facets": {string(enc)},
		"index":  {index},
		"offset": {strconv.Itoa(offset)},
		"limit":  {strconv.Itoa(searchLimit)},
	}
	var out struct {
		Hits []struct {
			ProjectID string `json:"project_id"`
			project
		} `json:"hits"`
		Offset    int `json:"offset"`
		TotalHits int `json:"total_hits"`
	}
	if err := c.getJSON(ctx, "/search?"+q.Encode(), &out); err != nil {
		return domain.ModSearch{}, err
	}
	res := domain.ModSearch{Hits: []domain.ModProject{}, Offset: out.Offset, Total: out.TotalHits}
	for _, h := range out.Hits {
		h.ID = h.ProjectID
		if p, ok := h.project.toDomain(c.urls.CDN); ok {
			res.Hits = append(res.Hits, p)
		}
	}
	return res, nil
}

// Projects returns the projects with the given IDs; unknown IDs are left out.
func (c *Client) Projects(ctx context.Context, ids []string) (map[string]domain.ModProject, error) {
	out := map[string]domain.ModProject{}
	if len(ids) == 0 {
		return out, nil
	}
	enc, _ := json.Marshal(ids)
	var list []project
	if err := c.getJSON(ctx, "/projects?"+url.Values{"ids": {string(enc)}}.Encode(), &list); err != nil {
		return nil, err
	}
	for _, p := range list {
		if dp, ok := p.toDomain(c.urls.CDN); ok {
			out[dp.ID] = dp
		}
	}
	return out, nil
}

// ProjectVersions lists a project's versions for loader on Minecraft mc, newest first; an
// empty loader or mc does not filter. An unknown project is domain.ErrNotFound.
func (c *Client) ProjectVersions(ctx context.Context, projectID, loader, mc string) ([]domain.ModVersion, error) {
	if err := domain.ValidateModrinthID("project_id", projectID); err != nil {
		return nil, err
	}
	q := url.Values{}
	if loader != "" {
		loaders, _ := json.Marshal([]string{loader})
		q.Set("loaders", string(loaders))
	}
	if mc != "" {
		games, _ := json.Marshal([]string{mc})
		q.Set("game_versions", string(games))
	}
	var list []version
	if err := c.getJSON(ctx, "/project/"+projectID+"/version?"+q.Encode(), &list); err != nil {
		return nil, err
	}
	out := []domain.ModVersion{}
	for _, v := range list {
		if dv, ok := v.toDomain(c.urls.CDN); ok {
			out = append(out, dv)
		}
	}
	return out, nil
}

// Version returns one version. An unknown version is domain.ErrNotFound.
func (c *Client) Version(ctx context.Context, id string) (domain.ModVersion, error) {
	if err := domain.ValidateModrinthID("version_id", id); err != nil {
		return domain.ModVersion{}, err
	}
	var v version
	if err := c.getJSON(ctx, "/version/"+id, &v); err != nil {
		return domain.ModVersion{}, err
	}
	dv, ok := v.toDomain(c.urls.CDN)
	if !ok {
		return domain.ModVersion{}, fmt.Errorf("%w: version %s has no valid file", domain.ErrModrinthUnavailable, id)
	}
	return dv, nil
}

// VersionsByHash identifies files by their SHA-512: hash → the version that published it.
func (c *Client) VersionsByHash(ctx context.Context, hashes []string) (map[string]domain.ModVersion, error) {
	return c.byHash(ctx, "/version_files", map[string]any{"hashes": hashes, "algorithm": "sha512"})
}

// LatestByHash returns, for each file hash, the newest version of its project for loader on
// Minecraft mc.
func (c *Client) LatestByHash(ctx context.Context, hashes []string, loader, mc string) (map[string]domain.ModVersion, error) {
	return c.byHash(ctx, "/version_files/update", map[string]any{
		"hashes": hashes, "algorithm": "sha512", "loaders": []string{loader}, "game_versions": []string{mc},
	})
}

func (c *Client) byHash(ctx context.Context, path string, body map[string]any) (map[string]domain.ModVersion, error) {
	out := map[string]domain.ModVersion{}
	if hashes, _ := body["hashes"].([]string); len(hashes) == 0 {
		return out, nil
	}
	var m map[string]version
	if err := c.postJSON(ctx, path, body, &m); err != nil {
		return nil, err
	}
	for h, v := range m {
		if dv, ok := v.toDomain(c.urls.CDN); ok && domain.ValidSHA512(h) {
			out[h] = dv
		}
	}
	return out, nil
}

// Download streams a mod file from the CDN. The reader fails instead of reaching EOF when
// the file is larger than f.Size or its SHA-512 differs, so a consumer that writes atomically
// never keeps an unverified file.
func (c *Client) Download(ctx context.Context, f domain.ModFile) (io.ReadCloser, error) {
	if !strings.HasPrefix(f.URL, c.urls.CDN) || !domain.ValidSHA512(f.SHA512) || f.Size <= 0 || f.Size > domain.ModFileMaxBytes {
		return nil, fmt.Errorf("%w: refusing file %q", domain.ErrModrinthUnavailable, f.URL)
	}
	resp, err := c.do(ctx, c.download, http.MethodGet, f.URL, nil)
	if err != nil {
		return nil, err
	}
	if resp.ContentLength > f.Size {
		resp.Body.Close()
		return nil, fmt.Errorf("%w: %s is larger than announced", domain.ErrModrinthUnavailable, f.Name)
	}
	return &verifier{body: resp.Body, r: io.LimitReader(resp.Body, f.Size+1), h: sha512.New(), file: f}, nil
}

type verifier struct {
	body io.Closer
	r    io.Reader
	h    hash.Hash
	n    int64
	file domain.ModFile
}

func (v *verifier) Read(p []byte) (int, error) {
	n, err := v.r.Read(p)
	v.h.Write(p[:n])
	v.n += int64(n)
	if err == io.EOF {
		if v.n != v.file.Size || hex.EncodeToString(v.h.Sum(nil)) != v.file.SHA512 {
			return n, fmt.Errorf("%w: %s failed verification (size or SHA-512 differs)", domain.ErrModrinthUnavailable, v.file.Name)
		}
	}
	return n, err
}

func (v *verifier) Close() error { return v.body.Close() }

// Icon downloads a project icon from the CDN and returns it with its sniffed content type.
// Only raster formats are accepted.
func (c *Client) Icon(ctx context.Context, iconURL string) ([]byte, string, error) {
	if !strings.HasPrefix(iconURL, c.urls.CDN) {
		return nil, "", domain.ErrNotFound
	}
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	resp, err := c.do(ctx, c.download, http.MethodGet, iconURL, nil)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	img, err := io.ReadAll(io.LimitReader(resp.Body, maxIcon+1))
	if err != nil {
		return nil, "", fmt.Errorf("%w: icon: %v", domain.ErrModrinthUnavailable, err)
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

func (c *Client) getJSON(ctx context.Context, path string, dst any) error {
	resp, err := c.do(ctx, c.api, http.MethodGet, c.urls.API+path, nil)
	if err != nil {
		return err
	}
	return decode(resp, dst)
}

func (c *Client) postJSON(ctx context.Context, path string, body, dst any) error {
	enc, err := json.Marshal(body)
	if err != nil {
		return err
	}
	resp, err := c.do(ctx, c.api, http.MethodPost, c.urls.API+path, enc)
	if err != nil {
		return err
	}
	return decode(resp, dst)
}

func decode(resp *http.Response, dst any) error {
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxJSON+1))
	if err != nil {
		return fmt.Errorf("%w: %v", domain.ErrModrinthUnavailable, err)
	}
	if len(body) > maxJSON {
		return fmt.Errorf("%w: response too large", domain.ErrModrinthUnavailable)
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return fmt.Errorf("%w: %v", domain.ErrModrinthUnavailable, err)
	}
	return nil
}

// do sends one request; a non-2xx answer is closed and turned into an error (404 is
// domain.ErrNotFound).
func (c *Client) do(ctx context.Context, client *http.Client, method, u string, body []byte) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", domain.ErrModrinthUnavailable, err)
	}
	req.Header.Set("User-Agent", userAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", domain.ErrModrinthUnavailable, err)
	}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		resp.Body.Close()
		return nil, domain.ErrNotFound
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		resp.Body.Close()
		return nil, fmt.Errorf("%w: %s %s: %s", domain.ErrModrinthUnavailable, method, req.URL.Path, resp.Status)
	}
	return resp, nil
}

type project struct {
	ID          string `json:"id"`
	Slug        string `json:"slug"`
	Title       string `json:"title"`
	Description string `json:"description"`
	IconURL     string `json:"icon_url"`
	ServerSide  string `json:"server_side"`
	Downloads   int64  `json:"downloads"`
}

func (p project) toDomain(cdn string) (domain.ModProject, bool) {
	if domain.ValidateModrinthID("project_id", p.ID) != nil {
		return domain.ModProject{}, false
	}
	icon := p.IconURL
	if !strings.HasPrefix(icon, cdn) {
		icon = ""
	}
	return domain.ModProject{
		ID: p.ID, Slug: p.Slug, Title: p.Title, Description: p.Description,
		IconURL: icon, ServerSide: p.ServerSide, Downloads: p.Downloads,
	}, true
}

type version struct {
	ID            string    `json:"id"`
	ProjectID     string    `json:"project_id"`
	Name          string    `json:"name"`
	VersionNumber string    `json:"version_number"`
	VersionType   string    `json:"version_type"`
	Loaders       []string  `json:"loaders"`
	GameVersions  []string  `json:"game_versions"`
	DatePublished time.Time `json:"date_published"`
	Files         []struct {
		Hashes   map[string]string `json:"hashes"`
		URL      string            `json:"url"`
		Filename string            `json:"filename"`
		Primary  bool              `json:"primary"`
		Size     int64             `json:"size"`
	} `json:"files"`
	Dependencies []struct {
		VersionID      string `json:"version_id"`
		ProjectID      string `json:"project_id"`
		DependencyType string `json:"dependency_type"`
	} `json:"dependencies"`
}

// toDomain keeps a version only if its IDs are valid and its primary file (or first, when none
// is marked) is a CDN jar with a SHA-512. Dependencies with invalid IDs are dropped.
func (v version) toDomain(cdn string) (domain.ModVersion, bool) {
	if domain.ValidateModrinthID("version_id", v.ID) != nil || domain.ValidateModrinthID("project_id", v.ProjectID) != nil || len(v.Files) == 0 {
		return domain.ModVersion{}, false
	}
	f := v.Files[0]
	for _, x := range v.Files {
		if x.Primary {
			f = x
			break
		}
	}
	file := domain.ModFile{URL: f.URL, Name: f.Filename, SHA512: strings.ToLower(f.Hashes["sha512"]), Size: f.Size}
	if !strings.HasPrefix(file.URL, cdn) || !domain.ValidSHA512(file.SHA512) || file.Size <= 0 || file.Size > domain.ModFileMaxBytes {
		return domain.ModVersion{}, false
	}
	out := domain.ModVersion{
		ID: v.ID, ProjectID: v.ProjectID, Name: v.Name, Number: v.VersionNumber, Type: v.VersionType,
		Loaders: v.Loaders, GameVersions: v.GameVersions, Published: v.DatePublished.UTC(), File: file,
	}
	for _, d := range v.Dependencies {
		dep := domain.ModDependency{ProjectID: d.ProjectID, VersionID: d.VersionID, Kind: d.DependencyType}
		if (dep.ProjectID != "" && domain.ValidateModrinthID("project_id", dep.ProjectID) != nil) ||
			(dep.VersionID != "" && domain.ValidateModrinthID("version_id", dep.VersionID) != nil) ||
			(dep.ProjectID == "" && dep.VersionID == "") {
			continue
		}
		out.Dependencies = append(out.Dependencies, dep)
	}
	return out, true
}
