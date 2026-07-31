package game

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/maloquacious/zmachine"

	"github.com/mdhender/zorkd/games"
)

func TestNewGameStoresTheOpening(t *testing.T) {
	store := NewMemoryStore()
	service := serviceOver(t, store)
	entry := testEntry(t, "zork1")

	session, result, err := service.NewGame(t.Context(), "zork1")
	if err != nil {
		t.Fatalf("NewGame() error = %v", err)
	}

	if session.ID == "" {
		t.Error("session has no identifier")
	}
	if session.StoryKey != entry.Key {
		t.Error("session records a different story than the one it was started from")
	}
	if session.Turn != 0 {
		t.Errorf("session.Turn = %d, want 0; no command has been played", session.Turn)
	}
	if session.Version == 0 {
		t.Error("session.Version = 0; a stored session must be versioned")
	}
	if session.Halted {
		t.Error("a new game is already halted")
	}
	if !bytes.Equal(session.State, result.State) {
		t.Error("the stored state is not the state the opening turn produced")
	}
	if store.Len() != 1 {
		t.Errorf("store holds %d sessions, want 1", store.Len())
	}
}

// Closing the browser must be enough. Nothing but the store survives here, and
// a fresh Service over it continues the game.
func TestSessionSurvivesARestart(t *testing.T) {
	store := NewMemoryStore()

	session, _, err := serviceOver(t, store).NewGame(t.Context(), "zork1")
	if err != nil {
		t.Fatalf("NewGame() error = %v", err)
	}
	for _, command := range []string{"open mailbox", "take leaflet", "north"} {
		if _, _, err := serviceOver(t, store).Play(t.Context(), session.ID, command); err != nil {
			t.Fatalf("Play(%q) error = %v", command, err)
		}
	}

	// A new process: new library, new runner, new service, same store.
	resumed, result, err := serviceOver(t, store).Play(t.Context(), session.ID, "inventory")
	if err != nil {
		t.Fatalf("Play() after restart error = %v", err)
	}

	if !strings.Contains(result.Output, "leaflet") {
		t.Errorf("the leaflet did not survive the restart: %q", result.Output)
	}
	if result.StatusLine.Name != "North of House" {
		t.Errorf("StatusLine.Name = %q, want %q", result.StatusLine.Name, "North of House")
	}
	if resumed.Turn != 4 {
		t.Errorf("session.Turn = %d, want 4", resumed.Turn)
	}
}

// The rule the whole failure path depends on: a turn that failed did not
// happen, so the state stored at the end of the previous turn is still the
// right one to play the next command against.
func TestFailedTurnLeavesTheStoredStateIntact(t *testing.T) {
	store := NewMemoryStore()
	service := serviceOver(t, store)

	session, _, err := service.NewGame(t.Context(), "zork1")
	if err != nil {
		t.Fatalf("NewGame() error = %v", err)
	}
	if _, _, err := service.Play(t.Context(), session.ID, "open mailbox"); err != nil {
		t.Fatalf("Play() error = %v", err)
	}

	before, err := store.Load(t.Context(), session.ID)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	// A service that cannot finish a turn, over the same store.
	broken, err := NewService(testLibrary(t), NewRunner(WithInstructionLimit(1)), store)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if _, _, err := broken.Play(t.Context(), session.ID, "read leaflet"); err == nil {
		t.Fatal("Play() = nil error, want the instruction limit")
	} else if got := Classify(err); got != FaultExecutionLimit {
		t.Fatalf("Classify() = %v, want %v", got, FaultExecutionLimit)
	}

	after, err := store.Load(t.Context(), session.ID)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if !bytes.Equal(before.State, after.State) {
		t.Error("a failed turn overwrote the stored state")
	}
	if before.Version != after.Version || before.Turn != after.Turn {
		t.Errorf("a failed turn moved the session on: %d/%d became %d/%d",
			before.Turn, before.Version, after.Turn, after.Version)
	}

	// And the session is still playable from exactly where it was.
	_, result, err := service.Play(t.Context(), session.ID, "read leaflet")
	if err != nil {
		t.Fatalf("Play() after a failed turn error = %v", err)
	}
	if !strings.Contains(result.Output, "WELCOME TO ZORK") {
		t.Errorf("the session did not resume where it was: %q", result.Output)
	}
}

// A halted story is the one time storing nil over a good state is right, and a
// halted session cannot be played again.
func TestPlayRefusesAHaltedSession(t *testing.T) {
	store := NewMemoryStore()
	service := serviceOver(t, store)

	session, _, err := service.NewGame(t.Context(), "zork1")
	if err != nil {
		t.Fatalf("NewGame() error = %v", err)
	}
	if _, _, err := service.Play(t.Context(), session.ID, "quit"); err != nil {
		t.Fatalf("Play(quit) error = %v", err)
	}
	halted, result, err := service.Play(t.Context(), session.ID, "yes")
	if err != nil {
		t.Fatalf("Play(yes) error = %v", err)
	}

	if result.Status != zmachine.Halted {
		t.Fatalf("Status = %v, want %v", result.Status, zmachine.Halted)
	}
	if !halted.Halted {
		t.Error("session.Halted = false after the story ended")
	}
	if halted.State != nil {
		t.Errorf("a halted session stored %d bytes of state", len(halted.State))
	}

	if _, _, err := service.Play(t.Context(), session.ID, "look"); !errors.Is(err, ErrGameOver) {
		t.Errorf("Play() on a halted session error = %v, want %v", err, ErrGameOver)
	}
}

func TestServiceRejectsBadRequests(t *testing.T) {
	service := serviceOver(t, NewMemoryStore())

	if _, _, err := service.NewGame(t.Context(), "zork4"); !errors.Is(err, ErrStoryUnavailable) {
		t.Errorf("NewGame(zork4) error = %v, want %v", err, ErrStoryUnavailable)
	}
	if _, _, err := service.Play(t.Context(), "1234", "look"); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("Play() on an unknown session error = %v, want %v", err, ErrSessionNotFound)
	}
	if _, err := service.Session(t.Context(), "1234"); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("Session() on an unknown session error = %v, want %v", err, ErrSessionNotFound)
	}
}

// A session whose story this binary no longer carries is a deployment problem
// and must say so, rather than being reported as damaged state.
func TestPlayReportsAMissingStory(t *testing.T) {
	store := NewMemoryStore()

	session, _, err := serviceOver(t, store).NewGame(t.Context(), "zork2")
	if err != nil {
		t.Fatalf("NewGame() error = %v", err)
	}

	only, err := NewLibrary(games.All()[:1])
	if err != nil {
		t.Fatalf("NewLibrary() error = %v", err)
	}
	service, err := NewService(only, NewRunner(), store)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	if _, _, err := service.Play(t.Context(), session.ID, "look"); !errors.Is(err, ErrStoryUnavailable) {
		t.Errorf("Play() error = %v, want %v", err, ErrStoryUnavailable)
	}
}

// The write is conditional on the state that was read, so a turn that lost the
// race is refused rather than stored over the one that won.
func TestPlayRefusesAStaleWrite(t *testing.T) {
	service, err := NewService(testLibrary(t), NewRunner(), staleStore{NewMemoryStore()})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	session, _, err := service.NewGame(t.Context(), "zork1")
	if err != nil {
		t.Fatalf("NewGame() error = %v", err)
	}

	if _, _, err := service.Play(t.Context(), session.ID, "look"); !errors.Is(err, ErrVersionConflict) {
		t.Errorf("Play() error = %v, want %v", err, ErrVersionConflict)
	}
}

// staleStore models another process having got there first: every update finds
// the version already moved.
type staleStore struct{ *MemoryStore }

func (staleStore) Update(context.Context, Session) (Session, error) {
	return Session{}, ErrVersionConflict
}

// Two commands submitted at once must both be played, one after the other. The
// failure this prevents is a command that vanishes.
func TestConcurrentTurnsAreSerialized(t *testing.T) {
	const turns = 8

	store := NewMemoryStore()
	service := serviceOver(t, store)

	session, _, err := service.NewGame(t.Context(), "zork1")
	if err != nil {
		t.Fatalf("NewGame() error = %v", err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, turns)

	for range turns {
		wg.Go(func() {
			if _, _, err := service.Play(context.Background(), session.ID, "look"); err != nil {
				errs <- err
			}
		})
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("Play() error = %v", err)
	}

	final, err := store.Load(t.Context(), session.ID)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if final.Turn != turns {
		t.Errorf("session.Turn = %d, want %d; a command was lost", final.Turn, turns)
	}
	if final.Version != turns+1 {
		t.Errorf("session.Version = %d, want %d", final.Version, turns+1)
	}
}

// Two players are two sessions, and one does not move the other.
func TestSessionsAreIndependent(t *testing.T) {
	store := NewMemoryStore()
	service := serviceOver(t, store)

	first, _, err := service.NewGame(t.Context(), "zork1")
	if err != nil {
		t.Fatalf("NewGame(zork1) error = %v", err)
	}
	second, _, err := service.NewGame(t.Context(), "zork2")
	if err != nil {
		t.Fatalf("NewGame(zork2) error = %v", err)
	}

	if _, _, err := service.Play(t.Context(), first.ID, "north"); err != nil {
		t.Fatalf("Play() error = %v", err)
	}

	_, result, err := service.Play(t.Context(), second.ID, "look")
	if err != nil {
		t.Fatalf("Play() error = %v", err)
	}
	if result.StatusLine.Name != "Inside the Barrow" {
		t.Errorf("the second session moved with the first: %q", result.StatusLine.Name)
	}

	untouched, err := service.Session(t.Context(), second.ID)
	if err != nil {
		t.Fatalf("Session() error = %v", err)
	}
	if untouched.StoryKey != testEntry(t, "zork2").Key {
		t.Error("the second session records the wrong story")
	}
}

func TestNewServiceRequiresItsParts(t *testing.T) {
	library := testLibrary(t)

	tests := []struct {
		name    string
		library *Library
		runner  *Runner
		store   Store
	}{
		{name: "no library", runner: NewRunner(), store: NewMemoryStore()},
		{name: "no runner", library: library, store: NewMemoryStore()},
		{name: "no store", library: library, runner: NewRunner()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewService(tt.library, tt.runner, tt.store); err == nil {
				t.Fatal("NewService() = nil error, want failure")
			}
		})
	}
}

func TestMemoryStore(t *testing.T) {
	store := NewMemoryStore()

	created, err := store.Create(t.Context(), Session{State: []byte("state one")})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if created.ID == "" || created.Version == 0 {
		t.Fatalf("Create() = %+v, want an identifier and a version", created)
	}

	t.Run("load is a copy", func(t *testing.T) {
		loaded, err := store.Load(t.Context(), created.ID)
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		loaded.State[0] = 'X'

		again, err := store.Load(t.Context(), created.ID)
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if !bytes.Equal(again.State, []byte("state one")) {
			t.Errorf("a caller's write reached the store: %q", again.State)
		}
	})

	t.Run("update is conditional", func(t *testing.T) {
		stale := created

		updated, err := store.Update(t.Context(), Session{
			ID: created.ID, Version: created.Version, State: []byte("state two"),
		})
		if err != nil {
			t.Fatalf("Update() error = %v", err)
		}
		if updated.Version != created.Version+1 {
			t.Errorf("Update() version = %d, want %d", updated.Version, created.Version+1)
		}

		if _, err := store.Update(t.Context(), stale); !errors.Is(err, ErrVersionConflict) {
			t.Errorf("Update() with a stale version error = %v, want %v", err, ErrVersionConflict)
		}

		loaded, err := store.Load(t.Context(), created.ID)
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if !bytes.Equal(loaded.State, []byte("state two")) {
			t.Errorf("stored state = %q, want the state that won", loaded.State)
		}
	})

	t.Run("missing sessions", func(t *testing.T) {
		if _, err := store.Load(t.Context(), "nope"); !errors.Is(err, ErrSessionNotFound) {
			t.Errorf("Load() error = %v, want %v", err, ErrSessionNotFound)
		}
		if _, err := store.Update(t.Context(), Session{ID: "nope"}); !errors.Is(err, ErrSessionNotFound) {
			t.Errorf("Update() error = %v, want %v", err, ErrSessionNotFound)
		}
	})
}

func serviceOver(t *testing.T, store Store) *Service {
	t.Helper()

	service, err := NewService(testLibrary(t), NewRunner(), store)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return service
}
