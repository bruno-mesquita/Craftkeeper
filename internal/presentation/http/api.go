package httpapi

import (
	"io/fs"
	"log/slog"
	"net/http"
	"strings"

	"github.com/bruno-mesquita/mesquita-panel/internal/application/auth"
	"github.com/bruno-mesquita/mesquita-panel/internal/application/dashboard"
	"github.com/bruno-mesquita/mesquita-panel/internal/application/monitoring"
)

// API wires HTTP routes to application services.
type API struct {
	auth       *auth.Service
	monitoring *monitoring.Service
	dashboard  *dashboard.Service
	static     fs.FS
	log        *slog.Logger
}

func New(a *auth.Service, m *monitoring.Service, d *dashboard.Service, static fs.FS, log *slog.Logger) *API {
	return &API{auth: a, monitoring: m, dashboard: d, static: static, log: log}
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
	mux.HandleFunc("GET /api/v1/dashboard", a.requireAuth(a.dashboardOverview))

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
