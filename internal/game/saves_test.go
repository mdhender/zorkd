package game

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/mdhender/zorkd/games"
)

// SAVE and RESTORE must never reach the engine.
//
// The proof is a service whose library does not carry the session's story: any
// line the engine would have to answer fails with ErrStoryUnavailable, so a
// save that succeeds is a save that got nowhere near a machine.
func TestSaveAndRestoreNeverReachTheEngine(t *testing.T) {
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

	// The engine is out of reach.
	if _, err := service.Play(t.Context(), player, session.ID, "look"); !errors.Is(err, ErrStoryUnavailable) {
		t.Fatalf("Play(look) error = %v, want %v", err, ErrStoryUnavailable)
	}

	// These two are answered anyway.
	saved, err := service.Play(t.Context(), player, session.ID, "save here")
	if err != nil {
		t.Fatalf("Play(save here) error = %v", err)
	}
	if saved.Intent != IntentSave || saved.Save.Name != "here" {
		t.Errorf("Play(save here) = %+v, want a save named %q", saved, "here")
	}

	restored, err := service.Play(t.Context(), player, session.ID, "restore here")
	if err != nil {
		t.Fatalf("Play(restore here) error = %v", err)
	}
	if restored.Intent != IntentRestore || restored.Save.Name != "here" {
		t.Errorf("Play(restore here) = %+v, want a restore of %q", restored, "here")
	}
}

// A bare SAVE or RESTORE is a question. Nothing is played and nothing is
// written until the answer arrives.
func TestBareSaveAndRestoreOnlyAsk(t *testing.T) {
	store := NewMemoryStore()
	service := serviceOver(t, store)

	session, _, err := service.NewGame(t.Context(), player, "zork1")
	if err != nil {
		t.Fatalf("NewGame() error = %v", err)
	}

	for _, command := range []string{"save", "restore", "SAVE"} {
		turn, err := service.Play(t.Context(), player, session.ID, command)
		if err != nil {
			t.Fatalf("Play(%q) error = %v", command, err)
		}
		if !turn.Asked {
			t.Errorf("Play(%q) did not ask; it answered %+v", command, turn)
		}

		stored, err := service.Session(t.Context(), player, session.ID)
		if err != nil {
			t.Fatalf("Session() error = %v", err)
		}
		if stored.Version != session.Version || stored.Turn != session.Turn {
			t.Errorf("Play(%q) moved the session on: %d/%d became %d/%d",
				command, session.Turn, session.Version, stored.Turn, stored.Version)
		}
	}
}

// The point of the whole feature: a save goes back to the game the player left.
func TestRestorePutsTheGameBack(t *testing.T) {
	store := NewMemoryStore()
	service := serviceOver(t, store)

	session, _, err := service.NewGame(t.Context(), player, "zork1")
	if err != nil {
		t.Fatalf("NewGame() error = %v", err)
	}

	play(t, service, session.ID, "open mailbox", "take leaflet")

	save, replaced, err := service.Save(t.Context(), player, session.ID, "with leaflet")
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if replaced {
		t.Error("the first save replaced something")
	}

	// Put the leaflet back and walk away from it.
	play(t, service, session.ID, "drop leaflet", "north", "north")

	before, err := service.Session(t.Context(), player, session.ID)
	if err != nil {
		t.Fatalf("Session() error = %v", err)
	}

	restored, back, err := service.Restore(t.Context(), player, session.ID, save.ID)
	if err != nil {
		t.Fatalf("Restore() error = %v", err)
	}
	if back.Name != save.Name {
		t.Errorf("Restore() returned %q, want %q", back.Name, save.Name)
	}

	// The move count and the status bar went back with the state.
	if restored.Turn != save.Turn {
		t.Errorf("session.Turn = %d, want %d", restored.Turn, save.Turn)
	}
	if restored.Status.Name != "West of House" {
		t.Errorf("status = %q, want %q", restored.Status.Name, "West of House")
	}

	// The screen went back too, rather than describing a game that is gone.
	if strings.Contains(restored.Transcript, "drop leaflet") {
		t.Error("the transcript still holds turns the restore undid")
	}
	if !strings.Contains(restored.Transcript, `[Restored "with leaflet".]`) {
		t.Errorf("the transcript does not record the restore:\n%s", tail(restored.Transcript))
	}

	// And the engine agrees: the leaflet is back in hand.
	turn, err := service.Play(t.Context(), player, session.ID, "inventory")
	if err != nil {
		t.Fatalf("Play(inventory) error = %v", err)
	}
	if !strings.Contains(turn.Result.Output, "leaflet") {
		t.Errorf("the leaflet did not come back: %q", turn.Result.Output)
	}

	// The version moved forward: a restore is a write like any other, so a turn
	// issued against the pre-restore screen is refused rather than replayed.
	if restored.Version <= before.Version {
		t.Errorf("version = %d, want more than %d", restored.Version, before.Version)
	}
}

// Saving under a name the game already holds replaces it, whatever case it was
// typed in. The alternative is a list of saves that all read the same.
func TestSaveReplacesByName(t *testing.T) {
	store := NewMemoryStore()
	service := serviceOver(t, store)

	session, _, err := service.NewGame(t.Context(), player, "zork1")
	if err != nil {
		t.Fatalf("NewGame() error = %v", err)
	}

	first, replaced, err := service.Save(t.Context(), player, session.ID, "Troll")
	if err != nil {
		t.Fatalf("Save(Troll) error = %v", err)
	}
	if replaced {
		t.Error("the first save replaced something")
	}

	play(t, service, session.ID, "open mailbox")

	second, replaced, err := service.Save(t.Context(), player, session.ID, "troll")
	if err != nil {
		t.Fatalf("Save(troll) error = %v", err)
	}
	if !replaced {
		t.Error("saving under a name already in use did not replace it")
	}
	if second.ID != first.ID {
		t.Errorf("the replacement is a different row: %s became %s", first.ID, second.ID)
	}

	saves, err := service.Saves(t.Context(), player, session.ID)
	if err != nil {
		t.Fatalf("Saves() error = %v", err)
	}
	if len(saves) != 1 {
		t.Fatalf("the game holds %d saves, want 1", len(saves))
	}
	if saves[0].Turn != 1 {
		t.Errorf("save.Turn = %d, want 1; the replacement holds the newer state", saves[0].Turn)
	}
}

// A game cannot accumulate saves without limit: each one carries a whole copy
// of the screen as well as the state.
func TestSaveLimit(t *testing.T) {
	store := NewMemoryStore()
	service := serviceOver(t, store)

	session, _, err := service.NewGame(t.Context(), player, "zork1")
	if err != nil {
		t.Fatalf("NewGame() error = %v", err)
	}

	for i := range MaxSavesPerGame {
		if _, _, err := service.Save(t.Context(), player, session.ID, fmt.Sprintf("slot %d", i)); err != nil {
			t.Fatalf("Save(slot %d) error = %v", i, err)
		}
	}

	if _, _, err := service.Save(t.Context(), player, session.ID, "one too many"); !errors.Is(err, ErrTooManySaves) {
		t.Errorf("Save() past the limit error = %v, want %v", err, ErrTooManySaves)
	}

	// Replacing one that already exists is not a new save and is still allowed.
	if _, replaced, err := service.Save(t.Context(), player, session.ID, "slot 0"); err != nil || !replaced {
		t.Errorf("Save() over an existing name at the limit: replaced = %v, error = %v", replaced, err)
	}

	// And deleting one makes room again.
	saves, err := service.Saves(t.Context(), player, session.ID)
	if err != nil {
		t.Fatalf("Saves() error = %v", err)
	}
	if _, err := service.DeleteSave(t.Context(), player, session.ID, saves[0].ID); err != nil {
		t.Fatalf("DeleteSave() error = %v", err)
	}
	if _, _, err := service.Save(t.Context(), player, session.ID, "room at last"); err != nil {
		t.Errorf("Save() after deleting one error = %v", err)
	}
}

// A story that ended itself stores no state, so there is nothing to save — but
// a save written before it ended is the way back.
func TestRestoreBringsAHaltedGameBack(t *testing.T) {
	store := NewMemoryStore()
	service := serviceOver(t, store)

	session, _, err := service.NewGame(t.Context(), player, "zork1")
	if err != nil {
		t.Fatalf("NewGame() error = %v", err)
	}

	save, _, err := service.Save(t.Context(), player, session.ID, "alive")
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	play(t, service, session.ID, "quit", "yes")

	if _, _, err := service.Save(t.Context(), player, session.ID, "dead"); !errors.Is(err, ErrGameOver) {
		t.Errorf("Save() on a halted game error = %v, want %v", err, ErrGameOver)
	}

	restored, _, err := service.Restore(t.Context(), player, session.ID, save.ID)
	if err != nil {
		t.Fatalf("Restore() error = %v", err)
	}
	if restored.Halted {
		t.Error("the session is still halted after a restore")
	}
	if len(restored.State) == 0 {
		t.Error("the session has no state after a restore")
	}

	if _, err := service.Play(t.Context(), player, session.ID, "look"); err != nil {
		t.Errorf("Play() after restoring a halted game error = %v", err)
	}
}

func TestDeleteSave(t *testing.T) {
	store := NewMemoryStore()
	service := serviceOver(t, store)

	session, _, err := service.NewGame(t.Context(), player, "zork1")
	if err != nil {
		t.Fatalf("NewGame() error = %v", err)
	}

	save, _, err := service.Save(t.Context(), player, session.ID, "cellar")
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	deleted, err := service.DeleteSave(t.Context(), player, session.ID, save.ID)
	if err != nil {
		t.Fatalf("DeleteSave() error = %v", err)
	}
	if deleted.Name != "cellar" {
		t.Errorf("DeleteSave() returned %q, want %q", deleted.Name, "cellar")
	}

	if _, err := service.DeleteSave(t.Context(), player, session.ID, save.ID); !errors.Is(err, ErrSaveNotFound) {
		t.Errorf("DeleteSave() twice error = %v, want %v", err, ErrSaveNotFound)
	}
	if _, _, err := service.Restore(t.Context(), player, session.ID, save.ID); !errors.Is(err, ErrSaveNotFound) {
		t.Errorf("Restore() of a deleted save error = %v, want %v", err, ErrSaveNotFound)
	}

	// The game itself is untouched: deleting a save takes nothing away from it.
	if _, err := service.Play(t.Context(), player, session.ID, "look"); err != nil {
		t.Errorf("Play() after deleting a save error = %v", err)
	}
}

// A save is reached by way of the game it belongs to, and a game belongs to one
// user. Somebody else's game reads as missing rather than as a refusal.
func TestSavesAreScopedToTheOwner(t *testing.T) {
	const stranger = "2"

	store := NewMemoryStore()
	service := serviceOver(t, store)

	mine, _, err := service.NewGame(t.Context(), player, "zork1")
	if err != nil {
		t.Fatalf("NewGame() error = %v", err)
	}
	save, _, err := service.Save(t.Context(), player, mine.ID, "mine")
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	if _, _, err := service.Save(t.Context(), stranger, mine.ID, "theirs"); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("Save() by a stranger error = %v, want %v", err, ErrSessionNotFound)
	}
	if _, err := service.Saves(t.Context(), stranger, mine.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("Saves() by a stranger error = %v, want %v", err, ErrSessionNotFound)
	}
	if _, _, err := service.Restore(t.Context(), stranger, mine.ID, save.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("Restore() by a stranger error = %v, want %v", err, ErrSessionNotFound)
	}
	if _, err := service.DeleteSave(t.Context(), stranger, mine.ID, save.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("DeleteSave() by a stranger error = %v, want %v", err, ErrSessionNotFound)
	}

	// And it is still there.
	saves, err := service.Saves(t.Context(), player, mine.ID)
	if err != nil || len(saves) != 1 {
		t.Errorf("Saves() = %d saves, error = %v; want 1 and no error", len(saves), err)
	}
}

// A name typed on the command line reaches the same save the list would.
func TestRestoreByName(t *testing.T) {
	store := NewMemoryStore()
	service := serviceOver(t, store)

	session, _, err := service.NewGame(t.Context(), player, "zork1")
	if err != nil {
		t.Fatalf("NewGame() error = %v", err)
	}

	play(t, service, session.ID, "save Before Troll", "north")

	turn, err := service.Play(t.Context(), player, session.ID, "restore  before   troll")
	if err != nil {
		t.Fatalf("Play(restore) error = %v", err)
	}
	if turn.Save.Name != "Before Troll" {
		t.Errorf("restored %q, want %q", turn.Save.Name, "Before Troll")
	}

	if _, err := service.Play(t.Context(), player, session.ID, "restore nowhere"); !errors.Is(err, ErrSaveNotFound) {
		t.Errorf("Play(restore nowhere) error = %v, want %v", err, ErrSaveNotFound)
	}
}

func TestSaveRefusesAnUnusableName(t *testing.T) {
	service := serviceOver(t, NewMemoryStore())

	session, _, err := service.NewGame(t.Context(), player, "zork1")
	if err != nil {
		t.Fatalf("NewGame() error = %v", err)
	}

	for _, name := range []string{"", "   ", "a\x00b", strings.Repeat("x", MaxSaveNameBytes+1)} {
		if _, _, err := service.Save(t.Context(), player, session.ID, name); !errors.Is(err, ErrInvalidSaveName) {
			t.Errorf("Save(%q) error = %v, want %v", name, err, ErrInvalidSaveName)
		}
	}
}

// A save records what the terminal said about it, so a refresh reads the same
// as the turn did.
func TestSaveIsRecordedOnTheScreen(t *testing.T) {
	service := serviceOver(t, NewMemoryStore())

	session, _, err := service.NewGame(t.Context(), player, "zork1")
	if err != nil {
		t.Fatalf("NewGame() error = %v", err)
	}

	turn, err := service.Play(t.Context(), player, session.ID, "save cellar")
	if err != nil {
		t.Fatalf("Play(save cellar) error = %v", err)
	}

	if want := `Saved as "cellar".`; turn.Notice() != want {
		t.Errorf("Notice() = %q, want %q", turn.Notice(), want)
	}
	if !strings.Contains(turn.Session.Transcript, ">save cellar") {
		t.Errorf("the transcript does not echo the command:\n%s", tail(turn.Session.Transcript))
	}
	if !strings.Contains(turn.Session.Transcript, `[Saved as "cellar".]`) {
		t.Errorf("the transcript does not record the save:\n%s", tail(turn.Session.Transcript))
	}
	// The story's own save would have said this instead.
	if strings.Contains(turn.Session.Transcript, "Failed.") {
		t.Error("the story answered the save")
	}
	// A transcript always ends waiting for input.
	if !strings.HasSuffix(turn.Session.Transcript, ">") {
		t.Errorf("the transcript does not end at a prompt:\n%s", tail(turn.Session.Transcript))
	}
}

// play runs commands and fails the test if any of them does.
func play(t *testing.T, service *Service, sessionID string, commands ...string) {
	t.Helper()

	for _, command := range commands {
		if _, err := service.Play(t.Context(), player, sessionID, command); err != nil {
			t.Fatalf("Play(%q) error = %v", command, err)
		}
	}
}

// tail is the last of a transcript, for a failure message that has to be read.
func tail(transcript string) string {
	const shown = 300
	if len(transcript) <= shown {
		return transcript
	}
	return "..." + transcript[len(transcript)-shown:]
}
