package httpapi

import (
	"net/http"
	"strconv"
	"time"

	"github.com/bruno-mesquita/Craftkeeper/internal/application/mods"
	"github.com/bruno-mesquita/Craftkeeper/internal/domain"
)

// installDeadline bounds downloading a mod and its dependencies.
const installDeadline = 15 * time.Minute

type modProjectDTO struct {
	ID          string `json:"id"`
	Slug        string `json:"slug"`
	Title       string `json:"title"`
	Description string `json:"description"`
	HasIcon     bool   `json:"has_icon"`
	ServerSide  string `json:"server_side"`
	Downloads   int64  `json:"downloads"`
}

type modDependencyDTO struct {
	ProjectID string `json:"project_id"`
	VersionID string `json:"version_id"`
	Kind      string `json:"kind"`
}

type modVersionDTO struct {
	ID           string             `json:"id"`
	ProjectID    string             `json:"project_id"`
	Name         string             `json:"name"`
	Number       string             `json:"number"`
	Type         string             `json:"type"`
	PublishedAt  string             `json:"published_at"`
	FileName     string             `json:"file_name"`
	Size         int64              `json:"size"`
	Loaders      []string           `json:"loaders"`
	GameVersions []string           `json:"game_versions"`
	Dependencies []modDependencyDTO `json:"dependencies"`
}

type installedModDTO struct {
	File       string         `json:"file"`
	Enabled    bool           `json:"enabled"`
	Size       int64          `json:"size"`
	ModifiedAt string         `json:"modified_at"`
	Project    *modProjectDTO `json:"project"`
	Version    *modVersionDTO `json:"version"`
	Update     *modVersionDTO `json:"update"`
}

func toModProjectDTO(p domain.ModProject) modProjectDTO {
	return modProjectDTO{ID: p.ID, Slug: p.Slug, Title: p.Title, Description: p.Description,
		HasIcon: p.IconURL != "", ServerSide: p.ServerSide, Downloads: p.Downloads}
}

func toModVersionDTO(v domain.ModVersion) modVersionDTO {
	deps := make([]modDependencyDTO, 0, len(v.Dependencies))
	for _, d := range v.Dependencies {
		deps = append(deps, modDependencyDTO{ProjectID: d.ProjectID, VersionID: d.VersionID, Kind: d.Kind})
	}
	return modVersionDTO{
		ID: v.ID, ProjectID: v.ProjectID, Name: v.Name, Number: v.Number, Type: v.Type,
		PublishedAt: rfc3339(v.Published), FileName: v.File.Name, Size: v.File.Size,
		Loaders: nonNil(v.Loaders), GameVersions: nonNil(v.GameVersions), Dependencies: deps,
	}
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func (a *API) listMods(w http.ResponseWriter, r *http.Request) {
	l, err := a.mods.List(r.Context(), r.PathValue("id"))
	if err != nil {
		writeDomainError(w, a.log, err)
		return
	}
	out := make([]installedModDTO, 0, len(l.Mods))
	for _, m := range l.Mods {
		d := installedModDTO{File: m.File, Enabled: m.Enabled, Size: m.Size, ModifiedAt: rfc3339(m.ModifiedAt)}
		if m.Project != nil {
			p := toModProjectDTO(*m.Project)
			d.Project = &p
		}
		if m.Version != nil {
			v := toModVersionDTO(*m.Version)
			d.Version = &v
		}
		if m.Update != nil {
			v := toModVersionDTO(*m.Update)
			d.Update = &v
		}
		out = append(out, d)
	}
	writeJSON(w, http.StatusOK, map[string]any{"mods": out, "catalog_unavailable": l.CatalogUnavailable})
}

func (a *API) searchMods(w http.ResponseWriter, r *http.Request) {
	offset := 0
	if s := r.URL.Query().Get("offset"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			writeDomainError(w, a.log, &domain.ValidationError{Field: "offset", Message: "must be a number"})
			return
		}
		offset = n
	}
	res, err := a.mods.Search(r.Context(), r.PathValue("id"), r.URL.Query().Get("q"), offset)
	if err != nil {
		writeDomainError(w, a.log, err)
		return
	}
	hits := make([]modProjectDTO, 0, len(res.Hits))
	for _, p := range res.Hits {
		hits = append(hits, toModProjectDTO(p))
	}
	writeJSON(w, http.StatusOK, map[string]any{"hits": hits, "offset": res.Offset, "total": res.Total})
}

func (a *API) modVersions(w http.ResponseWriter, r *http.Request) {
	list, err := a.mods.Versions(r.Context(), r.PathValue("id"), r.PathValue("project"))
	if err != nil {
		writeDomainError(w, a.log, err)
		return
	}
	out := make([]modVersionDTO, 0, len(list))
	for _, v := range list {
		out = append(out, toModVersionDTO(v))
	}
	writeJSON(w, http.StatusOK, map[string]any{"versions": out})
}

func writeModResult(w http.ResponseWriter, res mods.Result) {
	updated := make([]map[string]string, 0, len(res.Updated))
	for _, c := range res.Updated {
		updated = append(updated, map[string]string{"from": c.From, "to": c.To})
	}
	writeJSON(w, http.StatusOK, map[string]any{"installed": nonNil(res.Installed), "updated": updated})
}

func (a *API) installMod(w http.ResponseWriter, r *http.Request) {
	extendDeadlines(w, installDeadline)
	var req struct {
		ProjectID string `json:"project_id"`
		VersionID string `json:"version_id"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	res, err := a.mods.Install(r.Context(), r.PathValue("id"), req.ProjectID, req.VersionID, actorOf(r))
	if err != nil {
		writeDomainError(w, a.log, err)
		return
	}
	writeModResult(w, res)
}

func (a *API) updateMods(w http.ResponseWriter, r *http.Request) {
	extendDeadlines(w, maintenanceDeadline) // a full backup may come first
	var req struct {
		Files  []string `json:"files"`
		Backup bool     `json:"backup"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if len(req.Files) > 1000 {
		writeDomainError(w, a.log, &domain.ValidationError{Field: "files", Message: "too many files"})
		return
	}
	res, err := a.mods.Update(r.Context(), r.PathValue("id"), req.Files, req.Backup, actorOf(r))
	if err != nil {
		writeDomainError(w, a.log, err)
		return
	}
	writeModResult(w, res)
}

func (a *API) setModEnabled(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Enabled == nil {
		writeDomainError(w, a.log, &domain.ValidationError{Field: "enabled", Message: "is required"})
		return
	}
	file, err := a.mods.SetEnabled(r.Context(), r.PathValue("id"), r.PathValue("file"), *req.Enabled, actorOf(r))
	if err != nil {
		writeDomainError(w, a.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"file": file, "enabled": *req.Enabled})
}

func (a *API) removeMod(w http.ResponseWriter, r *http.Request) {
	if err := a.mods.Remove(r.Context(), r.PathValue("id"), r.PathValue("file"), actorOf(r)); err != nil {
		writeDomainError(w, a.log, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// modIcon serves a mod's icon, fetched from the Modrinth CDN. The browser may keep it a day.
func (a *API) modIcon(w http.ResponseWriter, r *http.Request) {
	img, ct, err := a.mods.Icon(r.Context(), r.PathValue("project"))
	if err != nil {
		writeDomainError(w, a.log, err)
		return
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "private, max-age=86400")
	_, _ = w.Write(img)
}

func (a *API) modRoutes(mux *http.ServeMux, route func(http.HandlerFunc) http.HandlerFunc) {
	const base = "/api/v1/servers/{id}/mods"
	mux.HandleFunc("GET "+base, route(a.listMods))
	mux.HandleFunc("POST "+base, route(a.installMod))
	mux.HandleFunc("GET "+base+"/search", route(a.searchMods))
	mux.HandleFunc("GET "+base+"/projects/{project}/versions", route(a.modVersions))
	mux.HandleFunc("POST "+base+"/update", route(a.updateMods))
	mux.HandleFunc("PATCH "+base+"/files/{file}", route(a.setModEnabled))
	mux.HandleFunc("DELETE "+base+"/files/{file}", route(a.removeMod))
	mux.HandleFunc("GET /api/v1/mods/icons/{project}", a.requireAuth(a.modIcon))
}
