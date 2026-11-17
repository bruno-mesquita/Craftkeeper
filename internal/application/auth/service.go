package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bruno-mesquita/mesquita-panel/internal/domain"
)

const (
	loginWindow          = 15 * time.Minute
	maxFailuresPerIP     = 20
	maxFailuresPerUser   = 5
	limiterMaxKeys       = 10_000
	sessionTouchInterval = time.Minute
	// userAgentMaxLen truncates the stored User-Agent header.
	userAgentMaxLen = 256
)

// Config holds the service's non-port settings.
type Config struct {
	// SetupToken must be presented to complete initial setup. Empty disables setup.
	SetupToken string
	// Now overrides the clock in tests. Defaults to time.Now.
	Now func() time.Time
}

type Service struct {
	users    UserRepository
	sessions SessionRepository
	hasher   PasswordHasher
	audit    AuditRecorder

	setupToken string
	now        func() time.Time
	dummyHash  string

	byIP   *limiter
	byUser *limiter
}

func NewService(users UserRepository, sessions SessionRepository, hasher PasswordHasher, audit AuditRecorder, cfg Config) (*Service, error) {
	// Verifying against a real hash for unknown usernames keeps login timing uniform.
	dummy, err := hasher.Hash(rand.Text())
	if err != nil {
		return nil, fmt.Errorf("auth: prepare dummy hash: %w", err)
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Service{
		users: users, sessions: sessions, hasher: hasher, audit: audit,
		setupToken: cfg.SetupToken,
		now:        now,
		dummyHash:  dummy,
		byIP:       newLimiter(maxFailuresPerIP, loginWindow, limiterMaxKeys),
		byUser:     newLimiter(maxFailuresPerUser, loginWindow, limiterMaxKeys),
	}, nil
}

// Identity is an authenticated user together with their active session.
type Identity struct {
	User    domain.User
	Session domain.Session
}

func (s *Service) SetupRequired(ctx context.Context) (bool, error) {
	done, err := s.users.SetupCompleted(ctx)
	return !done, err
}

type SetupInput struct {
	Token    string
	Username string
	Password string
	IP       string
}

// CompleteSetup creates the first administrator. It is only possible once.
func (s *Service) CompleteSetup(ctx context.Context, in SetupInput) (domain.User, error) {
	if done, err := s.users.SetupCompleted(ctx); err != nil {
		return domain.User{}, err
	} else if done {
		return domain.User{}, domain.ErrSetupCompleted
	}
	if s.setupToken == "" || subtle.ConstantTimeCompare([]byte(strings.TrimSpace(in.Token)), []byte(s.setupToken)) != 1 {
		return domain.User{}, domain.ErrSetupTokenInvalid
	}
	if err := domain.ValidateUsername(in.Username); err != nil {
		return domain.User{}, err
	}
	if err := domain.ValidatePassword("password", in.Password); err != nil {
		return domain.User{}, err
	}
	hash, err := s.hasher.Hash(in.Password)
	if err != nil {
		return domain.User{}, err
	}
	now := s.now().UTC().Truncate(time.Second)
	u := domain.User{ID: rand.Text(), Username: in.Username, PasswordHash: hash, Role: domain.RoleAdmin, CreatedAt: now, UpdatedAt: now}
	if err := s.users.CreateInitialAdmin(ctx, u); err != nil {
		return domain.User{}, err
	}
	s.record(ctx, domain.AuditEvent{UserID: u.ID, Action: domain.AuditSetupCompleted, Target: u.Username, IP: in.IP, Success: true})
	return u, nil
}

type LoginInput struct {
	Username  string
	Password  string
	IP        string
	UserAgent string
}

// Login verifies credentials and returns the raw session token (only ever sent in the cookie).
func (s *Service) Login(ctx context.Context, in LoginInput) (token string, id Identity, err error) {
	now := s.now()
	userKey := strings.ToLower(in.Username)
	// Rejected attempts are not audited: an attacker could otherwise grow the audit log at will.
	if !s.byIP.Allowed(in.IP, now) || !s.byUser.Allowed(userKey, now) {
		return "", Identity{}, domain.ErrRateLimited
	}
	if len(in.Password) > domain.PasswordMaxLen || len(in.Username) > 64 {
		return "", Identity{}, domain.ErrInvalidCredentials
	}

	u, err := s.users.ByUsername(ctx, in.Username)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		_, _ = s.hasher.Verify(in.Password, s.dummyHash)
		return "", Identity{}, s.loginFailed(ctx, in, userKey, now)
	case err != nil:
		return "", Identity{}, err
	}
	ok, err := s.hasher.Verify(in.Password, u.PasswordHash)
	if err != nil {
		return "", Identity{}, err
	}
	if !ok {
		return "", Identity{}, s.loginFailed(ctx, in, userKey, now)
	}
	s.byUser.Reset(userKey)

	// Opportunistic cleanup instead of a background goroutine.
	if err := s.sessions.DeleteExpired(ctx, now); err != nil {
		return "", Identity{}, err
	}

	token = rand.Text()
	t := now.UTC().Truncate(time.Second)
	sess := domain.Session{
		TokenHash:  hashToken(token),
		UserID:     u.ID,
		CSRFToken:  rand.Text(),
		CreatedAt:  t,
		LastSeenAt: t,
		ExpiresAt:  t.Add(domain.SessionAbsoluteTTL),
		IP:         in.IP,
		UserAgent:  truncate(in.UserAgent, userAgentMaxLen),
	}
	if err := s.sessions.Create(ctx, sess); err != nil {
		return "", Identity{}, err
	}
	s.record(ctx, domain.AuditEvent{UserID: u.ID, Action: domain.AuditLogin, Target: u.Username, IP: in.IP, Success: true})
	return token, Identity{User: u, Session: sess}, nil
}

func (s *Service) loginFailed(ctx context.Context, in LoginInput, userKey string, now time.Time) error {
	s.byIP.Fail(in.IP, now)
	s.byUser.Fail(userKey, now)
	s.record(ctx, domain.AuditEvent{Action: domain.AuditLogin, Target: in.Username, IP: in.IP, Success: false})
	return domain.ErrInvalidCredentials
}

// Authenticate resolves a raw session token to an identity, refreshing its idle timer.
func (s *Service) Authenticate(ctx context.Context, token string) (Identity, error) {
	if token == "" || len(token) > 128 {
		return Identity{}, domain.ErrUnauthenticated
	}
	now := s.now()
	sess, err := s.sessions.ByTokenHash(ctx, hashToken(token))
	if errors.Is(err, domain.ErrNotFound) {
		return Identity{}, domain.ErrUnauthenticated
	} else if err != nil {
		return Identity{}, err
	}
	if !sess.ActiveAt(now) {
		_ = s.sessions.Delete(ctx, sess.TokenHash)
		return Identity{}, domain.ErrUnauthenticated
	}
	u, err := s.users.ByID(ctx, sess.UserID)
	if errors.Is(err, domain.ErrNotFound) {
		return Identity{}, domain.ErrUnauthenticated
	} else if err != nil {
		return Identity{}, err
	}
	// Limit write amplification: refresh last_seen at most once per interval.
	if now.Sub(sess.LastSeenAt) >= sessionTouchInterval {
		t := now.UTC().Truncate(time.Second)
		if err := s.sessions.Touch(ctx, sess.TokenHash, t); err != nil {
			return Identity{}, err
		}
		sess.LastSeenAt = t
	}
	return Identity{User: u, Session: sess}, nil
}

func (s *Service) Logout(ctx context.Context, id Identity, ip string) error {
	if err := s.sessions.Delete(ctx, id.Session.TokenHash); err != nil {
		return err
	}
	s.record(ctx, domain.AuditEvent{UserID: id.User.ID, Action: domain.AuditLogout, Target: id.User.Username, IP: ip, Success: true})
	return nil
}

type ChangePasswordInput struct {
	CurrentPassword string
	NewPassword     string
	IP              string
}

// ChangePassword verifies the current password, stores the new hash and ends every other session.
func (s *Service) ChangePassword(ctx context.Context, id Identity, in ChangePasswordInput) error {
	if err := domain.ValidatePassword("new_password", in.NewPassword); err != nil {
		return err
	}
	if len(in.CurrentPassword) > domain.PasswordMaxLen {
		return domain.ErrInvalidCredentials
	}
	ok, err := s.hasher.Verify(in.CurrentPassword, id.User.PasswordHash)
	if err != nil {
		return err
	}
	if !ok {
		s.record(ctx, domain.AuditEvent{UserID: id.User.ID, Action: domain.AuditPasswordChanged, Target: id.User.Username, IP: in.IP, Success: false})
		return domain.ErrInvalidCredentials
	}
	hash, err := s.hasher.Hash(in.NewPassword)
	if err != nil {
		return err
	}
	if err := s.users.UpdatePassword(ctx, id.User.ID, hash, s.now().UTC().Truncate(time.Second)); err != nil {
		return err
	}
	if err := s.sessions.DeleteOthersForUser(ctx, id.User.ID, id.Session.TokenHash); err != nil {
		return err
	}
	s.record(ctx, domain.AuditEvent{UserID: id.User.ID, Action: domain.AuditPasswordChanged, Target: id.User.Username, IP: in.IP, Success: true})
	return nil
}

// ValidCSRF compares a submitted CSRF token with the session's in constant time.
func ValidCSRF(sess domain.Session, submitted string) bool {
	return submitted != "" && subtle.ConstantTimeCompare([]byte(submitted), []byte(sess.CSRFToken)) == 1
}

// record writes an audit event. Audit failures must not break the user-facing
// operation, but they are surfaced through the recorder's own logging.
func (s *Service) record(ctx context.Context, e domain.AuditEvent) {
	e.CreatedAt = s.now().UTC().Truncate(time.Second)
	_ = s.audit.Record(ctx, e)
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
