package httpapi

import (
	"net/http"
	"time"

	"github.com/bruno-mesquita/mesquita-panel/internal/application/auth"
	"github.com/bruno-mesquita/mesquita-panel/internal/domain"
)

// --- response DTOs: the public JSON contract (docs/API.md) ---

type userDTO struct {
	ID        string `json:"id"`
	Username  string `json:"username"`
	Role      string `json:"role"`
	CreatedAt string `json:"created_at"`
}

type sessionDTO struct {
	User      userDTO `json:"user"`
	CSRFToken string  `json:"csrf_token"`
}

func toUserDTO(u domain.User) userDTO {
	return userDTO{ID: u.ID, Username: u.Username, Role: string(u.Role), CreatedAt: rfc3339(u.CreatedAt)}
}

func toSessionDTO(id auth.Identity) sessionDTO {
	return sessionDTO{User: toUserDTO(id.User), CSRFToken: id.Session.CSRFToken}
}

func rfc3339(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// --- health & setup ---

func (a *API) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *API) setupStatus(w http.ResponseWriter, r *http.Request) {
	required, err := a.auth.SetupRequired(r.Context())
	if err != nil {
		writeDomainError(w, a.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"required": required})
}

func (a *API) completeSetup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token    string `json:"token"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	u, err := a.auth.CompleteSetup(r.Context(), auth.SetupInput{Token: req.Token, Username: req.Username, Password: req.Password, IP: clientIP(r)})
	if err != nil {
		writeDomainError(w, a.log, err)
		return
	}
	a.log.Info("initial setup completed", "username", u.Username)
	writeJSON(w, http.StatusCreated, map[string]userDTO{"user": toUserDTO(u)})
}

// --- auth ---

func (a *API) login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	token, id, err := a.auth.Login(r.Context(), auth.LoginInput{
		Username: req.Username, Password: req.Password, IP: clientIP(r), UserAgent: r.UserAgent(),
	})
	if err != nil {
		writeDomainError(w, a.log, err)
		return
	}
	setSessionCookie(w, r, token, domain.SessionAbsoluteTTL)
	writeJSON(w, http.StatusOK, toSessionDTO(id))
}

func (a *API) logout(w http.ResponseWriter, r *http.Request) {
	if err := a.auth.Logout(r.Context(), identityFrom(r.Context()), clientIP(r)); err != nil {
		writeDomainError(w, a.log, err)
		return
	}
	clearSessionCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) me(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, toSessionDTO(identityFrom(r.Context())))
}

func (a *API) changePassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	err := a.auth.ChangePassword(r.Context(), identityFrom(r.Context()), auth.ChangePasswordInput{
		CurrentPassword: req.CurrentPassword, NewPassword: req.NewPassword, IP: clientIP(r),
	})
	if err != nil {
		writeDomainError(w, a.log, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- system & dashboard ---

type systemStatsDTO struct {
	CollectedAt string `json:"collected_at"`
	CPU         struct {
		Cores        int     `json:"cores"`
		UsagePercent float64 `json:"usage_percent"`
	} `json:"cpu"`
	Memory struct {
		TotalBytes     uint64 `json:"total_bytes"`
		UsedBytes      uint64 `json:"used_bytes"`
		AvailableBytes uint64 `json:"available_bytes"`
	} `json:"memory"`
	Disk struct {
		Path       string `json:"path"`
		TotalBytes uint64 `json:"total_bytes"`
		UsedBytes  uint64 `json:"used_bytes"`
		FreeBytes  uint64 `json:"free_bytes"`
	} `json:"disk"`
	Network struct {
		RxBytes          uint64  `json:"rx_bytes"`
		TxBytes          uint64  `json:"tx_bytes"`
		RxBytesPerSecond float64 `json:"rx_bytes_per_second"`
		TxBytesPerSecond float64 `json:"tx_bytes_per_second"`
	} `json:"network"`
}

func (a *API) systemStats(w http.ResponseWriter, r *http.Request) {
	st, err := a.monitoring.Stats(r.Context())
	if err != nil {
		writeDomainError(w, a.log, err)
		return
	}
	var d systemStatsDTO
	d.CollectedAt = st.CollectedAt.UTC().Format(time.RFC3339Nano)
	d.CPU.Cores, d.CPU.UsagePercent = st.CPU.Cores, st.CPU.UsagePercent
	d.Memory.TotalBytes, d.Memory.UsedBytes, d.Memory.AvailableBytes = st.Memory.TotalBytes, st.Memory.UsedBytes, st.Memory.AvailableBytes
	d.Disk.Path, d.Disk.TotalBytes, d.Disk.UsedBytes, d.Disk.FreeBytes = st.Disk.Path, st.Disk.TotalBytes, st.Disk.UsedBytes, st.Disk.FreeBytes
	d.Network.RxBytes, d.Network.TxBytes = st.Network.RxBytes, st.Network.TxBytes
	d.Network.RxBytesPerSecond, d.Network.TxBytesPerSecond = st.Network.RxBytesPerSecond, st.Network.TxBytesPerSecond
	writeJSON(w, http.StatusOK, d)
}

type containerStatsDTO struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	ServerID   string  `json:"server_id"`
	State      string  `json:"state"`
	CPUPercent float64 `json:"cpu_percent"`
	Memory     struct {
		UsedBytes  uint64 `json:"used_bytes"`
		LimitBytes uint64 `json:"limit_bytes"`
	} `json:"memory"`
	Network struct {
		RxBytes          uint64  `json:"rx_bytes"`
		TxBytes          uint64  `json:"tx_bytes"`
		RxBytesPerSecond float64 `json:"rx_bytes_per_second"`
		TxBytesPerSecond float64 `json:"tx_bytes_per_second"`
	} `json:"network"`
	BlockIO struct {
		ReadBytes  uint64 `json:"read_bytes"`
		WriteBytes uint64 `json:"write_bytes"`
	} `json:"block_io"`
}

func (a *API) containerStats(w http.ResponseWriter, r *http.Request) {
	snap, err := a.monitoring.Containers(r.Context())
	if err != nil {
		writeDomainError(w, a.log, err)
		return
	}
	list := make([]containerStatsDTO, 0, len(snap.Containers))
	for _, c := range snap.Containers {
		var d containerStatsDTO
		d.ID, d.Name, d.ServerID, d.State, d.CPUPercent = c.ID, c.Name, c.ServerID, c.State, c.CPUPercent
		d.Memory.UsedBytes, d.Memory.LimitBytes = c.MemoryUsedBytes, c.MemoryLimitBytes
		d.Network.RxBytes, d.Network.TxBytes = c.NetRxBytes, c.NetTxBytes
		d.Network.RxBytesPerSecond, d.Network.TxBytesPerSecond = c.NetRxBytesPerSecond, c.NetTxBytesPerSecond
		d.BlockIO.ReadBytes, d.BlockIO.WriteBytes = c.BlockReadBytes, c.BlockWriteBytes
		list = append(list, d)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"collected_at": snap.CollectedAt.UTC().Format(time.RFC3339Nano),
		"containers":   list,
	})
}

type auditEventDTO struct {
	ID        int64  `json:"id"`
	CreatedAt string `json:"created_at"`
	Action    string `json:"action"`
	Target    string `json:"target"`
	IP        string `json:"ip"`
	Success   bool   `json:"success"`
}

func (a *API) dashboardOverview(w http.ResponseWriter, r *http.Request) {
	o, err := a.dashboard.Overview(r.Context())
	if err != nil {
		writeDomainError(w, a.log, err)
		return
	}
	events := make([]auditEventDTO, 0, len(o.RecentEvents))
	for _, e := range o.RecentEvents {
		events = append(events, auditEventDTO{ID: e.ID, CreatedAt: rfc3339(e.CreatedAt), Action: e.Action, Target: e.Target, IP: e.IP, Success: e.Success})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"docker":        map[string]bool{"reachable": o.DockerReachable},
		"servers":       map[string]int{"total": o.Servers.Total, "running": o.Servers.Running, "stopped": o.Servers.Stopped},
		"recent_events": events,
	})
}
