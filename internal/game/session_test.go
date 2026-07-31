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

// player is the user every session in these tests belongs to. The Service takes
// the owner from its caller and never invents one, so the tests have to supply
// it too.
const player = "1"

func TestNewGameStoresTheOpening(t *testing.T) {
	store := NewMemoryStore()
	service := serviceOver(t, store)
	entry := testEntry(t, "zork1")

	session, result, err := service.NewGame(t.Context(), player, "zork1")
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

	session, _, err := serviceOver(t, store).NewGame(t.Context(), player, "zork1")
	if err != nil {
		t.Fatalf("NewGame() error = %v", err)
	}
	for _, command := range []string{"open mailbox", "take leaflet", "north"} {
		if _, err := serviceOver(t, store).Play(t.Context(), player, session.ID, command); err != nil {
			t.Fatalf("Play(%q) error = %v", command, err)
		}
	}

	// A new process: new library, new runner, new service, same store.
	turn, err := serviceOver(t, store).Play(t.Context(), player, session.ID, "inventory")
	if err != nil {
		t.Fatalf("Play() after restart error = %v", err)
	}
	resumed, result := turn.Session, turn.Result

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

	session, _, err := service.NewGame(t.Context(), player, "zork1")
	if err != nil {
		t.Fatalf("NewGame() error = %v", err)
	}
	if _, err := service.Play(t.Context(), player, session.ID, "open mailbox"); err != nil {
		t.Fatalf("Play() error = %v", err)
	}

	before, err := store.Load(t.Context(), player, session.ID)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	// A service that cannot finish a turn, over the same store.
	broken, err := NewService(testLibrary(t), NewRunner(WithInstructionLimit(1)), store)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if _, err := broken.Play(t.Context(), player, session.ID, "read leaflet"); err == nil {
		t.Fatal("Play() = nil error, want the instruction limit")
	} else if got := Classify(err); got != FaultExecutionLimit {
		t.Fatalf("Classify() = %v, want %v", got, FaultExecutionLimit)
	}

	after, err := store.Load(t.Context(), player, session.ID)
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
	turn, err := service.Play(t.Context(), player, session.ID, "read leaflet")
	if err != nil {
		t.Fatalf("Play() after a failed turn error = %v", err)
	}
	if !strings.Contains(turn.Result.Output, "WELCOME TO ZORK") {
		t.Errorf("the session did not resume where it was: %q", turn.Result.Output)
	}
}

// A halted story is the one time storing nil over a good state is right, and a
// halted session cannot be played again.
func TestPlayRefusesAHaltedSession(t *testing.T) {
	store := NewMemoryStore()
	service := serviceOver(t, store)

	session, _, err := service.NewGame(t.Context(), player, "zork1")
	if err != nil {
		t.Fatalf("NewGame() error = %v", err)
	}
	if _, err := service.Play(t.Context(), player, session.ID, "quit"); err != nil {
		t.Fatalf("Play(quit) error = %v", err)
	}
	turn, err := service.Play(t.Context(), player, session.ID, "yes")
	if err != nil {
		t.Fatalf("Play(yes) error = %v", err)
	}
	halted, result := turn.Session, turn.Result

	if result.Status != zmachine.Halted {
		t.Fatalf("Status = %v, want %v", result.Status, zmachine.Halted)
	}
	if !halted.Halted {
		t.Error("session.Halted = false after the story ended")
	}
	if halted.State != nil {
		t.Errorf("a halted session stored %d bytes of state", len(halted.State))
	}

	if _, err := service.Play(t.Context(), player, session.ID, "look"); !errors.Is(err, ErrGameOver) {
		t.Errorf("Play() on a halted session error = %v, want %v", err, ErrGameOver)
	}
}

// The bug this test was written for: restarting reset the story but not the
// screen this application keeps beside it, so the abandoned game stayed in the
// transcript with the new banner underneath it and the move count went on
// climbing.
func TestRestartResetsTheScreen(t *testing.T) {
	const seed = 1988

	store := NewMemoryStore()
	service, err := NewService(testLibrary(t), NewRunner(WithRandomSeed(seed)), store)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	session, opening, err := service.NewGame(t.Context(), player, "zork1")
	if err != nil {
		t.Fatalf("NewGame() error = %v", err)
	}

	play(t, service, session.ID, "open mailbox", "take leaflet")

	before, err := service.Session(t.Context(), player, session.ID)
	if err != nil {
		t.Fatalf("Session() error = %v", err)
	}

	turn, err := service.Restart(t.Context(), player, session.ID)
	if err != nil {
		t.Fatalf("Restart() error = %v", err)
	}
	if turn.Intent != IntentRestart {
		t.Errorf("Intent = %v, want %v", turn.Intent, IntentRestart)
	}

	// What is stored is what the caller was handed.
	stored, err := service.Session(t.Context(), player, session.ID)
	if err != nil {
		t.Fatalf("Session() error = %v", err)
	}
	if !bytes.Equal(stored.State, turn.Session.State) || stored.Transcript != turn.Session.Transcript {
		t.Error("the restart returned a session the store does not hold")
	}

	if strings.Contains(stored.Transcript, "take leaflet") {
		t.Errorf("the transcript still holds the abandoned game's commands:\n%s", tail(stored.Transcript))
	}
	if strings.Contains(stored.Transcript, "reveals a leaflet") {
		t.Errorf("the transcript still holds the abandoned game's output:\n%s", tail(stored.Transcript))
	}
	if stored.Transcript != opening.Output {
		t.Errorf("the transcript is not the opening of the new game:\n%s", tail(stored.Transcript))
	}
	if stored.Turn != 0 {
		t.Errorf("session.Turn = %d, want 0; the move count crossed the restart", stored.Turn)
	}
	if stored.Status.Name != "West of House" {
		t.Errorf("status = %q, want %q", stored.Status.Name, "West of House")
	}

	// A restart is a write like any other, so a turn issued against the screen
	// it replaced is refused rather than replayed.
	if stored.Version <= before.Version {
		t.Errorf("version = %d, want more than %d", stored.Version, before.Version)
	}

	// And the story went back with it: the leaflet is in the mailbox again.
	played, err := service.Play(t.Context(), player, session.ID, "inventory")
	if err != nil {
		t.Fatalf("Play(inventory) error = %v", err)
	}
	if strings.Contains(played.Result.Output, "leaflet") {
		t.Errorf("the abandoned game's leaflet survived the restart: %q", played.Result.Output)
	}
}

// A restart is a question. Nothing is played and nothing is written until the
// player confirms.
func TestRestartOnlyAsks(t *testing.T) {
	service := serviceOver(t, NewMemoryStore())

	session, _, err := service.NewGame(t.Context(), player, "zork1")
	if err != nil {
		t.Fatalf("NewGame() error = %v", err)
	}

	play(t, service, session.ID, "open mailbox")

	before, err := service.Session(t.Context(), player, session.ID)
	if err != nil {
		t.Fatalf("Session() error = %v", err)
	}

	for _, command := range []string{"restart", "RESTART"} {
		turn, err := service.Play(t.Context(), player, session.ID, command)
		if err != nil {
			t.Fatalf("Play(%q) error = %v", command, err)
		}
		if !turn.Asked || turn.Intent != IntentRestart {
			t.Errorf("Play(%q) = %+v, want a restart that only asked", command, turn)
		}

		after, err := service.Session(t.Context(), player, session.ID)
		if err != nil {
			t.Fatalf("Session() error = %v", err)
		}
		if after.Version != before.Version || after.Turn != before.Turn {
			t.Errorf("Play(%q) moved the session on: %d/%d became %d/%d",
				command, before.Turn, before.Version, after.Turn, after.Version)
		}
		if after.Transcript != before.Transcript {
			t.Errorf("Play(%q) wrote to the transcript:\n%s", command, tail(after.Transcript))
		}
	}
}

// A restart throws away the game in progress and nothing else. Every save is a
// complete state written from the same story, so every one of them still
// restores.
func TestRestartKeepsNamedSaves(t *testing.T) {
	service := serviceOver(t, NewMemoryStore())

	session, _, err := service.NewGame(t.Context(), player, "zork1")
	if err != nil {
		t.Fatalf("NewGame() error = %v", err)
	}

	play(t, service, session.ID, "open mailbox", "take leaflet")

	save, _, err := service.Save(t.Context(), player, session.ID, "with leaflet")
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	if _, err := service.Restart(t.Context(), player, session.ID); err != nil {
		t.Fatalf("Restart() error = %v", err)
	}

	saves, err := service.Saves(t.Context(), player, session.ID)
	if err != nil {
		t.Fatalf("Saves() error = %v", err)
	}
	if len(saves) != 1 || saves[0].ID != save.ID {
		t.Fatalf("the game holds %v after a restart, want the save it held before", saves)
	}

	restored, _, err := service.Restore(t.Context(), player, session.ID, save.ID)
	if err != nil {
		t.Fatalf("Restore() after a restart error = %v", err)
	}
	if restored.Turn != save.Turn {
		t.Errorf("session.Turn = %d, want %d", restored.Turn, save.Turn)
	}

	turn, err := service.Play(t.Context(), player, session.ID, "inventory")
	if err != nil {
		t.Fatalf("Play(inventory) error = %v", err)
	}
	if !strings.Contains(turn.Result.Output, "leaflet") {
		t.Errorf("the save did not survive the restart: %q", turn.Result.Output)
	}
}

// A story that ended itself is exactly the one a player wants to begin again,
// so a restart does not refuse the way a turn does.
func TestRestartAHaltedSession(t *testing.T) {
	service := serviceOver(t, NewMemoryStore())

	session, _, err := service.NewGame(t.Context(), player, "zork1")
	if err != nil {
		t.Fatalf("NewGame() error = %v", err)
	}

	play(t, service, session.ID, "quit", "yes")

	if _, err := service.Play(t.Context(), player, session.ID, "look"); !errors.Is(err, ErrGameOver) {
		t.Fatalf("Play() on a halted session error = %v, want %v", err, ErrGameOver)
	}

	turn, err := service.Restart(t.Context(), player, session.ID)
	if err != nil {
		t.Fatalf("Restart() on a halted session error = %v", err)
	}
	if turn.Session.Halted {
		t.Error("the session is still halted after a restart")
	}
	if len(turn.Session.State) == 0 {
		t.Error("the restarted session has no state")
	}
	if turn.Session.Turn != 0 {
		t.Errorf("session.Turn = %d, want 0", turn.Session.Turn)
	}

	if _, err := service.Play(t.Context(), player, session.ID, "look"); err != nil {
		t.Errorf("Play() after restarting a halted session error = %v", err)
	}
}

// Deleting a game takes the game, its screen and every save it held. There is
// nothing left afterwards, which is why the caller asks first.
func TestDeleteGame(t *testing.T) {
	store := NewMemoryStore()
	service := serviceOver(t, store)

	session, _, err := service.NewGame(t.Context(), player, "zork1")
	if err != nil {
		t.Fatalf("NewGame() error = %v", err)
	}

	play(t, service, session.ID, "open mailbox", "take leaflet")

	if _, _, err := service.Save(t.Context(), player, session.ID, "with leaflet"); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// A second game, to show that deleting one leaves the other alone.
	other, _, err := service.NewGame(t.Context(), player, "zork2")
	if err != nil {
		t.Fatalf("NewGame(zork2) error = %v", err)
	}
	if _, _, err := service.Save(t.Context(), player, other.ID, "kept"); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	if err := service.Delete(t.Context(), player, session.ID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	if _, err := service.Session(t.Context(), player, session.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("Session() after Delete() error = %v, want %v", err, ErrSessionNotFound)
	}
	if _, err := service.Saves(t.Context(), player, session.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("Saves() after Delete() error = %v, want %v", err, ErrSessionNotFound)
	}
	if err := service.Delete(t.Context(), player, session.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("Delete() twice error = %v, want %v", err, ErrSessionNotFound)
	}

	// It is gone from the lobby's list as well.
	games, err := service.Games(t.Context(), player)
	if err != nil {
		t.Fatalf("Games() error = %v", err)
	}
	if len(games) != 1 || games[0].ID != other.ID {
		t.Errorf("Games() = %+v, want only %s", games, other.ID)
	}

	if store.Len() != 1 {
		t.Errorf("the store holds %d sessions, want 1", store.Len())
	}

	// The deleted game's save is gone rather than merely unreachable: an
	// orphaned snapshot is the largest thing this store would go on holding.
	store.mu.Lock()
	for id, snapshot := range store.saves {
		if snapshot.GameID == session.ID {
			t.Errorf("save %s still hangs off the deleted game", id)
		}
	}
	held := len(store.saves)
	store.mu.Unlock()

	if held != 1 {
		t.Errorf("the store holds %d saves, want 1", held)
	}

	// The other game kept its own save.
	saves, err := service.Saves(t.Context(), player, other.ID)
	if err != nil {
		t.Fatalf("Saves() error = %v", err)
	}
	if len(saves) != 1 || saves[0].Name != "kept" {
		t.Errorf("the other game holds %+v, want the save it was given", saves)
	}
}

// A game that is somebody else's cannot be deleted, and reads as missing rather
// than as a refusal.
func TestOneUserCannotDeleteAnothersGame(t *testing.T) {
	service := serviceOver(t, NewMemoryStore())

	mine, _, err := service.NewGame(t.Context(), player, "zork1")
	if err != nil {
		t.Fatalf("NewGame() error = %v", err)
	}

	const stranger = "2"

	if err := service.Delete(t.Context(), stranger, mine.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("Delete() as another user error = %v, want %v", err, ErrSessionNotFound)
	}

	// And the game is still there.
	if _, err := service.Session(t.Context(), player, mine.ID); err != nil {
		t.Errorf("Session() after a refused delete error = %v", err)
	}
}

func TestServiceRejectsBadRequests(t *testing.T) {
	service := serviceOver(t, NewMemoryStore())

	if _, _, err := service.NewGame(t.Context(), player, "zork4"); !errors.Is(err, ErrStoryUnavailable) {
		t.Errorf("NewGame(zork4) error = %v, want %v", err, ErrStoryUnavailable)
	}
	if _, err := service.Play(t.Context(), player, "1234", "look"); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("Play() on an unknown session error = %v, want %v", err, ErrSessionNotFound)
	}
	if _, err := service.Session(t.Context(), player, "1234"); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("Session() on an unknown session error = %v, want %v", err, ErrSessionNotFound)
	}
	if _, err := service.Restart(t.Context(), player, "1234"); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("Restart() on an unknown session error = %v, want %v", err, ErrSessionNotFound)
	}
	if err := service.Delete(t.Context(), player, "1234"); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("Delete() on an unknown session error = %v, want %v", err, ErrSessionNotFound)
	}

	// Nothing is played on nobody's behalf. An empty user is a caller that
	// forgot to authenticate, not an anonymous game.
	if _, _, err := service.NewGame(t.Context(), "", "zork1"); err == nil {
		t.Error("NewGame() with no user = nil error, want failure")
	}
	if _, err := service.Play(t.Context(), "", "1", "look"); err == nil {
		t.Error("Play() with no user = nil error, want failure")
	}
	if _, err := service.Session(t.Context(), "", "1"); err == nil {
		t.Error("Session() with no user = nil error, want failure")
	}
	if _, err := service.Restart(t.Context(), "", "1"); err == nil {
		t.Error("Restart() with no user = nil error, want failure")
	}
	if err := service.Delete(t.Context(), "", "1"); err == nil {
		t.Error("Delete() with no user = nil error, want failure")
	}
}

// The authorization boundary: a session identifier is not a capability, and one
// player holding another's cannot read it, play it, or learn that it exists.
func TestOneUserCannotReachAnothersGame(t *testing.T) {
	service := serviceOver(t, NewMemoryStore())

	mine, _, err := service.NewGame(t.Context(), player, "zork1")
	if err != nil {
		t.Fatalf("NewGame() error = %v", err)
	}

	const stranger = "2"

	if _, err := service.Session(t.Context(), stranger, mine.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("Session() as another user error = %v, want %v", err, ErrSessionNotFound)
	}
	if _, err := service.Play(t.Context(), stranger, mine.ID, "north"); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("Play() as another user error = %v, want %v", err, ErrSessionNotFound)
	}
	if _, err := service.Restart(t.Context(), stranger, mine.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("Restart() as another user error = %v, want %v", err, ErrSessionNotFound)
	}

	// And the refused turn did not move the game.
	after, err := service.Session(t.Context(), player, mine.ID)
	if err != nil {
		t.Fatalf("Session() error = %v", err)
	}
	if after.Turn != 0 || after.Version != mine.Version {
		t.Errorf("the refused turn moved the session: %+v", after)
	}
}

// A session whose story this binary no longer carries is a deployment problem
// and must say so, rather than being reported as damaged state.
func TestPlayReportsAMissingStory(t *testing.T) {
	store := NewMemoryStore()

	session, _, err := serviceOver(t, store).NewGame(t.Context(), player, "zork2")
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

	if _, err := service.Play(t.Context(), player, session.ID, "look"); !errors.Is(err, ErrStoryUnavailable) {
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

	session, _, err := service.NewGame(t.Context(), player, "zork1")
	if err != nil {
		t.Fatalf("NewGame() error = %v", err)
	}

	if _, err := service.Play(t.Context(), player, session.ID, "look"); !errors.Is(err, ErrVersionConflict) {
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

	session, _, err := service.NewGame(t.Context(), player, "zork1")
	if err != nil {
		t.Fatalf("NewGame() error = %v", err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, turns)

	for range turns {
		wg.Go(func() {
			if _, err := service.Play(context.Background(), player, session.ID, "look"); err != nil {
				errs <- err
			}
		})
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("Play() error = %v", err)
	}

	final, err := store.Load(t.Context(), player, session.ID)
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

	first, _, err := service.NewGame(t.Context(), player, "zork1")
	if err != nil {
		t.Fatalf("NewGame(zork1) error = %v", err)
	}
	second, _, err := service.NewGame(t.Context(), player, "zork2")
	if err != nil {
		t.Fatalf("NewGame(zork2) error = %v", err)
	}

	if _, err := service.Play(t.Context(), player, first.ID, "north"); err != nil {
		t.Fatalf("Play() error = %v", err)
	}

	turn, err := service.Play(t.Context(), player, second.ID, "look")
	if err != nil {
		t.Fatalf("Play() error = %v", err)
	}
	if turn.Result.StatusLine.Name != "Inside the Barrow" {
		t.Errorf("the second session moved with the first: %q", turn.Result.StatusLine.Name)
	}

	untouched, err := service.Session(t.Context(), player, second.ID)
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

	created, err := store.Create(t.Context(), Session{UserID: player, State: []byte("state one")})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if created.ID == "" || created.Version == 0 {
		t.Fatalf("Create() = %+v, want an identifier and a version", created)
	}

	t.Run("load is a copy", func(t *testing.T) {
		loaded, err := store.Load(t.Context(), player, created.ID)
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		loaded.State[0] = 'X'

		again, err := store.Load(t.Context(), player, created.ID)
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
			ID: created.ID, UserID: player, Version: created.Version, State: []byte("state two"),
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

		loaded, err := store.Load(t.Context(), player, created.ID)
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if !bytes.Equal(loaded.State, []byte("state two")) {
			t.Errorf("stored state = %q, want the state that won", loaded.State)
		}
	})

	t.Run("missing sessions", func(t *testing.T) {
		if _, err := store.Load(t.Context(), player, "nope"); !errors.Is(err, ErrSessionNotFound) {
			t.Errorf("Load() error = %v, want %v", err, ErrSessionNotFound)
		}
		if _, err := store.Update(t.Context(), Session{ID: "nope", UserID: player}); !errors.Is(err, ErrSessionNotFound) {
			t.Errorf("Update() error = %v, want %v", err, ErrSessionNotFound)
		}
	})

	t.Run("another user's session", func(t *testing.T) {
		if _, err := store.Load(t.Context(), "2", created.ID); !errors.Is(err, ErrSessionNotFound) {
			t.Errorf("Load() error = %v, want %v", err, ErrSessionNotFound)
		}

		theirs := created
		theirs.UserID = "2"
		if _, err := store.Update(t.Context(), theirs); !errors.Is(err, ErrSessionNotFound) {
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
