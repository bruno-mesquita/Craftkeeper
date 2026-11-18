package httpapi

import (
	"bufio"
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/bruno-mesquita/mesquita-panel/internal/application/auth"
	"github.com/bruno-mesquita/mesquita-panel/internal/domain"
)

const (
	sessionCookie = "mesquita_session"
	csrfHeader    = "X-CSRF-Token"
)

const contentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; " +
	"connect-src 'self'; font-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// Hijack lets WebSocket handshakes take over the connection through this wrapper.
func (s *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	s.status = http.StatusSwitchingProtocols
	return http.NewResponseController(s.ResponseWriter).Hijack()
}

func requestLog(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		log.Debug("http request", "method", r.Method, "path", r.URL.Path, "status", rec.status,
			"duration_ms", time.Since(start).Milliseconds(), "ip", clientIP(r))
	})
}

func recoverPanics(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				log.Error("panic in handler", "panic", v, "path", r.URL.Path)
				writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type identityKey struct{}

func identityFrom(ctx context.Context) auth.Identity {
	id, _ := ctx.Value(identityKey{}).(auth.Identity)
	return id
}

// requireAuth resolves the session cookie and, for state-changing methods, requires the
// session's CSRF token in the X-CSRF-Token header.
func (a *API) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthenticated", "authentication required")
			return
		}
		id, err := a.auth.Authenticate(r.Context(), c.Value)
		if err != nil {
			if errors.Is(err, domain.ErrUnauthenticated) {
				a.clearSessionCookie(w, r)
			}
			writeDomainError(w, a.log, err)
			return
		}
		if !isSafeMethod(r.Method) && !auth.ValidCSRF(id.Session, r.Header.Get(csrfHeader)) {
			writeError(w, http.StatusForbidden, "csrf_invalid", "missing or invalid CSRF token")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), identityKey{}, id)))
	}
}

func isSafeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

func (a *API) setSessionCookie(w http.ResponseWriter, r *http.Request, token string, maxAge time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/",
		MaxAge: int(maxAge.Seconds()), HttpOnly: true, Secure: r.TLS != nil || a.secureCookie, SameSite: http.SameSiteStrictMode,
	})
}

func (a *API) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/",
		MaxAge: -1, HttpOnly: true, Secure: r.TLS != nil || a.secureCookie, SameSite: http.SameSiteStrictMode,
	})
}

// clientIP is the TCP peer address. The panel is not behind a trusted proxy, so
// forwarding headers are deliberately ignored.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
