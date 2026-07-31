package invite

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/mdhender/zorkd/internal/auth"
)

// memStore is an in-memory [Store]. The durable one is in internal/database,
// which has its own tests; what is under test here is the service above it.
//
// Everything Redeem does happens under one lock, which is this store's version
// of the transaction the SQLite one takes.
type memStore struct {
	mu          sync.Mutex
	invitations map[string]Invitation
	accounts    map[string]auth.User
	nextID      int64
}

func newMemStore() *memStore {
	return &memStore{
		invitations: make(map[string]Invitation),
		accounts:    make(map[string]auth.User),
	}
}

func (m *memStore) CreateInvitation(_ context.Context, tokenHash []byte, invitation Invitation) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.invitations[string(tokenHash)] = invitation
	return nil
}

func (m *memStore) InvitationByToken(_ context.Context, tokenHash []byte) (Invitation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	stored, ok := m.invitations[string(tokenHash)]
	if !ok {
		return Invitation{}, ErrNotInvited
	}
	return stored, nil
}

func (m *memStore) Redeem(_ context.Context, tokenHash []byte, at time.Time, record auth.Record) (auth.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	stored, ok := m.invitations[string(tokenHash)]
	if !ok || !stored.RedeemedAt.IsZero() || !at.Before(stored.ExpiresAt) || stored.Email != record.Email {
		return auth.User{}, ErrNotInvited
	}
	if _, taken := m.accounts[record.Email]; taken {
		return auth.User{}, auth.ErrEmailTaken
	}

	m.nextID++
	user := auth.User{ID: strconv.FormatInt(m.nextID, 10), Email: record.Email}
	m.accounts[record.Email] = user

	stored.RedeemedAt = at
	stored.UserID = user.ID
	m.invitations[string(tokenHash)] = stored

	return user, nil
}

func (m *memStore) DeleteSpentInvitations(_ context.Context, expiredBefore, redeemedBefore time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	removed := 0
	for key, stored := range m.invitations {
		spent := stored.RedeemedAt.IsZero() && stored.ExpiresAt.Before(expiredBefore)
		spent = spent || (!stored.RedeemedAt.IsZero() && stored.RedeemedAt.Before(redeemedBefore))
		if spent {
			delete(m.invitations, key)
			removed++
		}
	}
	return removed, nil
}

// put writes an invitation the service could not have created, so that a test
// can hold one that is expired or already spent without waiting for it.
func (m *memStore) put(token string, invitation Invitation) {
	m.mu.Lock()
	defer m.mu.Unlock()

	sum := sha256.Sum256([]byte(token))
	m.invitations[string(sum[:])] = invitation
}

func (m *memStore) held(token string) Invitation {
	m.mu.Lock()
	defer m.mu.Unlock()

	sum := sha256.Sum256([]byte(token))
	return m.invitations[string(sum[:])]
}

func (m *memStore) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()

	return len(m.invitations)
}

func testService(t *testing.T, store Store, opts ...Option) *Service {
	t.Helper()

	service, err := NewService(store, opts...)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return service
}

const goodPassword = "a good long password"

func TestCreateThenRedeem(t *testing.T) {
	ctx := context.Background()
	store := newMemStore()
	service := testService(t, store)

	token, err := service.Create(ctx, "player@example.com")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	invitation, err := service.Invited(ctx, token)
	if err != nil {
		t.Fatalf("Invited() error = %v", err)
	}
	if invitation.Email != "player@example.com" {
		t.Errorf("Email = %q, want %q", invitation.Email, "player@example.com")
	}
	if !invitation.RedeemedAt.IsZero() {
		t.Error("a fresh invitation reads as redeemed")
	}

	user, err := service.Redeem(ctx, token, "player@example.com", goodPassword)
	if err != nil {
		t.Fatalf("Redeem() error = %v", err)
	}
	if user.ID == "" || user.Email != "player@example.com" {
		t.Errorf("Redeem() = %+v, want the invited account", user)
	}

	// The invitation records that it was spent, and which account it made.
	held := store.held(token)
	if held.RedeemedAt.IsZero() {
		t.Error("the invitation was not marked redeemed")
	}
	if held.UserID != user.ID {
		t.Errorf("UserID = %q, want %q", held.UserID, user.ID)
	}

	// And it is spent: it cannot be used again, and it no longer draws a form.
	if _, err := service.Invited(ctx, token); !errors.Is(err, ErrNotInvited) {
		t.Errorf("Invited() after redeeming = %v, want %v", err, ErrNotInvited)
	}
	if _, err := service.Redeem(ctx, token, "player@example.com", goodPassword); !errors.Is(err, ErrNotInvited) {
		t.Errorf("Redeem() twice = %v, want %v", err, ErrNotInvited)
	}
}

// The token is 256 bits from crypto/rand, in the shape internal/session uses,
// and the store never sees it.
func TestCreateIssuesAnUnguessableToken(t *testing.T) {
	ctx := context.Background()
	store := newMemStore()
	service := testService(t, store)

	seen := make(map[string]bool)
	for range 16 {
		token, err := service.Create(ctx, "player@example.com")
		if err != nil {
			t.Fatalf("Create() error = %v", err)
		}

		raw, err := base64.RawURLEncoding.DecodeString(token)
		if err != nil {
			t.Fatalf("the token is not raw base64url: %v", err)
		}
		if len(raw) != tokenBytes {
			t.Errorf("the token carries %d bytes, want %d", len(raw), tokenBytes)
		}
		if seen[token] {
			t.Fatal("Create() issued the same token twice")
		}
		seen[token] = true

		// The store holds the hash and nothing that reads back as the token.
		for key := range store.invitations {
			if key == token {
				t.Error("the store holds the token itself")
			}
		}
	}
}

func TestCreateNormalizesTheAddress(t *testing.T) {
	ctx := context.Background()
	store := newMemStore()
	service := testService(t, store)

	token, err := service.Create(ctx, "  Player@Example.COM ")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if got := store.held(token).Email; got != "player@example.com" {
		t.Errorf("stored address = %q, want %q", got, "player@example.com")
	}
}

// A typo is refused while whoever is issuing the invitation is still looking at
// it, rather than when the invited person cannot register.
func TestCreateRefusesAnAddressItWillNotStore(t *testing.T) {
	ctx := context.Background()
	store := newMemStore()
	service := testService(t, store)

	for _, email := range []string{"", "   ", "player", "Zork <zork@example.com>"} {
		t.Run(email, func(t *testing.T) {
			if _, err := service.Create(ctx, email); !errors.Is(err, auth.ErrInvalidEmail) {
				t.Errorf("Create(%q) = %v, want %v", email, err, auth.ErrInvalidEmail)
			}
			if store.count() != 0 {
				t.Error("a refused address still wrote a row")
			}
		})
	}
}

// Every invitation that cannot be used gets the same answer, whether it is
// missing, unknown, expired, already redeemed, or for another address.
func TestUnusableInvitationsAllReadTheSame(t *testing.T) {
	ctx := context.Background()
	store := newMemStore()
	service := testService(t, store)

	store.put("expired", Invitation{
		Email:     "player@example.com",
		ExpiresAt: time.Now().Add(-time.Minute),
	})
	store.put("redeemed", Invitation{
		Email:      "player@example.com",
		ExpiresAt:  time.Now().Add(time.Hour),
		RedeemedAt: time.Now().Add(-time.Minute),
		UserID:     "1",
	})

	live, err := service.Create(ctx, "player@example.com")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	tests := []struct {
		name  string
		token string
		email string
	}{
		{name: "no token", token: "", email: "player@example.com"},
		{name: "unknown", token: "not a token anybody issued", email: "player@example.com"},
		{name: "expired", token: "expired", email: "player@example.com"},
		{name: "redeemed", token: "redeemed", email: "player@example.com"},
		{name: "another address", token: live, email: "stranger@example.com"},
		{name: "not an address", token: live, email: "player"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := service.Redeem(ctx, tt.token, tt.email, goodPassword); !errors.Is(err, ErrNotInvited) {
				t.Errorf("Redeem() = %v, want %v", err, ErrNotInvited)
			}
		})
	}

	// The form is refused for the same set, minus the one that is only a
	// mismatch: the form does not carry an address yet.
	for _, token := range []string{"", "not a token anybody issued", "expired", "redeemed"} {
		if _, err := service.Invited(ctx, token); !errors.Is(err, ErrNotInvited) {
			t.Errorf("Invited(%q) = %v, want %v", token, err, ErrNotInvited)
		}
	}

	// And the live one still works, so the refusals above are the invitations
	// and not the gate refusing everything.
	if _, err := service.Redeem(ctx, live, "player@example.com", goodPassword); err != nil {
		t.Errorf("Redeem() with a live invitation = %v, want nil", err)
	}
}

// Both sides are normalized, so an address that differs only in case or in
// surrounding space is the same invitation rather than a mismatch nobody can
// see.
func TestRedeemAcceptsTheAddressHoweverItIsSpelled(t *testing.T) {
	ctx := context.Background()

	for _, typed := range []string{
		"player@example.com",
		"PLAYER@EXAMPLE.COM",
		"  Player@Example.Com  ",
		"\tplayer@example.com\n",
	} {
		t.Run(typed, func(t *testing.T) {
			service := testService(t, newMemStore())

			token, err := service.Create(ctx, "Player@Example.COM")
			if err != nil {
				t.Fatalf("Create() error = %v", err)
			}

			user, err := service.Redeem(ctx, token, typed, goodPassword)
			if err != nil {
				t.Fatalf("Redeem(%q) error = %v", typed, err)
			}
			if user.Email != "player@example.com" {
				t.Errorf("Email = %q, want the normalized address", user.Email)
			}
		})
	}
}

// The invitation is checked before the password is looked at, so a request
// carrying no usable one is refused without spending an Argon2id hash on it.
//
// The password here is too short to store: if it were reached, the answer would
// be ErrWeakPassword rather than ErrNotInvited.
func TestRedeemChecksTheInvitationBeforeThePassword(t *testing.T) {
	ctx := context.Background()
	service := testService(t, newMemStore())

	if _, err := service.Redeem(ctx, "not a token anybody issued", "player@example.com", "short"); !errors.Is(err, ErrNotInvited) {
		t.Errorf("Redeem() = %v, want %v", err, ErrNotInvited)
	}

	// With a usable invitation the password is reached, and the short one is
	// then the player's own to fix.
	token, err := service.Create(ctx, "player@example.com")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if _, err := service.Redeem(ctx, token, "player@example.com", "short"); !errors.Is(err, auth.ErrWeakPassword) {
		t.Errorf("Redeem() with a short password = %v, want %v", err, auth.ErrWeakPassword)
	}
}

// An address that already has an account is the player's own to fix, so it is
// reported rather than folded into the one answer an unusable invitation gets.
func TestRedeemReportsAnAddressThatIsTaken(t *testing.T) {
	ctx := context.Background()
	service := testService(t, newMemStore())

	first, err := service.Create(ctx, "player@example.com")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if _, err := service.Redeem(ctx, first, "player@example.com", goodPassword); err != nil {
		t.Fatalf("Redeem() error = %v", err)
	}

	second, err := service.Create(ctx, "player@example.com")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if _, err := service.Redeem(ctx, second, "player@example.com", goodPassword); !errors.Is(err, auth.ErrEmailTaken) {
		t.Errorf("Redeem() = %v, want %v", err, auth.ErrEmailTaken)
	}
}

func TestWithTTL(t *testing.T) {
	ctx := context.Background()
	store := newMemStore()

	before := time.Now()
	token, err := testService(t, store, WithTTL(time.Hour)).Create(ctx, "player@example.com")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	expires := store.held(token).ExpiresAt
	if expires.Before(before.Add(time.Hour)) || expires.After(time.Now().Add(time.Hour)) {
		t.Errorf("ExpiresAt = %v, want about an hour from now", expires)
	}

	// The default is the one the deployment gets, and there is no flag for it.
	if got := testService(t, store).ttl; got != DefaultInvitationTTL {
		t.Errorf("default ttl = %v, want %v", got, DefaultInvitationTTL)
	}
}

func TestNewServiceRequiresAStoreAndAPositiveTTL(t *testing.T) {
	if _, err := NewService(nil); err == nil {
		t.Error("NewService(nil) = nil error, want failure")
	}
	for _, ttl := range []time.Duration{0, -time.Hour} {
		if _, err := NewService(newMemStore(), WithTTL(ttl)); err == nil {
			t.Errorf("NewService(WithTTL(%v)) = nil error, want failure", ttl)
		}
	}
}

// The sweep collects what nobody can use any more and leaves what somebody
// still can. A redeemed invitation is kept for the grace period, so a player
// reloading their own registration link is not told it is unknown.
func TestSweep(t *testing.T) {
	ctx := context.Background()
	store := newMemStore()
	service := testService(t, store, WithTTL(time.Hour))

	now := time.Now()
	store.put("expired", Invitation{Email: "a@example.com", ExpiresAt: now.Add(-time.Minute)})
	store.put("live", Invitation{Email: "b@example.com", ExpiresAt: now.Add(time.Minute)})
	store.put("just redeemed", Invitation{
		Email:      "c@example.com",
		ExpiresAt:  now.Add(time.Minute),
		RedeemedAt: now.Add(-time.Minute),
		UserID:     "1",
	})
	store.put("long redeemed", Invitation{
		Email:      "d@example.com",
		ExpiresAt:  now.Add(time.Minute),
		RedeemedAt: now.Add(-2 * time.Hour),
		UserID:     "2",
	})

	removed, err := service.Sweep(ctx)
	if err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	if removed != 2 {
		t.Errorf("Sweep() removed %d, want 2", removed)
	}

	for _, token := range []string{"live", "just redeemed"} {
		if store.held(token).Email == "" {
			t.Errorf("the sweep took %q", token)
		}
	}
	for _, token := range []string{"expired", "long redeemed"} {
		if store.held(token).Email != "" {
			t.Errorf("the sweep left %q", token)
		}
	}

	// A second sweep with nothing to do reports nothing.
	if removed, err := service.Sweep(ctx); err != nil || removed != 0 {
		t.Errorf("Sweep() = %d, %v, want 0, nil", removed, err)
	}
}
