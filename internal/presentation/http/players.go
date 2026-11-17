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
	writeJSON(w, http.StatusOK, map[string]any{
		"whitelist":      map[string]any{"enabled": l.WhitelistEnabled, "players": wl},
		"banned_players": bans,
		"banned_ips":     ips,
	})
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
	mux.HandleFunc("PUT "+base+"/whitelist", route(a.playerAction(func(r *http.Request, id string, b playerBody, actor servers.Actor) error {
		return a.players.SetWhitelistEnabled(r.Context(), id, b.Enabled, actor)
	})))
	mux.HandleFunc("POST "+base+"/whitelist", route(a.playerAction(func(r *http.Request, id string, b playerBody, actor servers.Actor) error {
		return a.players.AddWhitelist(r.Context(), id, b.Name, actor)
	})))
	mux.HandleFunc("DELETE "+base+"/whitelist", route(a.playerAction(func(r *http.Request, id string, b playerBody, actor servers.Actor) error {
		return a.players.RemoveWhitelist(r.Context(), id, b.Name, actor)
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
