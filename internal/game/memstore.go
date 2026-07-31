package game

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// A MemoryStore keeps sessions in memory.
//
// It is for tests and for driving a game without a database. It is not a
// deployment option: nothing here survives the process.
type MemoryStore struct {
	mu       sync.Mutex
	sessions map[string]Session
	updated  map[string]time.Time
	nextID   int64
}

// NewMemoryStore returns an empty store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		sessions: make(map[string]Session),
		updated:  make(map[string]time.Time),
	}
}

// Create assigns an identifier and stores the session at version 1.
func (m *MemoryStore) Create(_ context.Context, session Session) (Session, error) {
	if session.UserID == "" {
		return Session{}, fmt.Errorf("create: no user")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.nextID++
	session.ID = strconv.FormatInt(m.nextID, 10)
	session.Version = 1

	m.sessions[session.ID] = clone(session)
	m.updated[session.ID] = time.Now()

	return session, nil
}

// Load returns a copy of the user's session.
//
// Another user's session is reported as missing, exactly as the database
// reports it: the answer must not tell one player that another player's game
// exists.
func (m *MemoryStore) Load(_ context.Context, userID, id string) (Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	session, ok := m.sessions[id]
	if !ok || session.UserID != userID {
		return Session{}, fmt.Errorf("%s: %w", id, ErrSessionNotFound)
	}
	return clone(session), nil
}

// Update writes the session only if the stored version still matches, which is
// what a real database does with a version column in the WHERE clause.
func (m *MemoryStore) Update(_ context.Context, session Session) (Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	stored, ok := m.sessions[session.ID]
	if !ok || stored.UserID != session.UserID {
		return Session{}, fmt.Errorf("%s: %w", session.ID, ErrSessionNotFound)
	}
	if stored.Version != session.Version {
		return Session{}, fmt.Errorf("%s: stored version %d, wrote against %d: %w",
			session.ID, stored.Version, session.Version, ErrVersionConflict)
	}

	session.Version++
	m.sessions[session.ID] = clone(session)
	m.updated[session.ID] = time.Now()

	return session, nil
}

// List returns the user's games, most recently played first.
func (m *MemoryStore) List(_ context.Context, userID string) ([]Summary, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var summaries []Summary
	for id, session := range m.sessions {
		if session.UserID != userID {
			continue
		}
		summaries = append(summaries, Summary{
			ID:        id,
			StoryKey:  session.StoryKey,
			Turn:      session.Turn,
			Halted:    session.Halted,
			UpdatedAt: m.updated[id],
		})
	}

	slices.SortFunc(summaries, func(a, b Summary) int {
		if c := b.UpdatedAt.Compare(a.UpdatedAt); c != 0 {
			return c
		}
		// Games stored within the same clock tick still need a stable order.
		return strings.Compare(b.ID, a.ID)
	})

	return summaries, nil
}

// Len reports how many sessions the store holds.
func (m *MemoryStore) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()

	return len(m.sessions)
}

// clone copies the state so that a caller holding the slice cannot change what
// is stored, and a later turn cannot change what a caller was handed. A real
// database gets this for free.
func clone(session Session) Session {
	if session.State != nil {
		state := make([]byte, len(session.State))
		copy(state, session.State)
		session.State = state
	}
	return session
}
