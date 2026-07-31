package database

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/mdhender/zorkd/internal/game"
)

func storedSession(userID string) game.Session {
	return game.Session{
		UserID:   userID,
		StoryKey: game.StoryKeyOf([]byte("a story")),
		State:    []byte("saved state"),
	}
}

// testSessions returns a store together with an account to own what goes in it.
func testSessions(t *testing.T) (*Sessions, string) {
	t.Helper()

	db := testDB(t)
	return db.Sessions(), testUser(t, db, "player@example.com")
}

func TestSessionsRoundTrip(t *testing.T) {
	ctx := context.Background()
	sessions, owner := testSessions(t)

	created, err := sessions.Create(ctx, storedSession(owner))
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if created.ID == "" {
		t.Fatal("Create() assigned no identifier")
	}
	if created.Version != 1 {
		t.Errorf("Create() version = %d, want 1", created.Version)
	}

	loaded, err := sessions.Load(ctx, owner, created.ID)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.StoryKey != created.StoryKey {
		t.Error("the story key did not survive the round trip")
	}
	if string(loaded.State) != "saved state" {
		t.Errorf("state = %q, want %q", loaded.State, "saved state")
	}
	if loaded.Turn != 0 || loaded.Halted {
		t.Errorf("loaded = %+v, want an unplayed, unhalted session", loaded)
	}

	loaded.State = []byte("a later state")
	loaded.Turn = 1

	updated, err := sessions.Update(ctx, loaded)
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if updated.Version != loaded.Version+1 {
		t.Errorf("Update() version = %d, want %d", updated.Version, loaded.Version+1)
	}

	again, err := sessions.Load(ctx, owner, created.ID)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if string(again.State) != "a later state" || again.Turn != 1 {
		t.Errorf("loaded = %+v, want the state the update wrote", again)
	}
}

// The conditional write is what stops a turn from overwriting one that got
// there first.
func TestSessionsUpdateIsConditional(t *testing.T) {
	ctx := context.Background()
	sessions, owner := testSessions(t)

	created, err := sessions.Create(ctx, storedSession(owner))
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	stale := created

	winner := created
	winner.State = []byte("the turn that won")
	winner.Turn = 1
	if _, err := sessions.Update(ctx, winner); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	stale.State = []byte("the turn that lost")
	stale.Turn = 1
	if _, err := sessions.Update(ctx, stale); !errors.Is(err, game.ErrVersionConflict) {
		t.Errorf("Update() with a stale version error = %v, want %v", err, game.ErrVersionConflict)
	}

	loaded, err := sessions.Load(ctx, owner, created.ID)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if string(loaded.State) != "the turn that won" {
		t.Errorf("stored state = %q, want the turn that won", loaded.State)
	}
	if loaded.Version != 2 {
		t.Errorf("version = %d, want 2; the refused write moved it", loaded.Version)
	}
}

func TestSessionsMissing(t *testing.T) {
	ctx := context.Background()
	sessions, owner := testSessions(t)

	ids := []string{"1", "0", "-3", "", "abc", "1; DROP TABLE games", "../../etc/passwd"}

	for _, id := range ids {
		t.Run(id, func(t *testing.T) {
			if _, err := sessions.Load(ctx, owner, id); !errors.Is(err, game.ErrSessionNotFound) {
				t.Errorf("Load(%q) error = %v, want %v", id, err, game.ErrSessionNotFound)
			}
			if _, err := sessions.Update(ctx, game.Session{ID: id, UserID: owner, State: []byte("x")}); !errors.Is(err, game.ErrSessionNotFound) {
				t.Errorf("Update(%q) error = %v, want %v", id, err, game.ErrSessionNotFound)
			}
		})
	}
}

// The screen a refresh redraws is stored with the state, and comes back whole.
func TestSessionsStoreTheScreen(t *testing.T) {
	ctx := context.Background()
	sessions, owner := testSessions(t)

	screen := storedSession(owner)
	screen.Transcript = "West of House\n\n>look\nYou are standing in an open field.\n\n>"
	screen.Status = game.StatusLine{Available: true, Name: "West of House", Score: 10, Moves: 3}

	created, err := sessions.Create(ctx, screen)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	loaded, err := sessions.Load(ctx, owner, created.ID)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.Transcript != screen.Transcript {
		t.Errorf("transcript =\n%q\nwant\n%q", loaded.Transcript, screen.Transcript)
	}
	if loaded.Status != screen.Status {
		t.Errorf("status = %+v, want %+v", loaded.Status, screen.Status)
	}

	loaded.Transcript += "quit\n"
	loaded.Status = game.StatusLine{Available: true, Name: "Kitchen", TimeGame: true, Hours: 22, Minutes: 5}

	if _, err := sessions.Update(ctx, loaded); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	again, err := sessions.Load(ctx, owner, created.ID)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if again.Transcript != loaded.Transcript || again.Status != loaded.Status {
		t.Errorf("the update did not survive: %q / %+v", again.Transcript, again.Status)
	}
}

// The lobby's list carries neither the state nor the transcript: it is read to
// choose between games, not to play them.
func TestSessionsList(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	sessions := db.Sessions()

	owner := testUser(t, db, "player@example.com")
	stranger := testUser(t, db, "stranger@example.com")

	first, err := sessions.Create(ctx, storedSession(owner))
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	second, err := sessions.Create(ctx, storedSession(owner))
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if _, err := sessions.Create(ctx, storedSession(stranger)); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	// Move the first game so it becomes the most recent.
	first.Turn = 1
	if _, err := sessions.Update(ctx, first); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	listed, err := sessions.List(ctx, owner)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("List() returned %d games, want 2 — the stranger's is not theirs to see", len(listed))
	}
	if listed[0].ID != first.ID || listed[1].ID != second.ID {
		t.Errorf("List() = %s, %s; want %s first", listed[0].ID, listed[1].ID, first.ID)
	}
	if listed[0].Turn != 1 || listed[0].StoryKey != first.StoryKey {
		t.Errorf("List()[0] = %+v, want the game as it was stored", listed[0])
	}
	if listed[0].UpdatedAt.IsZero() {
		t.Error("List() reported no update time")
	}

	// An identifier that could never have been assigned owns nothing.
	for _, id := range []string{"", "0", "-1", "abc"} {
		if got, err := sessions.List(ctx, id); err != nil || len(got) != 0 {
			t.Errorf("List(%q) = %d games, %v; want none", id, len(got), err)
		}
	}
}

// The owner is part of the query, so a session identifier taken from somebody
// else's browser reads as a session that does not exist.
func TestSessionsAreScopedToTheirOwner(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	sessions := db.Sessions()

	owner := testUser(t, db, "player@example.com")
	stranger := testUser(t, db, "stranger@example.com")

	created, err := sessions.Create(ctx, storedSession(owner))
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if _, err := sessions.Load(ctx, stranger, created.ID); !errors.Is(err, game.ErrSessionNotFound) {
		t.Errorf("Load() as another user error = %v, want %v", err, game.ErrSessionNotFound)
	}

	theirs := created
	theirs.UserID = stranger
	theirs.State = []byte("a state they had no business writing")
	if _, err := sessions.Update(ctx, theirs); !errors.Is(err, game.ErrSessionNotFound) {
		t.Errorf("Update() as another user error = %v, want %v", err, game.ErrSessionNotFound)
	}

	loaded, err := sessions.Load(ctx, owner, created.ID)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if string(loaded.State) != "saved state" {
		t.Errorf("the refused write reached the row: %q", loaded.State)
	}
}

// A halted session is the one case where storing no state is right, and the
// only one the schema allows.
func TestSessionsHalted(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	owner := testUser(t, db, "player@example.com")
	sessions := db.Sessions()

	created, err := sessions.Create(ctx, storedSession(owner))
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	over := created
	over.State = nil
	over.Halted = true
	over.Turn = 12

	if _, err := sessions.Update(ctx, over); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	loaded, err := sessions.Load(ctx, owner, created.ID)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !loaded.Halted {
		t.Error("halted did not survive the round trip")
	}
	if loaded.State != nil {
		t.Errorf("a halted session loaded %d bytes of state", len(loaded.State))
	}

	// SQL NULL, not a zero-length blob: an empty blob would satisfy the
	// schema's rule that only a halted game may store nothing.
	if got := pragma(t, db, "SELECT state IS NULL FROM games;"); got != "1" {
		t.Errorf("state IS NULL = %q, want %q", got, "1")
	}
}

// Storing nothing for a game that is still being played would turn the next
// restore into a failure that reads as corruption. The schema refuses it.
func TestSessionsRefuseALiveGameWithNoState(t *testing.T) {
	ctx := context.Background()
	sessions, owner := testSessions(t)

	if _, err := sessions.Create(ctx, game.Session{StoryKey: game.StoryKeyOf([]byte("a story"))}); err == nil {
		t.Fatal("Create() = nil error, want the check constraint to refuse it")
	}

	created, err := sessions.Create(ctx, storedSession(owner))
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	blanked := created
	blanked.State = nil
	if _, err := sessions.Update(ctx, blanked); err == nil {
		t.Fatal("Update() = nil error, want the check constraint to refuse it")
	}

	loaded, err := sessions.Load(ctx, owner, created.ID)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if string(loaded.State) != "saved state" {
		t.Errorf("the refused write reached the row: %q", loaded.State)
	}
}

func TestSessionsDelete(t *testing.T) {
	ctx := context.Background()
	sessions, owner := testSessions(t)

	created, err := sessions.Create(ctx, storedSession(owner))
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if err := sessions.Delete(ctx, owner, created.ID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	if _, err := sessions.Load(ctx, owner, created.ID); !errors.Is(err, game.ErrSessionNotFound) {
		t.Errorf("Load() after Delete() error = %v, want %v", err, game.ErrSessionNotFound)
	}
	if err := sessions.Delete(ctx, owner, created.ID); !errors.Is(err, game.ErrSessionNotFound) {
		t.Errorf("Delete() twice error = %v, want %v", err, game.ErrSessionNotFound)
	}

	listed, err := sessions.List(ctx, owner)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(listed) != 0 {
		t.Errorf("List() returned %d games, want none", len(listed))
	}

	// An identifier that could never have been assigned is missing, not a
	// failure — a browser asking for "../../etc/passwd" has asked for a game
	// that does not exist.
	for _, id := range []string{"", "0", "-3", "abc", "1; DROP TABLE games", "../../etc/passwd"} {
		if err := sessions.Delete(ctx, owner, id); !errors.Is(err, game.ErrSessionNotFound) {
			t.Errorf("Delete(%q) error = %v, want %v", id, err, game.ErrSessionNotFound)
		}
	}
}

// The owner is in the WHERE clause, so a game that is somebody else's cannot be
// deleted and reads as missing rather than as a refusal.
func TestSessionsDeleteIsScopedToTheOwner(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	sessions := db.Sessions()

	owner := testUser(t, db, "player@example.com")
	stranger := testUser(t, db, "stranger@example.com")

	created, err := sessions.Create(ctx, storedSession(owner))
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if err := sessions.Delete(ctx, stranger, created.ID); !errors.Is(err, game.ErrSessionNotFound) {
		t.Errorf("Delete() as another user error = %v, want %v", err, game.ErrSessionNotFound)
	}

	if _, err := sessions.Load(ctx, owner, created.ID); err != nil {
		t.Errorf("Load() after a refused delete error = %v; the game should still be there", err)
	}
}

// Deleting a game takes its saves with it.
//
// The schema does this: saves.game_id declares ON DELETE CASCADE. This test
// exists because that is the kind of thing that stays true until somebody
// rebuilds the table — and because SQLite enforces foreign keys only when asked
// to, so a cascade that is declared but not switched on would leave the rows
// behind with nothing left to reach them by.
func TestDeletingAGameTakesItsSaves(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	sessions := db.Sessions()
	owner := testUser(t, db, "player@example.com")

	// The pragma is the whole cascade. Without it the DELETE below succeeds and
	// silently orphans every save.
	if got := pragma(t, db, "PRAGMA foreign_keys;"); got != "1" {
		t.Fatalf("foreign_keys = %q, want %q; a declared cascade is not enforced without it", got, "1")
	}
	if got := pragma(t, db,
		`SELECT "on_delete" FROM pragma_foreign_key_list('saves') WHERE "table" = 'games';`); got != "CASCADE" {
		t.Fatalf("saves.game_id on delete = %q, want %q", got, "CASCADE")
	}

	doomed, err := sessions.Create(ctx, storedSession(owner))
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	kept, err := sessions.Create(ctx, storedSession(owner))
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	for _, name := range []string{"before troll", "after troll"} {
		if _, _, err := sessions.CreateSave(ctx, storedSnapshot(doomed, name)); err != nil {
			t.Fatalf("CreateSave(%q) error = %v", name, err)
		}
	}
	if _, _, err := sessions.CreateSave(ctx, storedSnapshot(kept, "somewhere else")); err != nil {
		t.Fatalf("CreateSave() error = %v", err)
	}

	if got := pragma(t, db, "SELECT count(*) FROM saves;"); got != "3" {
		t.Fatalf("the saves table holds %s rows, want 3", got)
	}

	if err := sessions.Delete(ctx, owner, doomed.ID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	// Counted in the table rather than asked for through the store: a save that
	// is merely unreachable is still a row, and it is the largest one there is.
	if got := pragma(t, db, "SELECT count(*) FROM saves;"); got != "1" {
		t.Errorf("the saves table holds %s rows after the delete, want 1", got)
	}
	if got := pragma(t, db, "SELECT count(*) FROM games;"); got != "1" {
		t.Errorf("the games table holds %s rows after the delete, want 1", got)
	}

	// The other game kept every one of its own.
	saves, err := sessions.Saves(ctx, owner, kept.ID)
	if err != nil {
		t.Fatalf("Saves() error = %v", err)
	}
	if len(saves) != 1 || saves[0].Name != "somewhere else" {
		t.Errorf("the other game holds %+v, want the save it was given", saves)
	}
}

// The milestone's own requirement: a game survives the process that was
// playing it.
func TestGameSurvivesAServerRestart(t *testing.T) {
	ctx := context.Background()
	path := testPath(t)

	library, err := game.Embedded()
	if err != nil {
		t.Fatalf("Embedded() error = %v", err)
	}

	first := openAt(t, path, true)
	owner := testUser(t, first, "player@example.com")

	service, err := game.NewService(library, game.NewRunner(), first.Sessions())
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	session, opening, err := service.NewGame(ctx, owner, "zork1")
	if err != nil {
		t.Fatalf("NewGame() error = %v", err)
	}
	if !strings.Contains(opening.Output, "West of House") {
		t.Fatalf("the game did not open: %q", opening.Output)
	}
	for _, command := range []string{"open mailbox", "take leaflet", "north"} {
		if _, err := service.Play(ctx, owner, session.ID, command); err != nil {
			t.Fatalf("Play(%q) error = %v", command, err)
		}
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	// A different process, with nothing in common but the file.
	second := openAt(t, path, false)
	resumed, err := game.NewService(library, game.NewRunner(), second.Sessions())
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	turn, err := resumed.Play(ctx, owner, session.ID, "inventory")
	if err != nil {
		t.Fatalf("Play() after the restart error = %v", err)
	}
	after, result := turn.Session, turn.Result
	if !strings.Contains(result.Output, "leaflet") {
		t.Errorf("the leaflet did not survive the restart: %q", result.Output)
	}
	if result.StatusLine.Name != "North of House" {
		t.Errorf("StatusLine.Name = %q, want %q", result.StatusLine.Name, "North of House")
	}
	if after.Turn != 4 {
		t.Errorf("session.Turn = %d, want 4", after.Turn)
	}
}

// Turns submitted at once are serialized by the per-session lock and land one
// after another in the database. None of them may go missing.
func TestConcurrentTurnsThroughSQLite(t *testing.T) {
	const turns = 8

	ctx := context.Background()
	db := testDB(t)
	owner := testUser(t, db, "player@example.com")

	library, err := game.Embedded()
	if err != nil {
		t.Fatalf("Embedded() error = %v", err)
	}
	service, err := game.NewService(library, game.NewRunner(), db.Sessions())
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	session, _, err := service.NewGame(ctx, owner, "zork1")
	if err != nil {
		t.Fatalf("NewGame() error = %v", err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, turns)

	for range turns {
		wg.Go(func() {
			if _, err := service.Play(ctx, owner, session.ID, "look"); err != nil {
				errs <- err
			}
		})
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("Play() error = %v", err)
	}

	final, err := db.Sessions().Load(ctx, owner, session.ID)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if final.Turn != turns {
		t.Errorf("turn = %d, want %d; a command was lost", final.Turn, turns)
	}
	if final.Version != turns+1 {
		t.Errorf("version = %d, want %d", final.Version, turns+1)
	}
}
