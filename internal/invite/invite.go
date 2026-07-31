// Package invite gates registration.
//
// An account is created only by somebody holding an invitation, and only under
// the address the invitation was issued for. It knows nothing about HTTP:
// answering "may this address register, and with what password?" is all it
// does, and drawing the form that asks is
// [github.com/mdhender/zorkd/internal/httpserver]'s job.
package invite

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/mdhender/zorkd/internal/auth"
)

// DefaultInvitationTTL is how long an invitation can be redeemed for.
//
// It is also the grace period a redeemed invitation is kept for; see [Service.Sweep].
const DefaultInvitationTTL = 48 * time.Hour

// tokenBytes is the length of an invitation token before encoding. 256 bits of
// randomness is not guessable, which is the whole of the token's security: it
// is the one thing standing between a stranger and an account on this server.
const tokenBytes = 32

// ErrNotInvited is the single answer to every invitation that cannot be used:
// unknown, expired, already redeemed, or issued for a different address.
//
// They are deliberately not distinguished. Tokens are unguessable, so telling
// them apart gives an attacker nothing — but it gives a legitimate holder of a
// spent link nothing either, so it is worth deciding rather than falling out of
// whichever check happened to run first.
var ErrNotInvited = errors.New("invite: no usable invitation")

// An Invitation is permission for one address to register.
//
// The token is not here. Only the link handed to a person holds it; the store
// keeps its hash.
type Invitation struct {
	// Email is the address the invitation was issued for, in the normalized
	// form [auth.NormalizeEmail] produces and users.email holds.
	Email string

	// ExpiresAt is when the invitation stops being redeemable.
	ExpiresAt time.Time

	// RedeemedAt is when the invitation was spent, or the zero time while it is
	// still unspent.
	RedeemedAt time.Time

	// UserID is the account the invitation created, once it has been redeemed,
	// and empty until then.
	UserID string
}

// A Store holds invitations durably.
//
// Invitations are keyed by the SHA-256 of the token rather than by the token,
// so reading the store gives up nothing that can be redeemed.
//
// Implementations may be used from several goroutines at once.
type Store interface {
	// CreateInvitation stores an invitation under the token's hash.
	CreateInvitation(ctx context.Context, tokenHash []byte, invitation Invitation) error

	// InvitationByToken returns the invitation stored under the token's hash,
	// or ErrNotInvited. Expiry is not its business; the Service keeps the clock.
	InvitationByToken(ctx context.Context, tokenHash []byte) (Invitation, error)

	// Redeem marks the invitation spent and creates the account it was issued
	// for, in one transaction.
	//
	// The invitation must be unredeemed at at, unexpired at at, and issued for
	// record.Email; an implementation checks all three itself rather than
	// trusting a caller that checked them a moment earlier, because that moment
	// is exactly where two registrations racing on one token would both get
	// through. It returns ErrNotInvited when the invitation cannot be used and
	// [auth.ErrEmailTaken] when the address already has an account.
	Redeem(ctx context.Context, tokenHash []byte, at time.Time, record auth.Record) (auth.User, error)

	// DeleteSpentInvitations removes unredeemed invitations that expired before
	// expiredBefore and redeemed ones spent before redeemedBefore, and reports
	// how many it removed.
	DeleteSpentInvitations(ctx context.Context, expiredBefore, redeemedBefore time.Time) (int, error)
}

// A Service issues and redeems invitations.
//
// It owns the clock: the store keeps expiry times, and the Service decides
// whether one has passed.
//
// One Service serves every request and is safe for concurrent use.
type Service struct {
	store Store
	ttl   time.Duration
}

// An Option configures a Service.
type Option func(*Service)

// WithTTL sets how long an issued invitation can be redeemed for, and how long
// a redeemed one is kept before the sweep collects it.
func WithTTL(ttl time.Duration) Option {
	return func(s *Service) { s.ttl = ttl }
}

// NewService returns a Service over the store.
func NewService(store Store, opts ...Option) (*Service, error) {
	if store == nil {
		return nil, errors.New("invite: service: nil store")
	}

	s := &Service{store: store, ttl: DefaultInvitationTTL}
	for _, opt := range opts {
		opt(s)
	}
	if s.ttl <= 0 {
		return nil, errors.New("invite: service: ttl must be positive")
	}

	return s, nil
}

// Create issues an invitation for the address and returns the token.
//
// The token is generated here and returned once. Nothing else in the process
// keeps it, and the store never sees it: a lost invitation is reissued rather
// than looked up.
//
// The address is normalized here, so a typo is refused while whoever is issuing
// the invitation is still looking at it rather than when the invited person
// cannot register.
func (s *Service) Create(ctx context.Context, email string) (string, error) {
	address, err := auth.NormalizeEmail(email)
	if err != nil {
		return "", fmt.Errorf("invite: create: %w", err)
	}

	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("invite: create: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)

	invitation := Invitation{Email: address, ExpiresAt: time.Now().Add(s.ttl)}
	if err := s.store.CreateInvitation(ctx, hashToken(token), invitation); err != nil {
		return "", fmt.Errorf("invite: create: %w", err)
	}

	return token, nil
}

// Invited returns the invitation the token names, if it can still be redeemed,
// and otherwise ErrNotInvited.
//
// It answers the registration form: nobody should fill in a form that was never
// going to be accepted. It is a courtesy and not the gate — [Service.Redeem]
// checks again, and must not trust that this ran.
func (s *Service) Invited(ctx context.Context, token string) (Invitation, error) {
	if token == "" {
		return Invitation{}, ErrNotInvited
	}

	invitation, err := s.store.InvitationByToken(ctx, hashToken(token))
	if err != nil {
		if errors.Is(err, ErrNotInvited) {
			return Invitation{}, ErrNotInvited
		}
		return Invitation{}, fmt.Errorf("invite: lookup: %w", err)
	}
	if !redeemable(invitation, time.Now()) {
		return Invitation{}, ErrNotInvited
	}

	return invitation, nil
}

// Redeem spends the invitation and creates the account it was issued for.
//
// The order matters. The token and the address are checked before the password
// is hashed, so a request carrying no usable invitation is refused without
// spending an Argon2id verification on it. That does make a bad token
// measurably faster than a good one, which is fine: the token is not something
// anyone is going to find by probing.
//
// Everything the invitation could be wrong about — unknown, expired, already
// redeemed, or issued for another address — is reported as ErrNotInvited. A
// password that is too short is [auth.ErrWeakPassword] and an address that
// already has an account is [auth.ErrEmailTaken], because those are the
// player's own to fix.
func (s *Service) Redeem(ctx context.Context, token, email, password string) (auth.User, error) {
	if token == "" {
		return auth.User{}, ErrNotInvited
	}
	hash := hashToken(token)

	invitation, err := s.store.InvitationByToken(ctx, hash)
	if err != nil {
		if errors.Is(err, ErrNotInvited) {
			return auth.User{}, ErrNotInvited
		}
		return auth.User{}, fmt.Errorf("invite: redeem: %w", err)
	}

	at := time.Now()
	if !redeemable(invitation, at) {
		return auth.User{}, ErrNotInvited
	}

	// Both sides are normalized, so an address that differs only in case or in
	// surrounding space is the same invitation rather than a mismatch nobody
	// can see. One that will not normalize cannot equal the invited address
	// either, and gets the same single refusal.
	address, err := auth.NormalizeEmail(email)
	if err != nil || address != invitation.Email {
		return auth.User{}, ErrNotInvited
	}

	passwordHash, err := auth.HashPassword(password)
	if err != nil {
		return auth.User{}, err
	}

	user, err := s.store.Redeem(ctx, hash, at, auth.Record{
		User:         auth.User{Email: address},
		PasswordHash: passwordHash,
	})
	if err != nil {
		if errors.Is(err, ErrNotInvited) {
			// Another registration took this invitation between the lookup
			// above and the write. The store settles that race, not this check.
			return auth.User{}, ErrNotInvited
		}
		return auth.User{}, fmt.Errorf("invite: redeem: %w", err)
	}

	return user, nil
}

// Sweep removes invitations nobody can use any more and reports how many went.
//
// Redeemed invitations are kept for the TTL rather than deleted the moment they
// are spent. Deleting one immediately would mean a player reloading their own
// registration link is told the invitation is unknown, and it would mean the
// account the invitation created is recorded and then destroyed before anything
// could read it.
func (s *Service) Sweep(ctx context.Context) (int, error) {
	now := time.Now()

	n, err := s.store.DeleteSpentInvitations(ctx, now, now.Add(-s.ttl))
	if err != nil {
		return 0, fmt.Errorf("invite: sweep: %w", err)
	}
	return n, nil
}

// redeemable reports whether the invitation may still be spent at at.
func redeemable(invitation Invitation, at time.Time) bool {
	return invitation.RedeemedAt.IsZero() && at.Before(invitation.ExpiresAt)
}

// hashToken returns what the store keys an invitation by.
func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}
