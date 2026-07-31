package database

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"

	"github.com/mdhender/zorkd/internal/auth"
	"github.com/mdhender/zorkd/internal/invite"
)

// testInvitations returns the durable store with a service over it, which is
// how the application reaches it.
func testInvitations(t *testing.T, db *DB, opts ...invite.Option) *invite.Service {
	t.Helper()

	service, err := invite.NewService(db.Invitations(), opts...)
	if err != nil {
		t.Fatalf("invite.NewService() error = %v", err)
	}
	return service
}

func TestInvitationsRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	invitations := db.Invitations()

	expires := time.Now().Add(time.Hour).Round(0)
	hash := tokenHash("an invitation token")

	err := invitations.CreateInvitation(ctx, hash, invite.Invitation{
		Email:     "player@example.com",
		ExpiresAt: expires,
	})
	if err != nil {
		t.Fatalf("CreateInvitation() error = %v", err)
	}

	stored, err := invitations.InvitationByToken(ctx, hash)
	if err != nil {
		t.Fatalf("InvitationByToken() error = %v", err)
	}
	if stored.Email != "player@example.com" {
		t.Errorf("Email = %q, want %q", stored.Email, "player@example.com")
	}
	if !stored.ExpiresAt.Equal(expires) {
		t.Errorf("ExpiresAt = %v, want %v", stored.ExpiresAt, expires)
	}
	if !stored.RedeemedAt.IsZero() || stored.UserID != "" {
		t.Errorf("a fresh invitation reads as redeemed: %+v", stored)
	}

	if _, err := invitations.InvitationByToken(ctx, tokenHash("a token nobody issued")); !errors.Is(err, invite.ErrNotInvited) {
		t.Errorf("InvitationByToken() for an unknown token = %v, want %v", err, invite.ErrNotInvited)
	}
}

func TestInvitationsRedeem(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	invitations := testInvitations(t, db)

	token, err := invitations.Create(ctx, "player@example.com")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	user, err := invitations.Redeem(ctx, token, "  PLAYER@Example.COM ", "a good long password")
	if err != nil {
		t.Fatalf("Redeem() error = %v", err)
	}
	if user.Email != "player@example.com" {
		t.Errorf("Email = %q, want the normalized address", user.Email)
	}

	// The account is real: it authenticates.
	accounts, err := auth.NewService(db.Users())
	if err != nil {
		t.Fatalf("auth.NewService() error = %v", err)
	}
	if _, err := accounts.Authenticate(ctx, "player@example.com", "a good long password"); err != nil {
		t.Errorf("Authenticate() error = %v", err)
	}

	// And the invitation records that it was spent, and which account it made.
	stored, err := db.Invitations().InvitationByToken(ctx, tokenHash(token))
	if err != nil {
		t.Fatalf("InvitationByToken() error = %v", err)
	}
	if stored.RedeemedAt.IsZero() {
		t.Error("the invitation was not marked redeemed")
	}
	if stored.UserID != user.ID {
		t.Errorf("UserID = %q, want %q", stored.UserID, user.ID)
	}

	if _, err := invitations.Redeem(ctx, token, "player@example.com", "a good long password"); !errors.Is(err, invite.ErrNotInvited) {
		t.Errorf("Redeem() twice = %v, want %v", err, invite.ErrNotInvited)
	}
}

// The important one: the lookup, the account and the write are one transaction,
// so two registrations racing on one token produce exactly one account.
func TestInvitationsRedeemOnlyOnceUnderRace(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	invitations := testInvitations(t, db)

	token, err := invitations.Create(ctx, "player@example.com")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	const racers = 4

	var (
		start sync.WaitGroup
		done  sync.WaitGroup
		mu    sync.Mutex
		users []auth.User
		errs  []error
	)

	start.Add(1)
	for range racers {
		done.Add(1)
		go func() {
			defer done.Done()
			start.Wait()

			user, err := invitations.Redeem(ctx, token, "player@example.com", "a good long password")

			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			users = append(users, user)
		}()
	}

	start.Done()
	done.Wait()

	if len(users) != 1 {
		t.Fatalf("%d registrations succeeded, want exactly 1 (errors: %v)", len(users), errs)
	}
	for _, err := range errs {
		if !errors.Is(err, invite.ErrNotInvited) {
			t.Errorf("a losing registration = %v, want %v", err, invite.ErrNotInvited)
		}
	}

	// One account, and the invitation names it.
	if got := countRows(t, db, "SELECT count(*) AS n FROM users;"); got != 1 {
		t.Errorf("%d accounts exist, want 1", got)
	}
	stored, err := db.Invitations().InvitationByToken(ctx, tokenHash(token))
	if err != nil {
		t.Fatalf("InvitationByToken() error = %v", err)
	}
	if stored.UserID != users[0].ID {
		t.Errorf("UserID = %q, want %q", stored.UserID, users[0].ID)
	}
}

// A failed redemption writes nothing: the account is created inside the same
// transaction that spends the invitation, so a refusal leaves neither.
func TestInvitationsRefusedRedemptionWritesNothing(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	invitations := testInvitations(t, db)

	token, err := invitations.Create(ctx, "player@example.com")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if _, err := invitations.Redeem(ctx, token, "stranger@example.com", "a good long password"); !errors.Is(err, invite.ErrNotInvited) {
		t.Fatalf("Redeem() = %v, want %v", err, invite.ErrNotInvited)
	}

	if got := countRows(t, db, "SELECT count(*) AS n FROM users;"); got != 0 {
		t.Errorf("%d accounts exist, want 0", got)
	}

	stored, err := db.Invitations().InvitationByToken(ctx, tokenHash(token))
	if err != nil {
		t.Fatalf("InvitationByToken() error = %v", err)
	}
	if !stored.RedeemedAt.IsZero() {
		t.Error("a refused redemption still spent the invitation")
	}

	// And the invitation still works for the address it was issued for.
	if _, err := invitations.Redeem(ctx, token, "player@example.com", "a good long password"); err != nil {
		t.Errorf("Redeem() with the invited address = %v, want nil", err)
	}
}

// An expired invitation is refused by the store as well as by the service. The
// service owns the clock, but the check that matters runs while the write lock
// is held.
func TestInvitationsRefuseAnExpiredToken(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	invitations := db.Invitations()

	hash := tokenHash("an expired token")
	err := invitations.CreateInvitation(ctx, hash, invite.Invitation{
		Email:     "player@example.com",
		ExpiresAt: time.Now().Add(-time.Minute),
	})
	if err != nil {
		t.Fatalf("CreateInvitation() error = %v", err)
	}

	_, err = invitations.Redeem(ctx, hash, time.Now(), auth.Record{
		User:         auth.User{Email: "player@example.com"},
		PasswordHash: "$argon2id$not-really-a-hash",
	})
	if !errors.Is(err, invite.ErrNotInvited) {
		t.Errorf("Redeem() = %v, want %v", err, invite.ErrNotInvited)
	}
	if got := countRows(t, db, "SELECT count(*) AS n FROM users;"); got != 0 {
		t.Errorf("%d accounts exist, want 0", got)
	}
}

// The database holds the hash of the token and never the token. A copy of it
// yields nothing that can be redeemed.
func TestInvitationTokenIsNowhereInTheDatabase(t *testing.T) {
	ctx := context.Background()
	path := testPath(t)
	db := openAt(t, path, true)

	token, err := testInvitations(t, db).Create(ctx, "player@example.com")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	// The write-ahead log is part of the database, so everything beside the
	// file is searched too.
	files, err := filepath.Glob(path + "*")
	if err != nil {
		t.Fatalf("Glob() error = %v", err)
	}
	if len(files) == 0 {
		t.Fatalf("no database files at %s", path)
	}

	found := false
	for _, file := range files {
		contents, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("ReadFile(%s) error = %v", file, err)
		}
		if bytes.Contains(contents, []byte(token)) {
			t.Errorf("%s holds the plaintext token", file)
		}
		// The address is stored in plaintext, deliberately: it is low-entropy
		// and enumerable, so hashing it would look like protection and provide
		// almost none, and users.email holds it plainly in the next table. It
		// is asserted here so that the search above is known to be reading the
		// bytes the invitation was written into.
		found = found || bytes.Contains(contents, []byte("player@example.com"))
	}
	if !found {
		t.Error("the invited address is in none of the database files, so this test proves nothing")
	}
}

// The sweep collects what nobody can use any more and leaves what somebody
// still can.
func TestInvitationsSweep(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	invitations := db.Invitations()

	now := time.Now()
	rows := []struct {
		token      string
		expires    time.Time
		redeemedAt time.Time
	}{
		{token: "expired", expires: now.Add(-time.Minute)},
		{token: "live", expires: now.Add(time.Hour)},
		{token: "just redeemed", expires: now.Add(time.Hour), redeemedAt: now.Add(-time.Minute)},
		{token: "long redeemed", expires: now.Add(time.Hour), redeemedAt: now.Add(-2 * time.Hour)},
		// Redeemed and since expired: the grace period runs from the
		// redemption, so this one stays until the redemption is old enough.
		{token: "redeemed and expired", expires: now.Add(-time.Minute), redeemedAt: now.Add(-time.Minute)},
	}

	for _, row := range rows {
		err := invitations.CreateInvitation(ctx, tokenHash(row.token), invite.Invitation{
			Email:     row.token + "@example.com",
			ExpiresAt: row.expires,
		})
		if err != nil {
			t.Fatalf("CreateInvitation(%q) error = %v", row.token, err)
		}
		if row.redeemedAt.IsZero() {
			continue
		}
		markRedeemed(t, db, row.token, row.redeemedAt)
	}

	removed, err := testInvitations(t, db, invite.WithTTL(time.Hour)).Sweep(ctx)
	if err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	if removed != 2 {
		t.Errorf("Sweep() removed %d, want 2", removed)
	}

	for _, token := range []string{"live", "just redeemed", "redeemed and expired"} {
		if _, err := invitations.InvitationByToken(ctx, tokenHash(token)); err != nil {
			t.Errorf("the sweep took %q: %v", token, err)
		}
	}
	for _, token := range []string{"expired", "long redeemed"} {
		if _, err := invitations.InvitationByToken(ctx, tokenHash(token)); !errors.Is(err, invite.ErrNotInvited) {
			t.Errorf("the sweep left %q", token)
		}
	}
}

// A redeemed invitation goes when the account it made does. A user's games
// already cascade, and a redeemed invitation belonging to a deleted account is
// not a record worth keeping.
func TestInvitationsCascadeWithTheAccount(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	invitations := testInvitations(t, db)

	token, err := invitations.Create(ctx, "player@example.com")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	user, err := invitations.Redeem(ctx, token, "player@example.com", "a good long password")
	if err != nil {
		t.Fatalf("Redeem() error = %v", err)
	}

	conn, release, err := db.conn(ctx)
	if err != nil {
		t.Fatalf("conn() error = %v", err)
	}
	err = sqlitex.Execute(conn, `DELETE FROM users WHERE id = ?;`,
		&sqlitex.ExecOptions{Args: []any{user.ID}})
	release()
	if err != nil {
		t.Fatalf("deleting the account error = %v", err)
	}

	if got := countRows(t, db, "SELECT count(*) AS n FROM invitations;"); got != 0 {
		t.Errorf("%d invitations survived the account, want 0", got)
	}
}

// markRedeemed spends an invitation at a chosen moment, which is how a test
// holds one whose grace period has already run out.
func markRedeemed(t *testing.T, db *DB, token string, at time.Time) {
	t.Helper()

	conn, release, err := db.conn(context.Background())
	if err != nil {
		t.Fatalf("conn() error = %v", err)
	}
	defer release()

	err = sqlitex.Execute(conn, `UPDATE invitations SET redeemed_at = ? WHERE token_hash = ?;`,
		&sqlitex.ExecOptions{Args: []any{stamp(at), tokenHash(token)}})
	if err != nil {
		t.Fatalf("marking %q redeemed error = %v", token, err)
	}
}

func countRows(t *testing.T, db *DB, query string) int {
	t.Helper()

	conn, release, err := db.conn(context.Background())
	if err != nil {
		t.Fatalf("conn() error = %v", err)
	}
	defer release()

	var n int
	err = sqlitex.Execute(conn, query, &sqlitex.ExecOptions{
		ResultFunc: func(stmt *sqlite.Stmt) error {
			n = int(stmt.GetInt64("n"))
			return nil
		},
	})
	if err != nil {
		t.Fatalf("%s error = %v", query, err)
	}
	return n
}
