package httpapi

import (
	"net/http"

	"github.com/bruno-mesquita/mesquita-panel/internal/application/servers"
)

// playerBody is the JSON body of the player list mutations; each endpoint reads the field it needs.
type playerBody struct {
	Name    string `json:"name"`
	IP      string `json:"ip"`
	Reason  string `json:"reason"`
	Enabled bool   `json:"enabled"`
}

func (a *API) listPlayers(w http.ResponseWriter, r *http.Request) {
	l, err := a.players.List(r.Context(), r.PathValue("id"))
	if err != nil {
		writeDomainError(w, a.log, err)
		return
	}
	bans := make([]map[string]string, 0, len(l.BannedPlayers))
	for _, b := range l.BannedPlayers {
		bans = append(bans, map[string]string{"uuid": b.UUID, "name": b.Name, "created": b.Created, "source": b.Source, "expires": b.Expires, "reason": b.Reason})
	}
	ips := make([]map[string]string, 0, len(l.BannedIPs))
	for _, b := range l.BannedIPs {
		ips = append(ips, map[string]string{"ip": b.IP, "created": b.Created, "source": b.Source, "expires": b.Expires, "reason": b.Reason})
	}
	wl := make([]map[string]string, 0, len(l.Whitelist))
	for _, p := range l.Whitelist {
		wl = append(wl, map[string]string{"uuid": p.UUID, "name": p.Name})
	}
	ops := make([]map[string]any, 0, len(l.Ops))
	for _, o := range l.Ops {
		ops = append(ops, map[string]any{"uuid": o.UUID, "name": o.Name, "level": o.Level})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"whitelist":      map[string]any{"enabled": l.WhitelistEnabled, "players": wl},
		"ops":            ops,
		"banned_players": bans,
		"banned_ips":     ips,
	})
}

func (a *API) onlinePlayers(w http.ResponseWriter, r *http.Request) {
	o, err := a.players.Online(r.Context(), r.PathValue("id"))
	if err != nil {
		writeDomainError(w, a.log, err)
		return
	}
	list := make([]map[string]string, 0, len(o.Players))
	for _, p := range o.Players {
		list = append(list, map[string]string{"uuid": p.UUID, "name": p.Name})
	}
	writeJSON(w, http.StatusOK, map[string]any{"online": o.Online, "max": o.Max, "players": list})
}

// playerAvatar serves the face of a player's skin, looked up at Mojang. The browser may keep it a day.
func (a *API) playerAvatar(w http.ResponseWriter, r *http.Request) {
	img, err := a.players.Avatar(r.Context(), r.PathValue("uuid"))
	if err != nil {
		writeDomainError(w, a.log, err)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	_, _ = w.Write(img)
}

// playerAction decodes the body and runs one mutation, answering 204.
func (a *API) playerAction(do func(r *http.Request, id string, b playerBody, actor servers.Actor) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var b playerBody
		if !decodeJSON(w, r, &b) {
			return
		}
		if err := do(r, r.PathValue("id"), b, actorOf(r)); err != nil {
			writeDomainError(w, a.log, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func (a *API) playerRoutes(mux *http.ServeMux, route func(http.HandlerFunc) http.HandlerFunc) {
	const base = "/api/v1/servers/{id}/players"
	mux.HandleFunc("GET "+base, route(a.listPlayers))
	mux.HandleFunc("GET "+base+"/online", route(a.onlinePlayers))
	mux.HandleFunc("GET /api/v1/players/{uuid}/avatar", a.requireAuth(a.playerAvatar))
	mux.HandleFunc("PUT "+base+"/whitelist", route(a.playerAction(func(r *http.Request, id string, b playerBody, actor servers.Actor) error {
		return a.players.SetWhitelistEnabled(r.Context(), id, b.Enabled, actor)
	})))
	mux.HandleFunc("POST "+base+"/whitelist", route(a.playerAction(func(r *http.Request, id string, b playerBody, actor servers.Actor) error {
		return a.players.AddWhitelist(r.Context(), id, b.Name, actor)
	})))
	mux.HandleFunc("DELETE "+base+"/whitelist", route(a.playerAction(func(r *http.Request, id string, b playerBody, actor servers.Actor) error {
		return a.players.RemoveWhitelist(r.Context(), id, b.Name, actor)
	})))
	mux.HandleFunc("POST "+base+"/ops", route(a.playerAction(func(r *http.Request, id string, b playerBody, actor servers.Actor) error {
		return a.players.AddOp(r.Context(), id, b.Name, actor)
	})))
	mux.HandleFunc("DELETE "+base+"/ops", route(a.playerAction(func(r *http.Request, id string, b playerBody, actor servers.Actor) error {
		return a.players.RemoveOp(r.Context(), id, b.Name, actor)
	})))
	mux.HandleFunc("POST "+base+"/bans", route(a.playerAction(func(r *http.Request, id string, b playerBody, actor servers.Actor) error {
		return a.players.Ban(r.Context(), id, b.Name, b.Reason, actor)
	})))
	mux.HandleFunc("DELETE "+base+"/bans", route(a.playerAction(func(r *http.Request, id string, b playerBody, actor servers.Actor) error {
		return a.players.Pardon(r.Context(), id, b.Name, actor)
	})))
	mux.HandleFunc("POST "+base+"/ip-bans", route(a.playerAction(func(r *http.Request, id string, b playerBody, actor servers.Actor) error {
		return a.players.BanIP(r.Context(), id, b.IP, b.Reason, actor)
	})))
	mux.HandleFunc("DELETE "+base+"/ip-bans", route(a.playerAction(func(r *http.Request, id string, b playerBody, actor servers.Actor) error {
		return a.players.PardonIP(r.Context(), id, b.IP, actor)
	})))
}
