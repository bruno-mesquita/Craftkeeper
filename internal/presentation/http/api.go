package httpapi

import (
	"io/fs"
	"log/slog"
	"net/http"
	"strings"

	"github.com/bruno-mesquita/mesquita-panel/internal/application/auth"
	"github.com/bruno-mesquita/mesquita-panel/internal/application/backups"
	"github.com/bruno-mesquita/mesquita-panel/internal/application/dashboard"
	"github.com/bruno-mesquita/mesquita-panel/internal/application/files"
	"github.com/bruno-mesquita/mesquita-panel/internal/application/installers"
	"github.com/bruno-mesquita/mesquita-panel/internal/application/monitoring"
	"github.com/bruno-mesquita/mesquita-panel/internal/application/players"
	"github.com/bruno-mesquita/mesquita-panel/internal/application/servers"
	wspres "github.com/bruno-mesquita/mesquita-panel/internal/presentation/websocket"
)

// API wires HTTP routes to application services.
type API struct {
	auth       *auth.Service
	monitoring *monitoring.Service
	dashboard  *dashboard.Service
	servers    *servers.Service
	installers *installers.Service
	files      *files.Service
	backups    *backups.Service
	players    *players.Service
	ws         *wspres.Hub
	static     fs.FS
	log        *slog.Logger
}

func New(a *auth.Service, m *monitoring.Service, d *dashboard.Service, s *servers.Service, i *installers.Service,
	f *files.Service, b *backups.Service, p *players.Service, ws *wspres.Hub, static fs.FS, log *slog.Logger) *API {
	return &API{auth: a, monitoring: m, dashboard: d, servers: s, installers: i, files: f, backups: b, players: p, ws: ws,
		static: static, log: log}
}

// Handler returns the complete HTTP handler with middleware applied.
func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/v1/health", a.health)
	mux.HandleFunc("GET /api/v1/setup", a.setupStatus)
	mux.HandleFunc("POST /api/v1/setup", a.completeSetup)

	mux.HandleFunc("POST /api/v1/auth/login", a.login)
	mux.HandleFunc("POST /api/v1/auth/logout", a.requireAuth(a.logout))
	mux.HandleFunc("GET /api/v1/auth/me", a.requireAuth(a.me))
	mux.HandleFunc("POST /api/v1/auth/password", a.requireAuth(a.changePassword))

	mux.HandleFunc("GET /api/v1/system/stats", a.requireAuth(a.systemStats))
	mux.HandleFunc("GET /api/v1/system/containers", a.requireAuth(a.containerStats))
	mux.HandleFunc("DELETE /api/v1/system/containers/{id}", a.requireAuth(a.deleteOrphanContainer))
	mux.HandleFunc("GET /api/v1/dashboard", a.requireAuth(a.dashboardOverview))

	mux.HandleFunc("GET /api/v1/installers/{loader}/versions", a.requireAuth(a.minecraftVersions))
	mux.HandleFunc("GET /api/v1/installers/{loader}/versions/{minecraft}", a.requireAuth(a.loaderVersions))

	mux.HandleFunc("GET /api/v1/servers", a.requireAuth(a.listServers))
	mux.HandleFunc("POST /api/v1/servers", a.requireAuth(a.createServer))
	mux.HandleFunc("GET /api/v1/servers/{id}", a.requireAuth(a.withServerID(a.getServer)))
	mux.HandleFunc("PATCH /api/v1/servers/{id}", a.requireAuth(a.withServerID(a.updateServer)))
	mux.HandleFunc("DELETE /api/v1/servers/{id}", a.requireAuth(a.withServerID(a.deleteServer)))
	mux.HandleFunc("POST /api/v1/servers/{id}/start", a.requireAuth(a.withServerID(a.serverAction(a.servers.Start))))
	mux.HandleFunc("POST /api/v1/servers/{id}/stop", a.requireAuth(a.withServerID(a.serverAction(a.servers.Stop))))
	mux.HandleFunc("POST /api/v1/servers/{id}/restart", a.requireAuth(a.withServerID(a.serverAction(a.servers.Restart))))
	mux.HandleFunc("POST /api/v1/servers/{id}/kill", a.requireAuth(a.withServerID(a.serverAction(a.servers.Kill))))

	fileRoute := func(h http.HandlerFunc) http.HandlerFunc { return a.requireAuth(a.withServerID(h)) }
	mux.HandleFunc("GET /api/v1/servers/{id}/files", fileRoute(a.listFiles))
	mux.HandleFunc("DELETE /api/v1/servers/{id}/files", fileRoute(a.deleteFile))
	mux.HandleFunc("GET /api/v1/servers/{id}/files/download", fileRoute(a.downloadFile))
	mux.HandleFunc("GET /api/v1/servers/{id}/files/content", fileRoute(a.readFileContent))
	mux.HandleFunc("PUT /api/v1/servers/{id}/files/content", fileRoute(a.writeFileContent))
	mux.HandleFunc("POST /api/v1/servers/{id}/files/upload", fileRoute(a.uploadFiles))
	mux.HandleFunc("POST /api/v1/servers/{id}/files/directory", fileRoute(a.makeDirectory))
	mux.HandleFunc("POST /api/v1/servers/{id}/files/rename", fileRoute(a.renameFile))
	mux.HandleFunc("POST /api/v1/servers/{id}/files/extract", fileRoute(a.extractArchive))
	mux.HandleFunc("GET /api/v1/servers/{id}/modpack", fileRoute(a.getModpack))
	mux.HandleFunc("PUT /api/v1/servers/{id}/modpack", fileRoute(a.configureModpack))
	mux.HandleFunc("POST /api/v1/servers/{id}/modpack/install", fileRoute(a.installModpack))

	backupRoute := func(h http.HandlerFunc) http.HandlerFunc { return fileRoute(withBackupID(h)) }
	mux.HandleFunc("GET /api/v1/servers/{id}/backups", fileRoute(a.listBackups))
	mux.HandleFunc("POST /api/v1/servers/{id}/backups", fileRoute(a.createBackup))
	mux.HandleFunc("GET /api/v1/servers/{id}/backups/schedule", fileRoute(a.getBackupSchedule))
	mux.HandleFunc("PUT /api/v1/servers/{id}/backups/schedule", fileRoute(a.updateBackupSchedule))
	mux.HandleFunc("GET /api/v1/servers/{id}/backups/{backupID}/download", backupRoute(a.downloadBackup))
	mux.HandleFunc("POST /api/v1/servers/{id}/backups/{backupID}/restore", backupRoute(a.restoreBackup))
	mux.HandleFunc("DELETE /api/v1/servers/{id}/backups/{backupID}", backupRoute(a.deleteBackup))

	a.playerRoutes(mux, fileRoute)

	mux.HandleFunc("GET /api/v1/ws", a.requireAuth(a.websocket))

	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "unknown API endpoint")
	})
	// Registered without a method: "GET /" would conflict with the method-less "/api/" pattern.
	mux.Handle("/", staticHandler(a.static))

	// Rejects cross-origin state-changing requests (Sec-Fetch-Site / Origin), covering the
	// unauthenticated login and setup endpoints that have no session CSRF token yet.
	var h http.Handler = http.NewCrossOriginProtection().Handler(mux)
	h = requestLog(a.log, h)
	h = securityHeaders(h)
	return recoverPanics(a.log, h)
}

// websocket hands an authenticated handshake to the WebSocket manager (docs/WEBSOCKET.md).
// The session token is passed along so the manager can re-check it while the connection lives.
func (a *API) websocket(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(sessionCookie)
	if err != nil { // requireAuth already checked it
		writeError(w, http.StatusUnauthorized, "unauthenticated", "authentication required")
		return
	}
	a.ws.Serve(w, r, identityFrom(r.Context()), c.Value, clientIP(r))
}

// staticHandler serves the embedded frontend without directory listings.
func staticHandler(static fs.FS) http.Handler {
	files := http.FileServerFS(static)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path != "/" && strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r)
			return
		}
		// Assets are not content-hashed; force revalidation so upgrades take effect.
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	})
}
