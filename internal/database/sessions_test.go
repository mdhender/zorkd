package database

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/mdhender/zorkd/internal/game"
)

func storedSession() game.Session {
	return game.Session{
		StoryKey: game.StoryKeyOf([]byte("a story")),
		State:    []byte("saved state"),
	}
}

func TestSessionsRoundTrip(t *testing.T) {
	ctx := context.Background()
	sessions := testDB(t).Sessions()

	created, err := sessions.Create(ctx, storedSession())
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if created.ID == "" {
		t.Fatal("Create() assigned no identifier")
	}
	if created.Version != 1 {
		t.Errorf("Create() version = %d, want 1", created.Version)
	}

	loaded, err := sessions.Load(ctx, created.ID)
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

	again, err := sessions.Load(ctx, created.ID)
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
	sessions := testDB(t).Sessions()

	created, err := sessions.Create(ctx, storedSession())
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

	loaded, err := sessions.Load(ctx, created.ID)
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
	sessions := testDB(t).Sessions()

	ids := []string{"1", "0", "-3", "", "abc", "1; DROP TABLE games", "../../etc/passwd"}

	for _, id := range ids {
		t.Run(id, func(t *testing.T) {
			if _, err := sessions.Load(ctx, id); !errors.Is(err, game.ErrSessionNotFound) {
				t.Errorf("Load(%q) error = %v, want %v", id, err, game.ErrSessionNotFound)
			}
			if _, err := sessions.Update(ctx, game.Session{ID: id, State: []byte("x")}); !errors.Is(err, game.ErrSessionNotFound) {
				t.Errorf("Update(%q) error = %v, want %v", id, err, game.ErrSessionNotFound)
			}
		})
	}
}

// A halted session is the one case where storing no state is right, and the
// only one the schema allows.
func TestSessionsHalted(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	sessions := db.Sessions()

	created, err := sessions.Create(ctx, storedSession())
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

	loaded, err := sessions.Load(ctx, created.ID)
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
	sessions := testDB(t).Sessions()

	if _, err := sessions.Create(ctx, game.Session{StoryKey: game.StoryKeyOf([]byte("a story"))}); err == nil {
		t.Fatal("Create() = nil error, want the check constraint to refuse it")
	}

	created, err := sessions.Create(ctx, storedSession())
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	blanked := created
	blanked.State = nil
	if _, err := sessions.Update(ctx, blanked); err == nil {
		t.Fatal("Update() = nil error, want the check constraint to refuse it")
	}

	loaded, err := sessions.Load(ctx, created.ID)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if string(loaded.State) != "saved state" {
		t.Errorf("the refused write reached the row: %q", loaded.State)
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

	first := openAt(t, path)
	service, err := game.NewService(library, game.NewRunner(), first.Sessions())
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	session, opening, err := service.NewGame(ctx, "zork1")
	if err != nil {
		t.Fatalf("NewGame() error = %v", err)
	}
	if !strings.Contains(opening.Output, "West of House") {
		t.Fatalf("the game did not open: %q", opening.Output)
	}
	for _, command := range []string{"open mailbox", "take leaflet", "north"} {
		if _, _, err := service.Play(ctx, session.ID, command); err != nil {
			t.Fatalf("Play(%q) error = %v", command, err)
		}
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	// A different process, with nothing in common but the file.
	second := openAt(t, path)
	resumed, err := game.NewService(library, game.NewRunner(), second.Sessions())
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	after, result, err := resumed.Play(ctx, session.ID, "inventory")
	if err != nil {
		t.Fatalf("Play() after the restart error = %v", err)
	}
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

	library, err := game.Embedded()
	if err != nil {
		t.Fatalf("Embedded() error = %v", err)
	}
	service, err := game.NewService(library, game.NewRunner(), db.Sessions())
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	session, _, err := service.NewGame(ctx, "zork1")
	if err != nil {
		t.Fatalf("NewGame() error = %v", err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, turns)

	for range turns {
		wg.Go(func() {
			if _, _, err := service.Play(ctx, session.ID, "look"); err != nil {
				errs <- err
			}
		})
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("Play() error = %v", err)
	}

	final, err := db.Sessions().Load(ctx, session.ID)
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
