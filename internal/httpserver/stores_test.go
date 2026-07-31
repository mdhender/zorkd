package httpserver

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/mdhender/zorkd/internal/auth"
	"github.com/mdhender/zorkd/internal/invite"
	"github.com/mdhender/zorkd/internal/session"
)

// The in-memory stores these tests run against. The real ones are in
// internal/database, which has its own tests; what is under test here is the
// server above them.

type accountStore struct {
	mu      sync.Mutex
	byEmail map[string]auth.Record
	byID    map[string]auth.Record
	nextID  int64
}

func newAccountStore() *accountStore {
	return &accountStore{byEmail: make(map[string]auth.Record), byID: make(map[string]auth.Record)}
}

func (a *accountStore) CreateUser(_ context.Context, record auth.Record) (auth.User, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if _, ok := a.byEmail[record.Email]; ok {
		return auth.User{}, auth.ErrEmailTaken
	}

	a.nextID++
	record.ID = strconv.FormatInt(a.nextID, 10)
	a.byEmail[record.Email] = record
	a.byID[record.ID] = record

	return record.User, nil
}

func (a *accountStore) UserByEmail(_ context.Context, email string) (auth.Record, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	record, ok := a.byEmail[email]
	if !ok {
		return auth.Record{}, fmt.Errorf("%q: %w", email, auth.ErrUserNotFound)
	}
	return record, nil
}

func (a *accountStore) UserByID(_ context.Context, id string) (auth.User, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	record, ok := a.byID[id]
	if !ok {
		return auth.User{}, fmt.Errorf("%s: %w", id, auth.ErrUserNotFound)
	}
	return record.User, nil
}

type sessionStore struct {
	mu       sync.Mutex
	sessions map[string]session.Session
}

func newSessionStore() *sessionStore {
	return &sessionStore{sessions: make(map[string]session.Session)}
}

func (s *sessionStore) CreateSession(_ context.Context, tokenHash []byte, stored session.Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.sessions[string(tokenHash)] = stored
	return nil
}

func (s *sessionStore) SessionByToken(_ context.Context, tokenHash []byte) (session.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	stored, ok := s.sessions[string(tokenHash)]
	if !ok {
		return session.Session{}, session.ErrNoSession
	}
	return stored, nil
}

func (s *sessionStore) DeleteSession(_ context.Context, tokenHash []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.sessions, string(tokenHash))
	return nil
}

func (s *sessionStore) DeleteExpiredSessions(_ context.Context, before time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	removed := 0
	for key, stored := range s.sessions {
		if stored.ExpiresAt.Before(before) {
			delete(s.sessions, key)
			removed++
		}
	}
	return removed, nil
}

// invitationStore is the in-memory half of the registration gate.
//
// It writes accounts into the account store because redeeming an invitation and
// creating the account it was issued for are one operation: everything Redeem
// does happens under one lock, which is this store's version of the transaction
// the SQLite one takes.
type invitationStore struct {
	mu          sync.Mutex
	accounts    *accountStore
	invitations map[string]invite.Invitation
}

func newInvitationStore(accounts *accountStore) *invitationStore {
	return &invitationStore{accounts: accounts, invitations: make(map[string]invite.Invitation)}
}

func (i *invitationStore) CreateInvitation(_ context.Context, tokenHash []byte, invitation invite.Invitation) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	i.invitations[string(tokenHash)] = invitation
	return nil
}

func (i *invitationStore) InvitationByToken(_ context.Context, tokenHash []byte) (invite.Invitation, error) {
	i.mu.Lock()
	defer i.mu.Unlock()

	stored, ok := i.invitations[string(tokenHash)]
	if !ok {
		return invite.Invitation{}, invite.ErrNotInvited
	}
	return stored, nil
}

func (i *invitationStore) Redeem(ctx context.Context, tokenHash []byte, at time.Time, record auth.Record) (auth.User, error) {
	i.mu.Lock()
	defer i.mu.Unlock()

	stored, ok := i.invitations[string(tokenHash)]
	if !ok || !stored.RedeemedAt.IsZero() || !at.Before(stored.ExpiresAt) || stored.Email != record.Email {
		return auth.User{}, invite.ErrNotInvited
	}

	user, err := i.accounts.CreateUser(ctx, record)
	if err != nil {
		return auth.User{}, err
	}

	stored.RedeemedAt = at
	stored.UserID = user.ID
	i.invitations[string(tokenHash)] = stored

	return user, nil
}

func (i *invitationStore) DeleteSpentInvitations(_ context.Context, expiredBefore, redeemedBefore time.Time) (int, error) {
	i.mu.Lock()
	defer i.mu.Unlock()

	removed := 0
	for key, stored := range i.invitations {
		spent := stored.RedeemedAt.IsZero() && stored.ExpiresAt.Before(expiredBefore)
		spent = spent || (!stored.RedeemedAt.IsZero() && stored.RedeemedAt.Before(redeemedBefore))
		if spent {
			delete(i.invitations, key)
			removed++
		}
	}
	return removed, nil
}
