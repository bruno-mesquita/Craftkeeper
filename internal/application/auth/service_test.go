package auth_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/bruno-mesquita/Craftkeeper/internal/application/auth"
	"github.com/bruno-mesquita/Craftkeeper/internal/domain"
	"github.com/bruno-mesquita/Craftkeeper/internal/infrastructure/password"
	"github.com/bruno-mesquita/Craftkeeper/internal/infrastructure/sqlite"
	"github.com/bruno-mesquita/Craftkeeper/migrations"
)

const (
	setupToken = "SETUPTOKEN"
	adminPass  = "a-very-long-password"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newService(t *testing.T) (*auth.Service, *clock) {
	t.Helper()
	store, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "db.sqlite"), migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	clk := &clock{t: time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc, err := auth.NewService(sqlite.NewUsers(store), sqlite.NewSessions(store),
		password.NewArgon2id(password.Params{MemoryKiB: 64, Iterations: 1, Threads: 1}),
		sqlite.NewAuditLog(store, log), auth.Config{SetupToken: setupToken, Now: clk.Now})
	if err != nil {
		t.Fatal(err)
	}
	return svc, clk
}

func setup(t *testing.T, svc *auth.Service) {
	t.Helper()
	if _, err := svc.CompleteSetup(context.Background(), auth.SetupInput{Token: setupToken, Username: "admin", Password: adminPass}); err != nil {
		t.Fatalf("setup: %v", err)
	}
}

func login(t *testing.T, svc *auth.Service, pw string) (string, auth.Identity, error) {
	t.Helper()
	return svc.Login(context.Background(), auth.LoginInput{Username: "admin", Password: pw, IP: "127.0.0.1"})
}

func TestSetupHappensOnlyOnceAndNeedsToken(t *testing.T) {
	svc, _ := newService(t)
	ctx := context.Background()

	if req, _ := svc.SetupRequired(ctx); !req {
		t.Fatal("setup should be required on a fresh database")
	}
	if _, err := svc.CompleteSetup(ctx, auth.SetupInput{Token: "wrong", Username: "admin", Password: adminPass}); !errors.Is(err, domain.ErrSetupTokenInvalid) {
		t.Fatalf("wrong token: got %v", err)
	}
	var ve *domain.ValidationError
	if _, err := svc.CompleteSetup(ctx, auth.SetupInput{Token: setupToken, Username: "admin", Password: "short"}); !errors.As(err, &ve) || ve.Field != "password" {
		t.Fatalf("short password: got %v", err)
	}
	setup(t, svc)
	if req, _ := svc.SetupRequired(ctx); req {
		t.Fatal("setup should no longer be required")
	}
	if _, err := svc.CompleteSetup(ctx, auth.SetupInput{Token: setupToken, Username: "other", Password: adminPass}); !errors.Is(err, domain.ErrSetupCompleted) {
		t.Fatalf("second setup: got %v", err)
	}
}

func TestLoginAuthenticateLogout(t *testing.T) {
	svc, _ := newService(t)
	setup(t, svc)
	ctx := context.Background()

	if _, _, err := login(t, svc, "wrong-password-123"); !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("wrong password: got %v", err)
	}
	if _, _, err := svc.Login(ctx, auth.LoginInput{Username: "nobody", Password: adminPass, IP: "127.0.0.1"}); !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("unknown user: got %v", err)
	}

	token, id, err := login(t, svc, adminPass)
	if err != nil {
		t.Fatal(err)
	}
	if id.Session.TokenHash == token {
		t.Fatal("raw token must not be stored")
	}
	got, err := svc.Authenticate(ctx, token)
	if err != nil || got.User.Username != "admin" {
		t.Fatalf("authenticate: %v %+v", err, got.User)
	}
	if !auth.ValidCSRF(got.Session, id.Session.CSRFToken) || auth.ValidCSRF(got.Session, "") || auth.ValidCSRF(got.Session, "x") {
		t.Fatal("CSRF validation mismatch")
	}
	if err := svc.Logout(ctx, got, "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(ctx, token); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("after logout: got %v", err)
	}
}

func TestLoginIsCaseInsensitiveOnUsername(t *testing.T) {
	svc, _ := newService(t)
	setup(t, svc)
	if _, _, err := svc.Login(context.Background(), auth.LoginInput{Username: "ADMIN", Password: adminPass, IP: "127.0.0.1"}); err != nil {
		t.Fatal(err)
	}
}

func TestSessionExpiry(t *testing.T) {
	svc, clk := newService(t)
	setup(t, svc)
	ctx := context.Background()

	token, _, err := login(t, svc, adminPass)
	if err != nil {
		t.Fatal(err)
	}
	clk.Advance(domain.SessionIdleTTL + time.Second)
	if _, err := svc.Authenticate(ctx, token); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("idle session: got %v", err)
	}

	token, _, _ = login(t, svc, adminPass)
	// Stay active, but exceed the absolute lifetime.
	for elapsed := time.Duration(0); elapsed <= domain.SessionAbsoluteTTL; elapsed += 12 * time.Hour {
		clk.Advance(12 * time.Hour)
		_, err = svc.Authenticate(ctx, token)
	}
	if !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("absolute expiry: got %v", err)
	}
}

func TestLoginRateLimit(t *testing.T) {
	svc, clk := newService(t)
	setup(t, svc)
	for range 5 {
		login(t, svc, "wrong-password-123")
	}
	if _, _, err := login(t, svc, adminPass); !errors.Is(err, domain.ErrRateLimited) {
		t.Fatalf("expected rate limit even with the right password, got %v", err)
	}
	clk.Advance(16 * time.Minute)
	if _, _, err := login(t, svc, adminPass); err != nil {
		t.Fatalf("after window: %v", err)
	}
}

func TestChangePasswordRevokesOtherSessions(t *testing.T) {
	svc, _ := newService(t)
	setup(t, svc)
	ctx := context.Background()

	tokenA, _, _ := login(t, svc, adminPass)
	tokenB, _, _ := login(t, svc, adminPass)
	idA, err := svc.Authenticate(ctx, tokenA)
	if err != nil {
		t.Fatal(err)
	}

	if err := svc.ChangePassword(ctx, idA, auth.ChangePasswordInput{CurrentPassword: "wrong-password-123", NewPassword: "another-long-password"}); !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("wrong current: got %v", err)
	}
	if err := svc.ChangePassword(ctx, idA, auth.ChangePasswordInput{CurrentPassword: adminPass, NewPassword: "another-long-password"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(ctx, tokenA); err != nil {
		t.Fatalf("current session must survive: %v", err)
	}
	if _, err := svc.Authenticate(ctx, tokenB); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("other session must be revoked: %v", err)
	}
	if _, _, err := login(t, svc, "another-long-password"); err != nil {
		t.Fatalf("login with new password: %v", err)
	}
}
