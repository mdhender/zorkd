package session

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// memoryStore is enough of a Store to test the Manager. The real one is in
// internal/database.
type memoryStore struct {
	mu       sync.Mutex
	sessions map[string]Session
}

func newMemoryStore() *memoryStore {
	return &memoryStore{sessions: make(map[string]Session)}
}

func (m *memoryStore) CreateSession(_ context.Context, tokenHash []byte, s Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.sessions[string(tokenHash)] = s
	return nil
}

func (m *memoryStore) SessionByToken(_ context.Context, tokenHash []byte) (Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[string(tokenHash)]
	if !ok {
		return Session{}, ErrNoSession
	}
	return s, nil
}

func (m *memoryStore) DeleteSession(_ context.Context, tokenHash []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.sessions, string(tokenHash))
	return nil
}

func (m *memoryStore) DeleteExpiredSessions(_ context.Context, before time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	removed := 0
	for key, s := range m.sessions {
		if s.ExpiresAt.Before(before) {
			delete(m.sessions, key)
			removed++
		}
	}
	return removed, nil
}

func (m *memoryStore) len() int {
	m.mu.Lock()
	defer m.mu.Unlock()

	return len(m.sessions)
}

func testManager(t *testing.T, opts ...Option) (*Manager, *memoryStore) {
	t.Helper()

	store := newMemoryStore()
	manager, err := NewManager(store, opts...)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	return manager, store
}

// sessionCookie returns the cookie the response set, and fails if it set none.
func sessionCookie(t *testing.T, w *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()

	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == CookieName {
			return cookie
		}
	}
	t.Fatalf("no %s cookie in %v", CookieName, w.Result().Header)
	return nil
}

func TestStartIssuesASession(t *testing.T) {
	manager, store := testManager(t)
	w := httptest.NewRecorder()

	if err := manager.Start(t.Context(), w, "7"); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if store.len() != 1 {
		t.Fatalf("store holds %d sessions, want 1", store.len())
	}

	cookie := sessionCookie(t, w)

	if !cookie.HttpOnly {
		t.Error("the session cookie is readable from JavaScript")
	}
	if !cookie.Secure {
		t.Error("the session cookie is sent over plain HTTP")
	}
	if cookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite = %v, want %v", cookie.SameSite, http.SameSiteLaxMode)
	}
	if cookie.Path != "/" {
		t.Errorf("Path = %q, want %q", cookie.Path, "/")
	}
	if cookie.MaxAge <= 0 {
		t.Errorf("MaxAge = %d, want an expiring cookie", cookie.MaxAge)
	}

	// The token is 256 bits of randomness, not a user identifier.
	raw, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	if err != nil {
		t.Fatalf("the token is not base64: %v", err)
	}
	if len(raw) != tokenBytes {
		t.Errorf("token = %d bytes, want %d", len(raw), tokenBytes)
	}

	// The store keeps the hash. The token itself is only in the cookie.
	if _, ok := store.sessions[cookie.Value]; ok {
		t.Error("the store is keyed by the token rather than by its hash")
	}
	if _, err := store.SessionByToken(t.Context(), hashToken(cookie.Value)); err != nil {
		t.Errorf("SessionByToken() error = %v", err)
	}
}

func TestUserIDReadsTheSession(t *testing.T) {
	manager, _ := testManager(t)

	w := httptest.NewRecorder()
	if err := manager.Start(t.Context(), w, "7"); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(sessionCookie(t, w))

	userID, err := manager.UserID(t.Context(), r)
	if err != nil {
		t.Fatalf("UserID() error = %v", err)
	}
	if userID != "7" {
		t.Errorf("UserID() = %q, want %q", userID, "7")
	}
}

// Every way of arriving without a usable session is the same answer: not logged
// in.
func TestUserIDRefusesWhatItShould(t *testing.T) {
	manager, store := testManager(t)

	w := httptest.NewRecorder()
	if err := manager.Start(t.Context(), w, "7"); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	issued := sessionCookie(t, w)

	tests := []struct {
		name   string
		cookie *http.Cookie
	}{
		{"no cookie", nil},
		{"an empty cookie", &http.Cookie{Name: CookieName, Value: ""}},
		{"a token nobody issued", &http.Cookie{Name: CookieName, Value: "not-a-real-token"}},
		{"a different cookie", &http.Cookie{Name: "something_else", Value: issued.Value}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.cookie != nil {
				r.AddCookie(tt.cookie)
			}
			if _, err := manager.UserID(t.Context(), r); !errors.Is(err, ErrNoSession) {
				t.Errorf("UserID() error = %v, want %v", err, ErrNoSession)
			}
		})
	}

	// The real session is still there; none of the above disturbed it.
	if store.len() != 1 {
		t.Errorf("store holds %d sessions, want 1", store.len())
	}
}

// An expired session is refused and cleared away, so an abandoned browser tidies
// up after itself.
func TestUserIDRefusesAnExpiredSession(t *testing.T) {
	manager, store := testManager(t, WithTTL(time.Millisecond))

	w := httptest.NewRecorder()
	if err := manager.Start(t.Context(), w, "7"); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	time.Sleep(5 * time.Millisecond)

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(sessionCookie(t, w))

	if _, err := manager.UserID(t.Context(), r); !errors.Is(err, ErrNoSession) {
		t.Errorf("UserID() error = %v, want %v", err, ErrNoSession)
	}
	if store.len() != 0 {
		t.Errorf("store holds %d sessions, want the expired one gone", store.len())
	}
}

// Logging out must invalidate the session on the server, not merely ask the
// browser to forget it: a copied cookie has to stop working.
func TestEndInvalidatesTheSession(t *testing.T) {
	manager, store := testManager(t)

	started := httptest.NewRecorder()
	if err := manager.Start(t.Context(), started, "7"); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	issued := sessionCookie(t, started)

	r := httptest.NewRequest(http.MethodGet, "/logout", nil)
	r.AddCookie(issued)

	ended := httptest.NewRecorder()
	if err := manager.End(t.Context(), ended, r); err != nil {
		t.Fatalf("End() error = %v", err)
	}

	if store.len() != 0 {
		t.Errorf("store holds %d sessions after logout, want 0", store.len())
	}

	cleared := sessionCookie(t, ended)
	if cleared.Value != "" || cleared.MaxAge >= 0 {
		t.Errorf("the logout cookie = %+v, want an expired empty one", cleared)
	}

	// The old cookie is now worth nothing.
	again := httptest.NewRequest(http.MethodGet, "/", nil)
	again.AddCookie(issued)
	if _, err := manager.UserID(t.Context(), again); !errors.Is(err, ErrNoSession) {
		t.Errorf("UserID() with the logged-out token error = %v, want %v", err, ErrNoSession)
	}
}

// Logging out without a session still clears the cookie, so a player who
// arrives with a stale one leaves without it.
func TestEndWithoutASession(t *testing.T) {
	manager, _ := testManager(t)

	w := httptest.NewRecorder()
	if err := manager.End(t.Context(), w, httptest.NewRequest(http.MethodGet, "/logout", nil)); err != nil {
		t.Fatalf("End() error = %v", err)
	}
	if got := sessionCookie(t, w); got.Value != "" {
		t.Errorf("cookie = %q, want it cleared", got.Value)
	}
}

// Two sessions are two tokens. A player logged in on a phone and a laptop is
// still one account.
func TestSessionsAreDistinct(t *testing.T) {
	manager, store := testManager(t)

	first := httptest.NewRecorder()
	second := httptest.NewRecorder()
	if err := manager.Start(t.Context(), first, "7"); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err := manager.Start(t.Context(), second, "7"); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	if sessionCookie(t, first).Value == sessionCookie(t, second).Value {
		t.Fatal("two sessions were issued the same token")
	}
	if store.len() != 2 {
		t.Errorf("store holds %d sessions, want 2", store.len())
	}
}

func TestSweepRemovesExpiredSessions(t *testing.T) {
	manager, store := testManager(t)

	if err := store.CreateSession(t.Context(), []byte("live"), Session{UserID: "7", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	if err := store.CreateSession(t.Context(), []byte("dead"), Session{UserID: "7", ExpiresAt: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}

	removed, err := manager.Sweep(t.Context())
	if err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	if removed != 1 || store.len() != 1 {
		t.Errorf("Sweep() removed %d, leaving %d; want 1 and 1", removed, store.len())
	}
}

func TestInsecureCookiesAreAskedForByName(t *testing.T) {
	manager, _ := testManager(t, WithInsecureCookies())

	w := httptest.NewRecorder()
	if err := manager.Start(t.Context(), w, "7"); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if sessionCookie(t, w).Secure {
		t.Error("WithInsecureCookies() left the Secure attribute set")
	}
}

func TestNewManagerValidatesItsArguments(t *testing.T) {
	if _, err := NewManager(nil); err == nil {
		t.Error("NewManager(nil) = nil error, want failure")
	}
	if _, err := NewManager(newMemoryStore(), WithTTL(0)); err == nil {
		t.Error("NewManager() with a zero ttl = nil error, want failure")
	}
	if _, err := NewManager(newMemoryStore(), WithTTL(-time.Hour)); err == nil {
		t.Error("NewManager() with a negative ttl = nil error, want failure")
	}
}

func TestStartRequiresAUser(t *testing.T) {
	manager, store := testManager(t)

	if err := manager.Start(t.Context(), httptest.NewRecorder(), ""); err == nil {
		t.Error("Start() with no user = nil error, want failure")
	}
	if store.len() != 0 {
		t.Error("a session was stored for nobody")
	}
}
