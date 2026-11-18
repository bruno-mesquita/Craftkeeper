package httpapi

import (
	"crypto/tls"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bruno-mesquita/mesquita-panel/internal/domain"
)

// The session cookie is Secure under TLS, or always when the panel is configured to sit
// behind a TLS-terminating proxy (SECURE_COOKIE).
func TestSessionCookieSecure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		secure bool
		tls    bool
		want   bool
	}{
		{"plain http", false, false, false},
		{"tls", false, true, true},
		{"proxy config", true, false, true},
	} {
		a := &API{secureCookie: tc.secure}
		req := httptest.NewRequest("POST", "/api/v1/auth/login", nil)
		if tc.tls {
			req.TLS = &tls.ConnectionState{}
		}
		for op, set := range map[string]func(*httptest.ResponseRecorder){
			"set":   func(w *httptest.ResponseRecorder) { a.setSessionCookie(w, req, "t", time.Hour) },
			"clear": func(w *httptest.ResponseRecorder) { a.clearSessionCookie(w, req) },
		} {
			w := httptest.NewRecorder()
			set(w)
			cookies := w.Result().Cookies()
			if len(cookies) != 1 || cookies[0].Secure != tc.want || !cookies[0].HttpOnly {
				t.Errorf("%s/%s: cookies = %+v, want Secure=%v", tc.name, op, cookies, tc.want)
			}
		}
	}
}

func TestCrashDTO(t *testing.T) {
	if got := toCrashDTO(domain.Server{}); got != nil {
		t.Fatalf("no crash = %+v", got)
	}
	at := time.Date(2026, 11, 18, 12, 30, 0, 0, time.UTC)
	got := toCrashDTO(domain.Server{CrashedAt: at, CrashRestarts: 3})
	if got == nil || got.At != "2026-11-18T12:30:00Z" || got.Restarts != 3 {
		t.Fatalf("crash = %+v", got)
	}
}
