package game

import (
	"context"
	"fmt"
	"strconv"
	"sync"
)

// A MemoryStore keeps sessions in memory.
//
// It is for tests and for driving a game without a database. It is not a
// deployment option: nothing here survives the process.
type MemoryStore struct {
	mu       sync.Mutex
	sessions map[string]Session
	nextID   int64
}

// NewMemoryStore returns an empty store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{sessions: make(map[string]Session)}
}

// Create assigns an identifier and stores the session at version 1.
func (m *MemoryStore) Create(_ context.Context, session Session) (Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.nextID++
	session.ID = strconv.FormatInt(m.nextID, 10)
	session.Version = 1

	m.sessions[session.ID] = clone(session)
	return session, nil
}

// Load returns a copy of the stored session.
func (m *MemoryStore) Load(_ context.Context, id string) (Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	session, ok := m.sessions[id]
	if !ok {
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
	if !ok {
		return Session{}, fmt.Errorf("%s: %w", session.ID, ErrSessionNotFound)
	}
	if stored.Version != session.Version {
		return Session{}, fmt.Errorf("%s: stored version %d, wrote against %d: %w",
			session.ID, stored.Version, session.Version, ErrVersionConflict)
	}

	session.Version++
	m.sessions[session.ID] = clone(session)
	return session, nil
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
