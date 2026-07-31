package game

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Errors a caller is expected to handle.
var (
	// ErrSaveNotFound means the game holds no save under that identifier or
	// name.
	ErrSaveNotFound = errors.New("game: save not found")

	// ErrInvalidSaveName means the name cannot be stored as typed.
	ErrInvalidSaveName = errors.New("game: unusable save name")

	// ErrTooManySaves means the game already holds MaxSavesPerGame saves and
	// one has to go before another can be written.
	ErrTooManySaves = errors.New("game: too many saves for this game")

	// ErrSaveMismatch means the save was written from a different story image
	// than the game it is being restored into. Restoring it would hand the
	// engine bytes that do not describe its memory.
	ErrSaveMismatch = errors.New("game: save belongs to a different story")
)

// MaxSaveNameBytes bounds a save name.
//
// It is measured in bytes because that is what the column holds; a name in a
// script that spends three bytes a character therefore gets fewer characters,
// which is the honest limit rather than a pleasant one.
const MaxSaveNameBytes = 48

// MaxSavesPerGame bounds how many named saves one game may hold.
//
// Each save carries a whole copy of the screen as well as the state, so this is
// what keeps one player's shelf of saves from becoming the largest thing in the
// database. Twenty is far more than the three or four an Infocom disk held.
const MaxSavesPerGame = 20

// A Save is one named snapshot of a game, without the bytes that restore it.
//
// A list of saves is read to choose between them, so it does not carry the
// state or the transcript: those are read only by the restore that wants them.
type Save struct {
	ID   string
	Name string

	// Turn is the game's command count at the moment the save was written.
	Turn int

	CreatedAt time.Time
}

// A Snapshot is a save with everything needed to put the screen back.
//
// The screen travels with the state deliberately. Restoring promotes these
// bytes to the game's active state, and a transcript left over from after the
// save point would then be describing a game that is no longer there.
type Snapshot struct {
	ID     string
	UserID string
	GameID string
	Name   string

	// StoryKey identifies the exact story image State was written from, and is
	// checked against the game's before a restore.
	StoryKey StoryKey

	State      []byte
	Transcript string
	Status     StatusLine
	Turn       int
	CreatedAt  time.Time
}

// CleanSaveName normalizes a name and reports whether it can be stored.
//
// The name is data and only ever data: it goes into a column, it is compared as
// a string, and it is escaped when it is drawn. It never becomes a file name
// and never reaches a filesystem, so the rules here are about a name a person
// can read back rather than about path traversal.
//
// Runs of whitespace collapse to single spaces, so a name cannot be made to
// look like another one by padding it, and a name that is nothing but spaces is
// refused rather than stored as empty.
func CleanSaveName(name string) (string, error) {
	if !utf8.ValidString(name) {
		return "", fmt.Errorf("game: name is not valid UTF-8: %w", ErrInvalidSaveName)
	}

	name = strings.Join(strings.Fields(name), " ")

	switch {
	case name == "":
		return "", fmt.Errorf("game: name is empty: %w", ErrInvalidSaveName)
	case len(name) > MaxSaveNameBytes:
		return "", fmt.Errorf("game: name is %d bytes, limit is %d: %w",
			len(name), MaxSaveNameBytes, ErrInvalidSaveName)
	}

	// Fields removed the whitespace controls; these are the rest — an escape
	// or a NUL that would draw as nothing and make two different names look
	// identical in the list.
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("game: name holds a control character: %w", ErrInvalidSaveName)
		}
	}

	return name, nil
}

// sameSaveName reports whether two names are the same save.
//
// Names are matched without regard to case, so a player who saved "Troll" and
// then saves "troll" replaces it rather than ending up with two saves that look
// the same in a list.
func sameSaveName(a, b string) bool { return strings.EqualFold(a, b) }

// Saves returns the game's saves, newest first.
func (s *Service) Saves(ctx context.Context, userID, gameID string) ([]Save, error) {
	if userID == "" {
		return nil, errors.New("game: saves: no user")
	}

	saves, err := s.store.Saves(ctx, userID, gameID)
	if err != nil {
		return nil, fmt.Errorf("game: session %s: list saves: %w", gameID, err)
	}
	return saves, nil
}

// Save writes the game's current state under a name.
//
// A name the game already holds is replaced, and the second return value says
// so. Replacing is how a player keeps one slot up to date, and refusing would
// only teach them to delete first.
func (s *Service) Save(ctx context.Context, userID, gameID, name string) (Save, bool, error) {
	if userID == "" {
		return Save{}, false, errors.New("game: save: no user")
	}

	clean, err := CleanSaveName(name)
	if err != nil {
		return Save{}, false, err
	}

	unlock := s.locks.lock(gameID)
	defer unlock()

	_, save, replaced, err := s.save(ctx, userID, gameID, clean, "save")
	return save, replaced, err
}

// Restore promotes a save's bytes to the game's active state.
//
// There is no engine call in it beyond the ordinary New and Restore of the next
// turn: a state is a complete, self-contained snapshot, so the whole operation
// is a conditional write of bytes this application already holds.
func (s *Service) Restore(ctx context.Context, userID, gameID, saveID string) (Session, Save, error) {
	if userID == "" {
		return Session{}, Save{}, errors.New("game: restore: no user")
	}

	unlock := s.locks.lock(gameID)
	defer unlock()

	return s.restore(ctx, userID, gameID, saveID, "restore")
}

// DeleteSave removes one save and returns what it removed.
//
// Every state stands alone, so deleting one takes nothing away from the others
// and nothing away from the game in progress.
func (s *Service) DeleteSave(ctx context.Context, userID, gameID, saveID string) (Save, error) {
	if userID == "" {
		return Save{}, errors.New("game: delete save: no user")
	}

	deleted, err := s.store.DeleteSave(ctx, userID, gameID, saveID)
	if err != nil {
		return Save{}, fmt.Errorf("game: session %s: delete save %s: %w", gameID, saveID, err)
	}
	return deleted, nil
}

// save writes a save with the session lock already held.
//
// echo is the line the transcript records as having asked for it, which is the
// player's line when they typed one and the bare verb when the name arrived
// from a field instead.
func (s *Service) save(ctx context.Context, userID, gameID, name, echo string) (Session, Save, bool, error) {
	session, err := s.store.Load(ctx, userID, gameID)
	if err != nil {
		return Session{}, Save{}, false, fmt.Errorf("game: session %s: load: %w", gameID, err)
	}
	if len(session.State) == 0 {
		// A halted story returns no state, so there is nothing to write. The
		// game is over rather than damaged.
		return Session{}, Save{}, false, fmt.Errorf("game: session %s: %w", gameID, ErrGameOver)
	}

	save, replaced, err := s.store.CreateSave(ctx, Snapshot{
		UserID:     userID,
		GameID:     gameID,
		Name:       name,
		StoryKey:   session.StoryKey,
		State:      session.State,
		Transcript: session.Transcript,
		Status:     session.Status,
		Turn:       session.Turn,
	})
	if err != nil {
		return Session{}, Save{}, false, fmt.Errorf("game: session %s: write save %q: %w", gameID, name, err)
	}

	// Say so on the screen as well, so a browser that refreshes still shows
	// that the save happened.
	//
	// A failure here is deliberately not the caller's problem: the save is
	// written, which is what was asked for, and the only thing lost is the line
	// about it. The one realistic cause is another process playing a turn
	// between the load above and this write — the per-session lock rules out
	// the rest — and reporting that as a failed save would be a lie.
	session.Transcript = appendNotice(session.Transcript, echo, savedNotice(save.Name, replaced))
	if updated, err := s.store.Update(ctx, session); err == nil {
		session = updated
	}

	return session, save, replaced, nil
}

// restore promotes a save with the session lock already held. echo is the line
// the transcript records, as in [Service.save].
func (s *Service) restore(ctx context.Context, userID, gameID, saveID, echo string) (Session, Save, error) {
	session, err := s.store.Load(ctx, userID, gameID)
	if err != nil {
		return Session{}, Save{}, fmt.Errorf("game: session %s: load: %w", gameID, err)
	}

	snapshot, err := s.store.LoadSave(ctx, userID, gameID, saveID)
	if err != nil {
		return Session{}, Save{}, fmt.Errorf("game: session %s: load save %s: %w", gameID, saveID, err)
	}
	if snapshot.StoryKey != session.StoryKey {
		return Session{}, Save{}, fmt.Errorf("game: session %s: save %s: %w", gameID, saveID, ErrSaveMismatch)
	}

	save := Save{ID: snapshot.ID, Name: snapshot.Name, Turn: snapshot.Turn, CreatedAt: snapshot.CreatedAt}

	session.State = snapshot.State
	session.Status = snapshot.Status
	session.Turn = snapshot.Turn

	// The screen goes back with the state. Keeping the newer transcript would
	// leave the player reading about a game that no longer exists.
	session.Transcript = appendNotice(snapshot.Transcript, echo, restoredNotice(snapshot.Name))

	// Restoring un-halts. The story ended itself, and this is going back to
	// before it did; the state being written is a state it asked for.
	session.Halted = false

	session, err = s.store.Update(ctx, session)
	if err != nil {
		return Session{}, Save{}, fmt.Errorf("game: session %s: restore save %s: %w", gameID, saveID, err)
	}

	return session, save, nil
}

// findSave returns the save the player named.
func findSave(saves []Save, name string) (Save, bool) {
	for _, save := range saves {
		if sameSaveName(save.Name, name) {
			return save, true
		}
	}
	return Save{}, false
}

// Notice is the line the transcript records for a line this application
// answered itself.
//
// A caller that shows the turn without redrawing the page shows this, so that
// what a player reads live and what they read after a refresh are the same
// sentence rather than two that have to be kept in step.
func (t Turn) Notice() string {
	switch t.Intent {
	case IntentSave:
		return savedNotice(t.Save.Name, t.Replaced)
	case IntentRestore:
		return restoredNotice(t.Save.Name)
	}
	return ""
}

// savedNotice is what the terminal says after a save.
func savedNotice(name string, replaced bool) string {
	if replaced {
		return fmt.Sprintf("Saved as %q, replacing the earlier one.", name)
	}
	return fmt.Sprintf("Saved as %q.", name)
}

// restoredNotice is what the terminal says after a restore.
func restoredNotice(name string) string { return fmt.Sprintf("Restored %q.", name) }
