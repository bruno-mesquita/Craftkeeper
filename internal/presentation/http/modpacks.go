package httpapi

import (
	"net/http"
	"strconv"

	"github.com/bruno-mesquita/Craftkeeper/internal/domain"
)

type modpackVersionDTO struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	PublishedAt string `json:"published_at"`
	Loader      string `json:"loader"`
	GameVersion string `json:"game_version"`
}

func toModpackVersionDTOs(list []domain.ModpackVersion) []modpackVersionDTO {
	out := make([]modpackVersionDTO, 0, len(list))
	for _, v := range list {
		out = append(out, modpackVersionDTO{ID: v.ID, Name: v.Name, Type: v.Type, PublishedAt: rfc3339(v.Published),
			Loader: v.Loader, GameVersion: v.GameVersion})
	}
	return out
}

func (a *API) searchModpacks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	offset := 0
	if s := q.Get("offset"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			writeDomainError(w, a.log, &domain.ValidationError{Field: "offset", Message: "must be a number"})
			return
		}
		offset = n
	}
	res, err := a.modpacks.Search(r.Context(), q.Get("source"), q.Get("q"), offset)
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

func (a *API) modpackVersions(w http.ResponseWriter, r *http.Request) {
	list, err := a.modpacks.Versions(r.Context(), r.PathValue("source"), r.PathValue("project"))
	if err != nil {
		writeDomainError(w, a.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"versions": toModpackVersionDTOs(list)})
}

// modpackIcon serves a modpack's icon, fetched from its catalog's CDN. The browser may keep it
// a day.
func (a *API) modpackIcon(w http.ResponseWriter, r *http.Request) {
	img, ct, err := a.modpacks.Icon(r.Context(), r.PathValue("source"), r.PathValue("project"))
	if err != nil {
		writeDomainError(w, a.log, err)
		return
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "private, max-age=86400")
	_, _ = w.Write(img)
}

func (a *API) serverModpackVersions(w http.ResponseWriter, r *http.Request) {
	current, list, err := a.modpacks.ServerVersions(r.Context(), r.PathValue("id"))
	if err != nil {
		writeDomainError(w, a.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"modpack": toModpackDTO(current), "versions": toModpackVersionDTOs(list)})
}

func (a *API) updateServerModpack(w http.ResponseWriter, r *http.Request) {
	extendDeadlines(w, maintenanceDeadline) // a full backup comes first
	var req struct {
		VersionID string `json:"version_id"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	v, err := a.modpacks.Update(r.Context(), r.PathValue("id"), req.VersionID, actorOf(r))
	if err != nil {
		writeDomainError(w, a.log, err)
		return
	}
	writeJSON(w, http.StatusOK, toServerDTO(v))
}

func (a *API) curseForgeKeyStatus(w http.ResponseWriter, r *http.Request) {
	configured, hint, err := a.modpacks.CurseForgeKeyStatus(r.Context())
	if err != nil {
		writeDomainError(w, a.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"configured": configured, "hint": hint})
}

func (a *API) setCurseForgeKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		APIKey string `json:"api_key"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := a.modpacks.SetCurseForgeKey(r.Context(), req.APIKey, actorOf(r)); err != nil {
		writeDomainError(w, a.log, err)
		return
	}
	a.curseForgeKeyStatus(w, r)
}

func (a *API) removeCurseForgeKey(w http.ResponseWriter, r *http.Request) {
	if err := a.modpacks.RemoveCurseForgeKey(r.Context(), actorOf(r)); err != nil {
		writeDomainError(w, a.log, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) modpackRoutes(mux *http.ServeMux, route func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("GET /api/v1/modpacks/search", a.requireAuth(a.searchModpacks))
	mux.HandleFunc("GET /api/v1/modpacks/{source}/{project}/versions", a.requireAuth(a.modpackVersions))
	mux.HandleFunc("GET /api/v1/modpacks/{source}/{project}/icon", a.requireAuth(a.modpackIcon))
	mux.HandleFunc("GET /api/v1/servers/{id}/modpack/versions", route(a.serverModpackVersions))
	mux.HandleFunc("POST /api/v1/servers/{id}/modpack/version", route(a.updateServerModpack))
	mux.HandleFunc("GET /api/v1/settings/curseforge", a.requireAuth(a.curseForgeKeyStatus))
	mux.HandleFunc("PUT /api/v1/settings/curseforge", a.requireAuth(a.setCurseForgeKey))
	mux.HandleFunc("DELETE /api/v1/settings/curseforge", a.requireAuth(a.removeCurseForgeKey))
}
