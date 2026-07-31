package game

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/maloquacious/zmachine"
)

// Errors a caller is expected to handle.
var (
	// ErrSessionNotFound means no session exists under that identifier.
	ErrSessionNotFound = errors.New("game: session not found")

	// ErrVersionConflict means the stored session moved on between the read
	// and the write: another turn got there first. The turn that lost is
	// refused rather than replayed, because the player issued it against the
	// state they could see.
	ErrVersionConflict = errors.New("game: session changed since it was read")

	// ErrGameOver means the story ended itself and there is nothing left to
	// resume. Start a new game.
	ErrGameOver = errors.New("game: the story has ended")

	// ErrStoryUnavailable means the session belongs to a story this binary
	// does not carry. It is a deployment problem, not a damaged session.
	ErrStoryUnavailable = errors.New("game: story not in this library")
)

// A Session is one game in progress: which story it belongs to, the bytes that
// resume it, and enough bookkeeping to detect a lost update.
//
// State is opaque. Nothing here parses it, and the score and room come from
// [zmachine.Result.StatusLine] rather than from these bytes.
type Session struct {
	ID string

	// UserID owns the session. Every read and every write is scoped to it, so
	// an identifier a browser supplies can only reach that browser's games.
	UserID string

	// StoryKey identifies the exact story image State was written from. A
	// state only restores into a machine built from that file.
	StoryKey StoryKey

	// State resumes the session. It is nil once Halted is true, which is the
	// one time nil is a legitimate thing to store.
	State []byte

	// Turn counts the commands this application has played, which is not the
	// story's own move counter.
	Turn int

	// Version is bumped by every successful write and is what makes a
	// conditional update possible. A caller does not set it.
	Version int64

	// Halted records that the story ended itself. A halted session cannot be
	// played again.
	Halted bool
}

// A Store holds sessions durably.
//
// It stores State as opaque bytes: it must not compress it, parse it, or write
// anything derived from it. Implementations may be used from several goroutines
// at once.
// Every lookup is scoped to the owning user. A session that belongs to somebody
// else is reported as ErrSessionNotFound rather than as a refusal, because
// distinguishing the two would let a stranger count the games on the server.
type Store interface {
	// Create stores a new session and returns it with ID and Version
	// assigned by the store. session.UserID must be set.
	Create(ctx context.Context, session Session) (Session, error)

	// Load returns the user's session, or ErrSessionNotFound.
	Load(ctx context.Context, userID, id string) (Session, error)

	// Update writes the session only if it still belongs to session.UserID and
	// the stored version still matches session.Version, and returns it with
	// the new version. It returns ErrVersionConflict if the stored version has
	// moved, and ErrSessionNotFound if the session is gone.
	Update(ctx context.Context, session Session) (Session, error)
}

// A Service plays turns against stored sessions.
//
// It is the whole read-run-write cycle with nothing above it: a request handler
// is this with an HTTP request in front. One Service serves every player and is
// safe for concurrent use.
type Service struct {
	library *Library
	runner  *Runner
	store   Store
	locks   keyedMutex
}

// NewService returns a Service. All three arguments are required.
func NewService(library *Library, runner *Runner, store Store) (*Service, error) {
	if library == nil {
		return nil, errors.New("game: service: nil library")
	}
	if runner == nil {
		return nil, errors.New("game: service: nil runner")
	}
	if store == nil {
		return nil, errors.New("game: service: nil store")
	}
	return &Service{library: library, runner: runner, store: store}, nil
}

// NewGame starts a story and stores the state its opening turn produced.
//
// The returned Result holds the banner and opening room; the returned Session
// is what later turns are played against.
func (s *Service) NewGame(ctx context.Context, userID, storyID string) (Session, zmachine.Result, error) {
	if userID == "" {
		return Session{}, zmachine.Result{}, errors.New("game: new game: no user")
	}

	entry, ok := s.library.ByID(storyID)
	if !ok {
		return Session{}, zmachine.Result{}, fmt.Errorf("game: %s: %w", storyID, ErrStoryUnavailable)
	}

	result, err := s.runner.Start(ctx, entry)
	if err != nil {
		return Session{}, zmachine.Result{}, err
	}

	session, err := s.store.Create(ctx, Session{
		UserID:   userID,
		StoryKey: entry.Key,
		State:    result.State,
		Halted:   result.Status == zmachine.Halted,
	})
	if err != nil {
		return Session{}, zmachine.Result{}, fmt.Errorf("game: %s: create session: %w", storyID, err)
	}

	return session, result, nil
}

// Play runs one command against a stored session and stores what it produced.
//
// The session is locked for the whole read-run-write cycle, so two commands
// submitted at once are played one after the other rather than both against the
// same state. The lock covers this process; the conditional write covers the
// rest, and a turn that loses the race is refused with ErrVersionConflict rather
// than replayed against a state the player never saw.
//
// A failed turn writes nothing. The session returned with the error is the
// zero value; the stored one is still the good one, and the command may be
// tried again if [Classify] says the fault is retryable.
func (s *Service) Play(ctx context.Context, userID, sessionID, command string) (Session, zmachine.Result, error) {
	if userID == "" {
		return Session{}, zmachine.Result{}, errors.New("game: play: no user")
	}

	unlock := s.locks.lock(sessionID)
	defer unlock()

	session, err := s.store.Load(ctx, userID, sessionID)
	if err != nil {
		return Session{}, zmachine.Result{}, fmt.Errorf("game: session %s: load: %w", sessionID, err)
	}
	if session.Halted {
		return Session{}, zmachine.Result{}, fmt.Errorf("game: session %s: %w", sessionID, ErrGameOver)
	}

	entry, ok := s.library.ByKey(session.StoryKey)
	if !ok {
		return Session{}, zmachine.Result{}, fmt.Errorf("game: session %s: story %s: %w",
			sessionID, session.StoryKey, ErrStoryUnavailable)
	}

	result, err := s.runner.Run(ctx, entry, session.State, command)
	if err != nil {
		return Session{}, zmachine.Result{}, err
	}

	// A halted story returns no state, and storing that nil is the one time
	// replacing a good state with nothing is right: the session is over and
	// there is nothing left to resume.
	session.State = result.State
	session.Halted = result.Status == zmachine.Halted
	session.Turn++

	session, err = s.store.Update(ctx, session)
	if err != nil {
		return Session{}, zmachine.Result{}, fmt.Errorf("game: session %s: store turn %d: %w",
			sessionID, session.Turn, err)
	}

	return session, result, nil
}

// Session returns a stored session without playing anything, for a client that
// is reconnecting rather than issuing a command.
func (s *Service) Session(ctx context.Context, userID, sessionID string) (Session, error) {
	if userID == "" {
		return Session{}, errors.New("game: session: no user")
	}

	session, err := s.store.Load(ctx, userID, sessionID)
	if err != nil {
		return Session{}, fmt.Errorf("game: session %s: load: %w", sessionID, err)
	}
	return session, nil
}

// keyedMutex serializes work per session identifier.
//
// Entries are dropped when the last holder leaves, so a server that has seen a
// million sessions is not still holding a million mutexes.
type keyedMutex struct {
	mu    sync.Mutex
	locks map[string]*keyedLock
}

type keyedLock struct {
	mu      sync.Mutex
	holders int
}

// lock blocks until the key is free and returns the function that releases it.
func (k *keyedMutex) lock(key string) func() {
	k.mu.Lock()
	if k.locks == nil {
		k.locks = make(map[string]*keyedLock)
	}
	entry, ok := k.locks[key]
	if !ok {
		entry = &keyedLock{}
		k.locks[key] = entry
	}
	entry.holders++
	k.mu.Unlock()

	entry.mu.Lock()

	return func() {
		entry.mu.Unlock()

		k.mu.Lock()
		entry.holders--
		if entry.holders == 0 {
			delete(k.locks, key)
		}
		k.mu.Unlock()
	}
}
