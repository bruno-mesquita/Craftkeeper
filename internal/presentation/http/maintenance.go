package httpapi

import (
	"net/http"
	"time"

	"github.com/bruno-mesquita/mesquita-panel/internal/application/servers"
	"github.com/bruno-mesquita/mesquita-panel/internal/domain"
)

// maintenanceDeadline bounds a change made after a full backup of the server (and, for an
// import, unpacking a whole world; for a version change, pulling an image).
const maintenanceDeadline = 45 * time.Minute

func (a *API) changeVersion(w http.ResponseWriter, r *http.Request) {
	extendDeadlines(w, maintenanceDeadline)
	var req struct {
		Loader        string `json:"loader"`
		Version       string `json:"version"`
		LoaderVersion string `json:"loader_version"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Version == "" && req.Loader == domain.LoaderVanilla {
		req.Version = domain.MinecraftLatest
	}
	v, err := a.maintenance.ChangeVersion(r.Context(), r.PathValue("id"), servers.VersionInput{
		Loader: req.Loader, Version: req.Version, LoaderVersion: req.LoaderVersion,
	}, actorOf(r))
	if err != nil {
		writeDomainError(w, a.log, err)
		return
	}
	writeJSON(w, http.StatusOK, toServerDTO(v))
}

func (a *API) resetWorld(w http.ResponseWriter, r *http.Request) {
	extendDeadlines(w, maintenanceDeadline)
	var req struct {
		Seed *string `json:"seed"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := a.maintenance.ResetWorld(r.Context(), r.PathValue("id"), req.Seed, actorOf(r)); err != nil {
		writeDomainError(w, a.log, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) importWorld(w http.ResponseWriter, r *http.Request) {
	extendDeadlines(w, maintenanceDeadline)
	var req struct {
		Path string `json:"path"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := a.maintenance.ImportWorld(r.Context(), r.PathValue("id"), req.Path, actorOf(r)); err != nil {
		writeDomainError(w, a.log, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
