// Package session holds browser sessions: the cookie a logged-in player
// carries and the record it points at.
//
// Game sessions are a different thing entirely and live in
// [github.com/mdhender/zorkd/internal/game]. This package answers one question:
// which user is making this request?
package session

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// CookieName is the name of the session cookie.
const CookieName = "zorkd_session"

// DefaultTTL is how long a session lasts without being renewed.
const DefaultTTL = 14 * 24 * time.Hour

// tokenBytes is the length of a session token before encoding. 256 bits of
// randomness is not guessable, and the cookie is the only thing standing
// between a stranger and somebody's game.
const tokenBytes = 32

// ErrNoSession means the request carries no usable session: no cookie, an
// unknown token, or one that has expired. A caller treats all three the same
// way — the player is not logged in.
var ErrNoSession = errors.New("session: no session")

// A Session is a logged-in browser.
//
// The token is not here. Only the browser holds it; the store keeps its hash.
type Session struct {
	UserID    string
	ExpiresAt time.Time
}

// A Store holds sessions durably.
//
// Sessions are keyed by the SHA-256 of the token rather than by the token, so
// that reading the store gives up nothing that can be replayed.
//
// Implementations may be used from several goroutines at once.
type Store interface {
	// CreateSession stores a session under the token's hash.
	CreateSession(ctx context.Context, tokenHash []byte, session Session) error

	// SessionByToken returns the session stored under the token's hash, or
	// ErrNoSession. Expiry is not its business; the Manager keeps the clock.
	SessionByToken(ctx context.Context, tokenHash []byte) (Session, error)

	// DeleteSession removes the session, if it is there. Removing one that is
	// already gone is not an error: logging out twice is not a failure.
	DeleteSession(ctx context.Context, tokenHash []byte) error

	// DeleteExpiredSessions removes every session that expired before the
	// given time and reports how many it removed.
	DeleteExpiredSessions(ctx context.Context, before time.Time) (int, error)
}

// A Manager issues and reads session cookies.
//
// It owns the clock: the store keeps expiry times, and the Manager decides
// whether one has passed.
type Manager struct {
	store  Store
	ttl    time.Duration
	secure bool
}

// An Option configures a Manager.
type Option func(*Manager)

// WithTTL sets how long an issued session lasts.
func WithTTL(ttl time.Duration) Option {
	return func(m *Manager) { m.ttl = ttl }
}

// WithInsecureCookies drops the Secure attribute so cookies survive plain HTTP.
//
// It exists for local development. A deployment that serves over HTTPS must not
// use it, which is why the default is the safe one and the unsafe setting has
// to be asked for by name.
func WithInsecureCookies() Option {
	return func(m *Manager) { m.secure = false }
}

// NewManager returns a Manager over the store.
func NewManager(store Store, opts ...Option) (*Manager, error) {
	if store == nil {
		return nil, errors.New("session: manager: nil store")
	}

	m := &Manager{store: store, ttl: DefaultTTL, secure: true}
	for _, opt := range opts {
		opt(m)
	}
	if m.ttl <= 0 {
		return nil, errors.New("session: manager: ttl must be positive")
	}

	return m, nil
}

// Start logs the user in: it stores a new session and sets the cookie.
//
// The token is generated here and written only to the response. Nothing else in
// the process keeps it, and the store never sees it.
func (m *Manager) Start(ctx context.Context, w http.ResponseWriter, userID string) error {
	if userID == "" {
		return errors.New("session: start: no user")
	}

	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return fmt.Errorf("session: start: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)

	expires := time.Now().Add(m.ttl)
	if err := m.store.CreateSession(ctx, hashToken(token), Session{UserID: userID, ExpiresAt: expires}); err != nil {
		return fmt.Errorf("session: start: %w", err)
	}

	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		MaxAge:   int(m.ttl.Seconds()),
		HttpOnly: true,
		Secure:   m.secure,
		// Lax lets a player follow a link into the game and still be logged
		// in, while keeping the cookie off cross-site form posts.
		SameSite: http.SameSiteLaxMode,
	})

	return nil
}

// UserID returns the user the request is authenticated as, or ErrNoSession.
//
// An expired session is deleted on the way past, so an abandoned browser
// cleans up after itself the next time it calls.
func (m *Manager) UserID(ctx context.Context, r *http.Request) (string, error) {
	cookie, err := r.Cookie(CookieName)
	if err != nil || cookie.Value == "" {
		return "", ErrNoSession
	}

	hash := hashToken(cookie.Value)

	session, err := m.store.SessionByToken(ctx, hash)
	if err != nil {
		if errors.Is(err, ErrNoSession) {
			return "", ErrNoSession
		}
		return "", fmt.Errorf("session: lookup: %w", err)
	}

	if !time.Now().Before(session.ExpiresAt) {
		if err := m.store.DeleteSession(ctx, hash); err != nil {
			return "", fmt.Errorf("session: delete expired: %w", err)
		}
		return "", ErrNoSession
	}

	return session.UserID, nil
}

// End logs the browser out: it deletes the stored session and clears the
// cookie.
//
// The cookie is cleared whether or not there was a session to delete, so a
// player who arrives with a stale cookie leaves without one.
func (m *Manager) End(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
	defer m.clear(w)

	cookie, err := r.Cookie(CookieName)
	if err != nil || cookie.Value == "" {
		return nil
	}

	if err := m.store.DeleteSession(ctx, hashToken(cookie.Value)); err != nil {
		return fmt.Errorf("session: end: %w", err)
	}
	return nil
}

// clear tells the browser to drop the cookie. The attributes have to match the
// ones it was set with, or the browser keeps the original.
func (m *Manager) clear(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   m.secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// Sweep removes sessions that have expired and reports how many went.
//
// Expired sessions are already refused, so this is housekeeping rather than
// enforcement: it keeps the table from growing without bound.
func (m *Manager) Sweep(ctx context.Context) (int, error) {
	n, err := m.store.DeleteExpiredSessions(ctx, time.Now())
	if err != nil {
		return 0, fmt.Errorf("session: sweep: %w", err)
	}
	return n, nil
}

// hashToken returns what the store keys a session by.
func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}
