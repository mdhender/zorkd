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
	saves    map[string]Snapshot
	nextID   int64
	nextSave int64
}

// NewMemoryStore returns an empty store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		sessions: make(map[string]Session),
		updated:  make(map[string]time.Time),
		saves:    make(map[string]Snapshot),
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

// Delete removes the user's game and every save hanging off it.
//
// Another user's game is reported as missing, exactly as the database reports
// it. The saves are swept here by hand because the database sweeps them with a
// foreign key, and the two stores have to behave the same.
func (m *MemoryStore) Delete(_ context.Context, userID, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	session, ok := m.sessions[id]
	if !ok || session.UserID != userID {
		return fmt.Errorf("%s: %w", id, ErrSessionNotFound)
	}

	delete(m.sessions, id)
	delete(m.updated, id)

	for saveID, stored := range m.saves {
		if stored.GameID == id {
			delete(m.saves, saveID)
		}
	}

	return nil
}

// CreateSave writes a snapshot under its name, replacing one the game already
// holds under that name.
func (m *MemoryStore) CreateSave(_ context.Context, snapshot Snapshot) (Save, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.owns(snapshot.UserID, snapshot.GameID); err != nil {
		return Save{}, false, err
	}

	snapshot.CreatedAt = time.Now()

	for id, stored := range m.saves {
		if stored.GameID != snapshot.GameID || !sameSaveName(stored.Name, snapshot.Name) {
			continue
		}
		snapshot.ID = id
		m.saves[id] = cloneSnapshot(snapshot)
		return summarize(snapshot), true, nil
	}

	held := 0
	for _, stored := range m.saves {
		if stored.GameID == snapshot.GameID {
			held++
		}
	}
	if held >= MaxSavesPerGame {
		return Save{}, false, fmt.Errorf("%s: %d saves: %w", snapshot.GameID, held, ErrTooManySaves)
	}

	m.nextSave++
	snapshot.ID = strconv.FormatInt(m.nextSave, 10)
	m.saves[snapshot.ID] = cloneSnapshot(snapshot)

	return summarize(snapshot), false, nil
}

// Saves returns the game's saves, newest first.
func (m *MemoryStore) Saves(_ context.Context, userID, gameID string) ([]Save, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.owns(userID, gameID); err != nil {
		return nil, err
	}

	var saves []Save
	for _, stored := range m.saves {
		if stored.GameID == gameID {
			saves = append(saves, summarize(stored))
		}
	}

	slices.SortFunc(saves, func(a, b Save) int {
		if c := b.CreatedAt.Compare(a.CreatedAt); c != 0 {
			return c
		}
		// Saves written within the same clock tick still need a stable order.
		return strings.Compare(b.ID, a.ID)
	})

	return saves, nil
}

// LoadSave returns one save with the bytes that restore it.
func (m *MemoryStore) LoadSave(_ context.Context, userID, gameID, saveID string) (Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.owns(userID, gameID); err != nil {
		return Snapshot{}, err
	}

	stored, ok := m.saves[saveID]
	if !ok || stored.GameID != gameID {
		return Snapshot{}, fmt.Errorf("%s: %w", saveID, ErrSaveNotFound)
	}
	return cloneSnapshot(stored), nil
}

// DeleteSave removes one save and returns what it removed.
func (m *MemoryStore) DeleteSave(_ context.Context, userID, gameID, saveID string) (Save, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.owns(userID, gameID); err != nil {
		return Save{}, err
	}

	stored, ok := m.saves[saveID]
	if !ok || stored.GameID != gameID {
		return Save{}, fmt.Errorf("%s: %w", saveID, ErrSaveNotFound)
	}
	delete(m.saves, saveID)

	return summarize(stored), nil
}

// owns reports whether the game exists and belongs to the user, with the lock
// already held. A game that is somebody else's reads as missing, exactly as the
// database reports it.
func (m *MemoryStore) owns(userID, gameID string) error {
	session, ok := m.sessions[gameID]
	if !ok || session.UserID != userID {
		return fmt.Errorf("%s: %w", gameID, ErrSessionNotFound)
	}
	return nil
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

// cloneSnapshot copies a save's state for the same reason clone does.
func cloneSnapshot(snapshot Snapshot) Snapshot {
	if snapshot.State != nil {
		state := make([]byte, len(snapshot.State))
		copy(state, snapshot.State)
		snapshot.State = state
	}
	return snapshot
}

// summarize is the listing view of a save: everything but the bytes.
func summarize(snapshot Snapshot) Save {
	return Save{
		ID:        snapshot.ID,
		Name:      snapshot.Name,
		Turn:      snapshot.Turn,
		CreatedAt: snapshot.CreatedAt,
	}
}
