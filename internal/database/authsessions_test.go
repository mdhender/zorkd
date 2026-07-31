package database

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mdhender/zorkd/internal/auth"
	"github.com/mdhender/zorkd/internal/game"
	"github.com/mdhender/zorkd/internal/session"
)

func tokenHash(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

func TestAuthSessionsRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	owner := testUser(t, db, "player@example.com")
	sessions := db.AuthSessions()

	expires := time.Now().Add(time.Hour).Round(0)
	hash := tokenHash("a token")

	if err := sessions.CreateSession(ctx, hash, session.Session{UserID: owner, ExpiresAt: expires}); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}

	stored, err := sessions.SessionByToken(ctx, hash)
	if err != nil {
		t.Fatalf("SessionByToken() error = %v", err)
	}
	if stored.UserID != owner {
		t.Errorf("UserID = %q, want %q", stored.UserID, owner)
	}
	if !stored.ExpiresAt.Equal(expires) {
		t.Errorf("ExpiresAt = %v, want %v", stored.ExpiresAt, expires)
	}

	if err := sessions.DeleteSession(ctx, hash); err != nil {
		t.Fatalf("DeleteSession() error = %v", err)
	}
	if _, err := sessions.SessionByToken(ctx, hash); !errors.Is(err, session.ErrNoSession) {
		t.Errorf("SessionByToken() after delete error = %v, want %v", err, session.ErrNoSession)
	}

	// Logging out twice is not a failure.
	if err := sessions.DeleteSession(ctx, hash); err != nil {
		t.Errorf("DeleteSession() on a missing session error = %v", err)
	}
}

// The token itself is never written. A reader of this table cannot log in as
// anybody.
func TestAuthSessionsStoreOnlyTheHash(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	owner := testUser(t, db, "player@example.com")

	const token = "a-token-nobody-else-should-learn"

	err := db.AuthSessions().CreateSession(ctx, tokenHash(token),
		session.Session{UserID: owner, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}

	if got := pragma(t, db, "SELECT count(*) FROM auth_sessions WHERE instr(cast(token_hash AS TEXT), 'a-token') > 0;"); got != "0" {
		t.Error("the session token was stored alongside its hash")
	}
	if got := pragma(t, db, "SELECT length(token_hash) FROM auth_sessions;"); got != "32" {
		t.Errorf("length(token_hash) = %q, want 32", got)
	}
}

func TestAuthSessionsSweepExpired(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	owner := testUser(t, db, "player@example.com")
	sessions := db.AuthSessions()

	now := time.Now()
	live := tokenHash("live")
	dead := tokenHash("dead")

	for _, tt := range []struct {
		hash    []byte
		expires time.Time
	}{
		{live, now.Add(time.Hour)},
		{dead, now.Add(-time.Hour)},
	} {
		if err := sessions.CreateSession(ctx, tt.hash, session.Session{UserID: owner, ExpiresAt: tt.expires}); err != nil {
			t.Fatalf("CreateSession() error = %v", err)
		}
	}

	removed, err := sessions.DeleteExpiredSessions(ctx, now)
	if err != nil {
		t.Fatalf("DeleteExpiredSessions() error = %v", err)
	}
	if removed != 1 {
		t.Errorf("removed %d sessions, want 1", removed)
	}

	if _, err := sessions.SessionByToken(ctx, dead); !errors.Is(err, session.ErrNoSession) {
		t.Errorf("the expired session survived: %v", err)
	}
	if _, err := sessions.SessionByToken(ctx, live); err != nil {
		t.Errorf("the sweep took a live session: %v", err)
	}
}

// The whole path a request will take: log in, carry the cookie, be recognized,
// and reach one's own game and nobody else's.
func TestLoginReachesOnlyItsOwnGames(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)

	accounts, err := auth.NewService(db.Users())
	if err != nil {
		t.Fatalf("auth.NewService() error = %v", err)
	}
	manager, err := session.NewManager(db.AuthSessions())
	if err != nil {
		t.Fatalf("session.NewManager() error = %v", err)
	}
	library, err := game.Embedded()
	if err != nil {
		t.Fatalf("Embedded() error = %v", err)
	}
	games, err := game.NewService(library, game.NewRunner(), db.Sessions())
	if err != nil {
		t.Fatalf("game.NewService() error = %v", err)
	}

	if _, err := accounts.Register(ctx, "player@example.com", "a good long password"); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	// Log in.
	user, err := accounts.Authenticate(ctx, "player@example.com", "a good long password")
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	w := httptest.NewRecorder()
	if err := manager.Start(ctx, w, user.ID); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	// Arrive again, carrying the cookie.
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, cookie := range w.Result().Cookies() {
		r.AddCookie(cookie)
	}
	userID, err := manager.UserID(ctx, r)
	if err != nil {
		t.Fatalf("UserID() error = %v", err)
	}
	if userID != user.ID {
		t.Fatalf("UserID() = %q, want %q", userID, user.ID)
	}

	// Play a turn as that user.
	mine, _, err := games.NewGame(ctx, userID, "zork1")
	if err != nil {
		t.Fatalf("NewGame() error = %v", err)
	}
	if _, err := games.Play(ctx, userID, mine.ID, "open mailbox"); err != nil {
		t.Fatalf("Play() error = %v", err)
	}

	// Somebody else holding the identifier gets nowhere with it.
	stranger := testUser(t, db, "stranger@example.com")
	if _, err := games.Play(ctx, stranger, mine.ID, "north"); !errors.Is(err, game.ErrSessionNotFound) {
		t.Errorf("Play() as another user error = %v, want %v", err, game.ErrSessionNotFound)
	}

	// And a request with no cookie is nobody.
	if _, err := manager.UserID(ctx, httptest.NewRequest(http.MethodGet, "/", nil)); !errors.Is(err, session.ErrNoSession) {
		t.Errorf("UserID() without a cookie error = %v, want %v", err, session.ErrNoSession)
	}
}

// A session belongs to an account, and it goes when the account does.
func TestAuthSessionsRequireAUser(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	sessions := db.AuthSessions()

	err := sessions.CreateSession(ctx, tokenHash("a token"),
		session.Session{UserID: "999", ExpiresAt: time.Now().Add(time.Hour)})
	if err == nil {
		t.Fatal("CreateSession() for an account that does not exist = nil error, want the foreign key to refuse it")
	}
}
