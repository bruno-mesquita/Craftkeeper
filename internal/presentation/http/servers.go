package httpapi

import (
	"context"
	"math"
	"net/http"
	"time"

	"github.com/bruno-mesquita/Craftkeeper/internal/application/servers"
	"github.com/bruno-mesquita/Craftkeeper/internal/domain"
)

// Deadline for requests that wait on a graceful game stop, longer than the server-wide
// timeouts (main.go). Server creation does not wait: the image pull runs in the background.
const stopDeadline = 3 * time.Minute // graceful stop (60 s) + removal, with margin

type serverDTO struct {
	ID            string      `json:"id"`
	Name          string      `json:"name"`
	Description   string      `json:"description"`
	Game          string      `json:"game"`
	Loader        string      `json:"loader"`
	Version       string      `json:"version"`
	LoaderVersion string      `json:"loader_version"`
	Status        string      `json:"status"`
	Failure       string      `json:"failure"`
	MemoryMB      int         `json:"memory_mb"`
	CPUCores      float64     `json:"cpu_cores"`
	Port          int         `json:"port"`
	ContainerID   string      `json:"container_id"`
	CreatedAt     string      `json:"created_at"`
	UpdatedAt     string      `json:"updated_at"`
	Crash         *crashDTO   `json:"crash"`
	Modpack       *modpackDTO `json:"modpack"`
}

// modpackDTO is the catalog modpack a server runs. Null when there is none.
type modpackDTO struct {
	Source      string `json:"source"`
	ProjectID   string `json:"project_id"`
	Slug        string `json:"slug"`
	VersionID   string `json:"version_id"`
	Name        string `json:"name"`
	VersionName string `json:"version_name"`
}

// crashDTO is the crash loop record of a server: when the panel stopped it and after how
// many automatic restarts. Null when there is none.
type crashDTO struct {
	At       string `json:"at"`
	Restarts int    `json:"restarts"`
}

func toServerDTO(v servers.View) serverDTO {
	return serverDTO{
		ID: v.ID, Name: v.Name, Description: v.Description, Game: v.Game, Loader: v.Loader, Version: v.Version,
		LoaderVersion: v.LoaderVersion, Status: string(v.Status), Failure: v.Failure, MemoryMB: v.MemoryMB, CPUCores: float64(v.CPUMillicores) / 1000, Port: v.Port,
		ContainerID: v.ContainerID, CreatedAt: rfc3339(v.CreatedAt), UpdatedAt: rfc3339(v.UpdatedAt),
		Crash: toCrashDTO(v.Server), Modpack: toModpackDTO(v.Modpack),
	}
}

func toModpackDTO(m domain.ModpackRef) *modpackDTO {
	if !m.Set() {
		return nil
	}
	return &modpackDTO{Source: m.Source, ProjectID: m.ProjectID, Slug: m.Slug, VersionID: m.VersionID,
		Name: m.Name, VersionName: m.VersionName}
}

func toCrashDTO(x domain.Server) *crashDTO {
	if x.CrashedAt.IsZero() {
		return nil
	}
	return &crashDTO{At: rfc3339(x.CrashedAt), Restarts: x.CrashRestarts}
}

// millicores converts a JSON core count (e.g. 1.5) to millicores; out-of-range values are
// left for domain validation to reject.
func millicores(cores float64) int {
	if math.IsNaN(cores) || math.IsInf(cores, 0) || cores < 0 || cores > 1e6 {
		return -1
	}
	return int(math.Round(cores * 1000))
}

func actorOf(r *http.Request) servers.Actor {
	return servers.Actor{UserID: identityFrom(r.Context()).User.ID, IP: clientIP(r)}
}

// extendDeadlines lets one request outlive the server-wide read/write timeouts. The read
// deadline matters too: when it expires, net/http cancels the request context.
func extendDeadlines(w http.ResponseWriter, d time.Duration) {
	rc := http.NewResponseController(w)
	at := time.Now().Add(d)
	_ = rc.SetReadDeadline(at)
	_ = rc.SetWriteDeadline(at)
}

// withServerID rejects IDs the panel could not have generated before any lookup.
func (a *API) withServerID(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !domain.ValidServerID(r.PathValue("id")) {
			writeError(w, http.StatusNotFound, "not_found", "resource not found")
			return
		}
		next(w, r)
	}
}

func (a *API) listServers(w http.ResponseWriter, r *http.Request) {
	list, err := a.servers.List(r.Context())
	if err != nil {
		writeDomainError(w, a.log, err)
		return
	}
	out := make([]serverDTO, 0, len(list))
	for _, v := range list {
		out = append(out, toServerDTO(v))
	}
	writeJSON(w, http.StatusOK, map[string]any{"servers": out})
}

// modpackRequest selects a catalog modpack version for a new server.
type modpackRequest struct {
	Source    string `json:"source"`
	ProjectID string `json:"project_id"`
	VersionID string `json:"version_id"`
}

func (a *API) createServer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name          string          `json:"name"`
		Description   string          `json:"description"`
		Loader        string          `json:"loader"`
		Version       string          `json:"version"`
		LoaderVersion string          `json:"loader_version"`
		MemoryMB      int             `json:"memory_mb"`
		CPUCores      float64         `json:"cpu_cores"`
		Port          int             `json:"port"`
		AcceptEULA    bool            `json:"accept_eula"`
		Start         bool            `json:"start"`
		Modpack       *modpackRequest `json:"modpack"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	var pack domain.ModpackRef
	if m := req.Modpack; m != nil {
		// The pack sets the game: the catalog says which loader and Minecraft version it runs.
		if req.Loader != "" || req.Version != "" || req.LoaderVersion != "" {
			writeDomainError(w, a.log, &domain.ValidationError{Field: "modpack", Message: "replaces loader, version and loader_version"})
			return
		}
		ref, game, err := a.modpacks.Resolve(r.Context(), m.Source, m.ProjectID, m.VersionID)
		if err != nil {
			writeDomainError(w, a.log, err)
			return
		}
		pack, req.Loader, req.Version = ref, game.Loader, game.Version
	}
	if req.Loader == "" {
		req.Loader = domain.LoaderVanilla
	}
	if req.Version == "" && req.Loader == domain.LoaderVanilla {
		req.Version = domain.MinecraftLatest
	}
	v, err := a.servers.Create(r.Context(), servers.CreateInput{
		Name: req.Name, Description: req.Description, Loader: req.Loader, Version: req.Version,
		LoaderVersion: req.LoaderVersion, MemoryMB: req.MemoryMB,
		CPUMillicores: millicores(req.CPUCores), Port: req.Port, AcceptEULA: req.AcceptEULA,
		Start: req.Start, Modpack: pack,
	}, actorOf(r))
	if err != nil {
		writeDomainError(w, a.log, err)
		return
	}
	// The image pull and container creation continue in the background.
	writeJSON(w, http.StatusAccepted, toServerDTO(v))
}

func (a *API) getServer(w http.ResponseWriter, r *http.Request) {
	v, err := a.servers.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeDomainError(w, a.log, err)
		return
	}
	writeJSON(w, http.StatusOK, toServerDTO(v))
}

func (a *API) updateServer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        *string  `json:"name"`
		Description *string  `json:"description"`
		MemoryMB    *int     `json:"memory_mb"`
		CPUCores    *float64 `json:"cpu_cores"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	in := servers.UpdateInput{Name: req.Name, Description: req.Description, MemoryMB: req.MemoryMB}
	if req.CPUCores != nil {
		m := millicores(*req.CPUCores)
		in.CPUMillicores = &m
	}
	v, err := a.servers.Update(r.Context(), r.PathValue("id"), in, actorOf(r))
	if err != nil {
		writeDomainError(w, a.log, err)
		return
	}
	writeJSON(w, http.StatusOK, toServerDTO(v))
}

func (a *API) deleteServer(w http.ResponseWriter, r *http.Request) {
	var deleteFiles bool
	switch r.URL.Query().Get("delete_files") {
	case "", "false":
	case "true":
		deleteFiles = true
	default:
		writeDomainError(w, a.log, &domain.ValidationError{Field: "delete_files", Message: `must be "true" or "false"`})
		return
	}
	extendDeadlines(w, stopDeadline)
	if err := a.servers.Delete(r.Context(), r.PathValue("id"), deleteFiles, actorOf(r)); err != nil {
		writeDomainError(w, a.log, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// serverAction adapts a lifecycle use case (start, stop, restart, kill) to a handler.
func (a *API) serverAction(op func(context.Context, string, servers.Actor) (servers.View, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		extendDeadlines(w, stopDeadline)
		v, err := op(r.Context(), r.PathValue("id"), actorOf(r))
		if err != nil {
			writeDomainError(w, a.log, err)
			return
		}
		writeJSON(w, http.StatusOK, toServerDTO(v))
	}
}
