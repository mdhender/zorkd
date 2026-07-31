package database

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/mdhender/zorkd/internal/game"
)

// storedSnapshot is a save of the session, ready to write.
func storedSnapshot(session game.Session, name string) game.Snapshot {
	return game.Snapshot{
		UserID:     session.UserID,
		GameID:     session.ID,
		Name:       name,
		StoryKey:   session.StoryKey,
		State:      session.State,
		Transcript: "West of House\n\n>",
		Status:     game.StatusLine{Available: true, Name: "West of House", Score: 10, Moves: 3},
		Turn:       3,
	}
}

func TestSavesRoundTrip(t *testing.T) {
	ctx := context.Background()
	sessions, owner := testSessions(t)

	session, err := sessions.Create(ctx, storedSession(owner))
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	save, replaced, err := sessions.CreateSave(ctx, storedSnapshot(session, "before troll"))
	if err != nil {
		t.Fatalf("CreateSave() error = %v", err)
	}
	if replaced {
		t.Error("the first save replaced something")
	}
	if save.ID == "" || save.Name != "before troll" || save.Turn != 3 {
		t.Errorf("CreateSave() = %+v, want an identified save named %q at turn 3", save, "before troll")
	}
	if save.CreatedAt.IsZero() {
		t.Error("CreateSave() recorded no time")
	}

	loaded, err := sessions.LoadSave(ctx, owner, session.ID, save.ID)
	if err != nil {
		t.Fatalf("LoadSave() error = %v", err)
	}
	if string(loaded.State) != string(session.State) {
		t.Errorf("state = %q, want %q", loaded.State, session.State)
	}
	if loaded.StoryKey != session.StoryKey {
		t.Error("the story key did not survive the round trip")
	}
	if loaded.Transcript != "West of House\n\n>" {
		t.Errorf("transcript = %q", loaded.Transcript)
	}
	if loaded.Status.Name != "West of House" || loaded.Status.Score != 10 || loaded.Status.Moves != 3 {
		t.Errorf("status = %+v, want the bar the save was written from", loaded.Status)
	}
	if !loaded.CreatedAt.Equal(save.CreatedAt) {
		t.Errorf("created = %v, want %v", loaded.CreatedAt, save.CreatedAt)
	}

	saves, err := sessions.Saves(ctx, owner, session.ID)
	if err != nil {
		t.Fatalf("Saves() error = %v", err)
	}
	if len(saves) != 1 || saves[0].ID != save.ID {
		t.Errorf("Saves() = %+v, want the one save", saves)
	}
}

// Names are unique within a game and are matched without regard to case, so a
// second save under the same name replaces the first rather than sitting beside
// it looking identical.
func TestCreateSaveReplacesByName(t *testing.T) {
	ctx := context.Background()
	sessions, owner := testSessions(t)

	session, err := sessions.Create(ctx, storedSession(owner))
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	first, _, err := sessions.CreateSave(ctx, storedSnapshot(session, "Troll"))
	if err != nil {
		t.Fatalf("CreateSave(Troll) error = %v", err)
	}

	later := storedSnapshot(session, "troll")
	later.State = []byte("a later state")
	later.Turn = 9

	second, replaced, err := sessions.CreateSave(ctx, later)
	if err != nil {
		t.Fatalf("CreateSave(troll) error = %v", err)
	}
	if !replaced {
		t.Error("the second save did not replace the first")
	}
	if second.ID != first.ID {
		t.Errorf("the replacement is a different row: %s became %s", first.ID, second.ID)
	}

	saves, err := sessions.Saves(ctx, owner, session.ID)
	if err != nil {
		t.Fatalf("Saves() error = %v", err)
	}
	if len(saves) != 1 {
		t.Fatalf("the game holds %d saves, want 1", len(saves))
	}
	if saves[0].Name != "troll" || saves[0].Turn != 9 {
		t.Errorf("save = %+v, want the newer one", saves[0])
	}

	loaded, err := sessions.LoadSave(ctx, owner, session.ID, second.ID)
	if err != nil {
		t.Fatalf("LoadSave() error = %v", err)
	}
	if string(loaded.State) != "a later state" {
		t.Errorf("state = %q, want the newer one", loaded.State)
	}
}

func TestCreateSaveStopsAtTheLimit(t *testing.T) {
	ctx := context.Background()
	sessions, owner := testSessions(t)

	session, err := sessions.Create(ctx, storedSession(owner))
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	for i := range game.MaxSavesPerGame {
		name := fmt.Sprintf("slot %d", i)
		if _, _, err := sessions.CreateSave(ctx, storedSnapshot(session, name)); err != nil {
			t.Fatalf("CreateSave(%q) error = %v", name, err)
		}
	}

	_, _, err = sessions.CreateSave(ctx, storedSnapshot(session, "one too many"))
	if !errors.Is(err, game.ErrTooManySaves) {
		t.Errorf("CreateSave() past the limit error = %v, want %v", err, game.ErrTooManySaves)
	}

	// The refused save left nothing behind.
	saves, err := sessions.Saves(ctx, owner, session.ID)
	if err != nil {
		t.Fatalf("Saves() error = %v", err)
	}
	if len(saves) != game.MaxSavesPerGame {
		t.Errorf("the game holds %d saves, want %d", len(saves), game.MaxSavesPerGame)
	}
}

func TestDeleteSave(t *testing.T) {
	ctx := context.Background()
	sessions, owner := testSessions(t)

	session, err := sessions.Create(ctx, storedSession(owner))
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	save, _, err := sessions.CreateSave(ctx, storedSnapshot(session, "cellar"))
	if err != nil {
		t.Fatalf("CreateSave() error = %v", err)
	}

	deleted, err := sessions.DeleteSave(ctx, owner, session.ID, save.ID)
	if err != nil {
		t.Fatalf("DeleteSave() error = %v", err)
	}
	if deleted.Name != "cellar" {
		t.Errorf("DeleteSave() returned %q, want %q", deleted.Name, "cellar")
	}

	if _, err := sessions.DeleteSave(ctx, owner, session.ID, save.ID); !errors.Is(err, game.ErrSaveNotFound) {
		t.Errorf("DeleteSave() twice error = %v, want %v", err, game.ErrSaveNotFound)
	}
	if _, err := sessions.LoadSave(ctx, owner, session.ID, save.ID); !errors.Is(err, game.ErrSaveNotFound) {
		t.Errorf("LoadSave() after delete error = %v, want %v", err, game.ErrSaveNotFound)
	}

	// The game the save hung off is untouched.
	if _, err := sessions.Load(ctx, owner, session.ID); err != nil {
		t.Errorf("Load() after deleting a save error = %v", err)
	}
}

// A save is reached by way of the game it belongs to. Somebody else's game
// reads as missing, because saying "not yours" would confirm that it exists.
func TestSavesReachOnlyTheirOwnGame(t *testing.T) {
	ctx := context.Background()

	db := testDB(t)
	sessions := db.Sessions()
	owner := testUser(t, db, "player@example.com")
	stranger := testUser(t, db, "stranger@example.com")

	mine, err := sessions.Create(ctx, storedSession(owner))
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	theirs, err := sessions.Create(ctx, storedSession(stranger))
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	save, _, err := sessions.CreateSave(ctx, storedSnapshot(mine, "mine"))
	if err != nil {
		t.Fatalf("CreateSave() error = %v", err)
	}

	if _, _, err := sessions.CreateSave(ctx, storedSnapshot(mine, "theirs")); err != nil {
		t.Fatalf("CreateSave() by the owner error = %v", err)
	}

	// The stranger cannot write to, read, or delete anything of the owner's.
	snapshot := storedSnapshot(mine, "intruder")
	snapshot.UserID = stranger
	if _, _, err := sessions.CreateSave(ctx, snapshot); !errors.Is(err, game.ErrSessionNotFound) {
		t.Errorf("CreateSave() by a stranger error = %v, want %v", err, game.ErrSessionNotFound)
	}
	if _, err := sessions.Saves(ctx, stranger, mine.ID); !errors.Is(err, game.ErrSessionNotFound) {
		t.Errorf("Saves() by a stranger error = %v, want %v", err, game.ErrSessionNotFound)
	}
	if _, err := sessions.LoadSave(ctx, stranger, mine.ID, save.ID); !errors.Is(err, game.ErrSessionNotFound) {
		t.Errorf("LoadSave() by a stranger error = %v, want %v", err, game.ErrSessionNotFound)
	}
	if _, err := sessions.DeleteSave(ctx, stranger, mine.ID, save.ID); !errors.Is(err, game.ErrSessionNotFound) {
		t.Errorf("DeleteSave() by a stranger error = %v, want %v", err, game.ErrSessionNotFound)
	}

	// Nor can the stranger reach it through a game that is theirs.
	if _, err := sessions.LoadSave(ctx, stranger, theirs.ID, save.ID); !errors.Is(err, game.ErrSaveNotFound) {
		t.Errorf("LoadSave() through another game error = %v, want %v", err, game.ErrSaveNotFound)
	}
	if _, err := sessions.DeleteSave(ctx, stranger, theirs.ID, save.ID); !errors.Is(err, game.ErrSaveNotFound) {
		t.Errorf("DeleteSave() through another game error = %v, want %v", err, game.ErrSaveNotFound)
	}

	// And the owner's saves are all still there.
	saves, err := sessions.Saves(ctx, owner, mine.ID)
	if err != nil || len(saves) != 2 {
		t.Errorf("Saves() = %d saves, error = %v; want 2 and no error", len(saves), err)
	}
}

// An identifier that came from a browser is not a row identifier until it has
// been read as one, and one that could never have been assigned is reported as
// missing rather than as a failure.
func TestSavesRefuseMalformedIdentifiers(t *testing.T) {
	ctx := context.Background()
	sessions, owner := testSessions(t)

	session, err := sessions.Create(ctx, storedSession(owner))
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	for _, id := range []string{"", "0", "-1", "../../etc/passwd", "1; DROP TABLE saves"} {
		if _, err := sessions.LoadSave(ctx, owner, session.ID, id); !errors.Is(err, game.ErrSaveNotFound) {
			t.Errorf("LoadSave(%q) error = %v, want %v", id, err, game.ErrSaveNotFound)
		}
	}
}
