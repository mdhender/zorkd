package database

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strconv"
	"time"

	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"

	"github.com/mdhender/zorkd/internal/game"
)

// The named-save half of [game.Store].
//
// Every statement here joins through games to a user. A save is reached by way
// of the game it belongs to and never on its own, so there is no query that
// could return somebody else's save by being called with the wrong argument.

// CreateSave writes a snapshot under its name, replacing one the game already
// holds under that name.
//
// The lookup, the count and the write are one transaction: without it, two
// requests could each find no save under a name and both insert it, or each see
// room for one more and take the room twice.
func (s *Sessions) CreateSave(ctx context.Context, snapshot game.Snapshot) (game.Save, bool, error) {
	owner, err := userRowID(snapshot.UserID)
	if err != nil {
		return game.Save{}, false, fmt.Errorf("database: session %s: %w", snapshot.GameID, game.ErrSessionNotFound)
	}

	gameID, err := rowID(snapshot.GameID)
	if err != nil {
		return game.Save{}, false, err
	}
	if len(snapshot.State) == 0 {
		return game.Save{}, false, fmt.Errorf("database: session %s: save %q: no state",
			snapshot.GameID, snapshot.Name)
	}

	conn, release, err := s.db.conn(ctx)
	if err != nil {
		return game.Save{}, false, err
	}
	defer release()

	end, err := sqlitex.ImmediateTransaction(conn)
	if err != nil {
		return game.Save{}, false, fmt.Errorf("database: session %s: write save: %w", snapshot.GameID, err)
	}
	defer end(&err)

	if err = ownsGame(conn, owner, gameID); err != nil {
		return game.Save{}, false, err
	}

	existing, err := saveIDByName(conn, gameID, snapshot.Name)
	if err != nil {
		return game.Save{}, false, err
	}

	snapshot.CreatedAt = time.Now()
	replaced := existing != 0

	if replaced {
		err = replaceSave(conn, existing, snapshot)
	} else {
		var held int
		if held, err = countSaves(conn, gameID); err != nil {
			return game.Save{}, false, err
		}
		if held >= game.MaxSavesPerGame {
			err = fmt.Errorf("database: session %s: %d saves: %w",
				snapshot.GameID, held, game.ErrTooManySaves)
			return game.Save{}, false, err
		}
		existing, err = insertSave(conn, gameID, snapshot)
	}
	if err != nil {
		return game.Save{}, false, err
	}

	return game.Save{
		ID:        strconv.FormatInt(existing, 10),
		Name:      snapshot.Name,
		Turn:      snapshot.Turn,
		CreatedAt: snapshot.CreatedAt,
	}, replaced, nil
}

// Saves returns the game's saves, newest first.
//
// It reads neither the state nor the transcript: a list of saves is read to
// choose between them, and the bytes that restore one are the largest thing in
// the row.
func (s *Sessions) Saves(ctx context.Context, userID, gameID string) ([]game.Save, error) {
	owner, err := userRowID(userID)
	if err != nil {
		return nil, fmt.Errorf("database: session %s: %w", gameID, game.ErrSessionNotFound)
	}

	id, err := rowID(gameID)
	if err != nil {
		return nil, err
	}

	conn, release, err := s.db.conn(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	if err := ownsGame(conn, owner, id); err != nil {
		return nil, err
	}

	var (
		saves []game.Save
		bad   error
	)

	err = sqlitex.Execute(conn,
		`SELECT id, name, turn, created_at
		   FROM saves WHERE game_id = ? ORDER BY created_at DESC, id DESC;`,
		&sqlitex.ExecOptions{
			Args: []any{id},
			ResultFunc: func(stmt *sqlite.Stmt) error {
				created, err := time.Parse(time.RFC3339, stmt.GetText("created_at"))
				if err != nil {
					bad = fmt.Errorf("unreadable timestamp %q", stmt.GetText("created_at"))
					return nil
				}

				saves = append(saves, game.Save{
					ID:        strconv.FormatInt(stmt.GetInt64("id"), 10),
					Name:      stmt.GetText("name"),
					Turn:      int(stmt.GetInt64("turn")),
					CreatedAt: created,
				})
				return nil
			},
		})
	if err != nil {
		return nil, fmt.Errorf("database: session %s: list saves: %w", gameID, err)
	}
	if bad != nil {
		return nil, fmt.Errorf("database: session %s: list saves: %w", gameID, bad)
	}

	return saves, nil
}

// LoadSave returns one save with the bytes that restore it.
func (s *Sessions) LoadSave(ctx context.Context, userID, gameID, saveID string) (game.Snapshot, error) {
	owner, err := userRowID(userID)
	if err != nil {
		return game.Snapshot{}, fmt.Errorf("database: session %s: %w", gameID, game.ErrSessionNotFound)
	}

	id, err := rowID(gameID)
	if err != nil {
		return game.Snapshot{}, err
	}

	save, err := saveRowID(saveID)
	if err != nil {
		return game.Snapshot{}, err
	}

	conn, release, err := s.db.conn(ctx)
	if err != nil {
		return game.Snapshot{}, err
	}
	defer release()

	if err := ownsGame(conn, owner, id); err != nil {
		return game.Snapshot{}, err
	}

	var (
		snapshot game.Snapshot
		found    bool
		bad      error
	)

	err = sqlitex.Execute(conn,
		`SELECT id, name, story_key, state, transcript, turn,
		        status_available, status_name, status_time_game,
		        status_score, status_moves, status_hours, status_minutes, created_at
		   FROM saves WHERE id = ? AND game_id = ?;`,
		&sqlitex.ExecOptions{
			Args: []any{save, id},
			ResultFunc: func(stmt *sqlite.Stmt) error {
				found = true

				snapshot.ID = strconv.FormatInt(stmt.GetInt64("id"), 10)
				snapshot.UserID = userID
				snapshot.GameID = gameID
				snapshot.Name = stmt.GetText("name")
				snapshot.Transcript = stmt.GetText("transcript")
				snapshot.Turn = int(stmt.GetInt64("turn"))
				snapshot.Status = readStatus(stmt)

				if n := stmt.GetLen("story_key"); n != sha256.Size {
					bad = fmt.Errorf("story key is %d bytes, want %d", n, sha256.Size)
					return nil
				}
				stmt.GetBytes("story_key", snapshot.StoryKey[:])

				if n := stmt.GetLen("state"); n > 0 {
					snapshot.State = make([]byte, n)
					stmt.GetBytes("state", snapshot.State)
				}

				created, err := time.Parse(time.RFC3339, stmt.GetText("created_at"))
				if err != nil {
					bad = fmt.Errorf("unreadable timestamp %q", stmt.GetText("created_at"))
					return nil
				}
				snapshot.CreatedAt = created

				return nil
			},
		})
	if err != nil {
		return game.Snapshot{}, fmt.Errorf("database: session %s: load save %s: %w", gameID, saveID, err)
	}
	if !found {
		return game.Snapshot{}, fmt.Errorf("database: session %s: save %s: %w", gameID, saveID, game.ErrSaveNotFound)
	}
	if bad != nil {
		return game.Snapshot{}, fmt.Errorf("database: session %s: load save %s: %w", gameID, saveID, bad)
	}

	return snapshot, nil
}

// DeleteSave removes one save and returns what it removed.
func (s *Sessions) DeleteSave(ctx context.Context, userID, gameID, saveID string) (game.Save, error) {
	snapshot, err := s.LoadSave(ctx, userID, gameID, saveID)
	if err != nil {
		return game.Save{}, err
	}

	id, err := saveRowID(saveID)
	if err != nil {
		return game.Save{}, err
	}

	conn, release, err := s.db.conn(ctx)
	if err != nil {
		return game.Save{}, err
	}
	defer release()

	// The game identifier stays in the WHERE clause even though LoadSave has
	// already answered for it: this is the statement that deletes, and it is
	// the one that has to be unable to reach another game's row.
	gameRow, err := rowID(gameID)
	if err != nil {
		return game.Save{}, err
	}

	err = sqlitex.Execute(conn, `DELETE FROM saves WHERE id = ? AND game_id = ?;`,
		&sqlitex.ExecOptions{Args: []any{id, gameRow}})
	if err != nil {
		return game.Save{}, fmt.Errorf("database: session %s: delete save %s: %w", gameID, saveID, err)
	}
	if conn.Changes() == 0 {
		return game.Save{}, fmt.Errorf("database: session %s: save %s: %w", gameID, saveID, game.ErrSaveNotFound)
	}

	return game.Save{
		ID:        snapshot.ID,
		Name:      snapshot.Name,
		Turn:      snapshot.Turn,
		CreatedAt: snapshot.CreatedAt,
	}, nil
}

// ownsGame reports whether the game exists and belongs to the user.
//
// A game that is somebody else's reads as missing, because saying "not yours"
// would confirm that it exists.
func ownsGame(conn *sqlite.Conn, owner, gameID int64) error {
	var found bool

	err := sqlitex.Execute(conn, `SELECT 1 FROM games WHERE id = ? AND user_id = ?;`,
		&sqlitex.ExecOptions{
			Args:       []any{gameID, owner},
			ResultFunc: func(*sqlite.Stmt) error { found = true; return nil },
		})
	if err != nil {
		return fmt.Errorf("database: session %d: %w", gameID, err)
	}
	if !found {
		return fmt.Errorf("database: session %d: %w", gameID, game.ErrSessionNotFound)
	}
	return nil
}

// saveIDByName returns the row a name already occupies, or zero.
func saveIDByName(conn *sqlite.Conn, gameID int64, name string) (int64, error) {
	var id int64

	err := sqlitex.Execute(conn,
		`SELECT id FROM saves WHERE game_id = ? AND name = ? COLLATE NOCASE;`,
		&sqlitex.ExecOptions{
			Args: []any{gameID, name},
			ResultFunc: func(stmt *sqlite.Stmt) error {
				id = stmt.GetInt64("id")
				return nil
			},
		})
	if err != nil {
		return 0, fmt.Errorf("database: session %d: find save %q: %w", gameID, name, err)
	}
	return id, nil
}

func countSaves(conn *sqlite.Conn, gameID int64) (int, error) {
	var held int

	err := sqlitex.Execute(conn, `SELECT count(*) AS held FROM saves WHERE game_id = ?;`,
		&sqlitex.ExecOptions{
			Args: []any{gameID},
			ResultFunc: func(stmt *sqlite.Stmt) error {
				held = int(stmt.GetInt64("held"))
				return nil
			},
		})
	if err != nil {
		return 0, fmt.Errorf("database: session %d: count saves: %w", gameID, err)
	}
	return held, nil
}

func insertSave(conn *sqlite.Conn, gameID int64, snapshot game.Snapshot) (int64, error) {
	stmt, err := conn.Prepare(
		`INSERT INTO saves (game_id, name, story_key, state, transcript, turn,
		                    status_available, status_name, status_time_game,
		                    status_score, status_moves, status_hours, status_minutes,
		                    created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);`)
	if err != nil {
		return 0, fmt.Errorf("database: session %d: write save: %w", gameID, err)
	}
	defer stmt.Reset()

	stmt.BindInt64(1, gameID)
	stmt.BindText(2, snapshot.Name)
	stmt.BindBytes(3, snapshot.StoryKey[:])
	stmt.BindBytes(4, snapshot.State)
	stmt.BindText(5, snapshot.Transcript)
	stmt.BindInt64(6, int64(snapshot.Turn))
	bindStatus(stmt, 7, snapshot.Status)
	stmt.BindText(14, stamp(snapshot.CreatedAt))

	if _, err := stmt.Step(); err != nil {
		return 0, fmt.Errorf("database: session %d: write save: %w", gameID, err)
	}

	return conn.LastInsertRowID(), nil
}

// replaceSave overwrites a save in place.
//
// The row keeps its identifier, so a page that was already showing the save
// still points at it, and created_at moves to now because that is when these
// bytes were written.
func replaceSave(conn *sqlite.Conn, saveID int64, snapshot game.Snapshot) error {
	stmt, err := conn.Prepare(
		`UPDATE saves
		    SET name = ?, story_key = ?, state = ?, transcript = ?, turn = ?,
		        status_available = ?, status_name = ?, status_time_game = ?,
		        status_score = ?, status_moves = ?, status_hours = ?, status_minutes = ?,
		        created_at = ?
		  WHERE id = ?;`)
	if err != nil {
		return fmt.Errorf("database: save %d: replace: %w", saveID, err)
	}
	defer stmt.Reset()

	stmt.BindText(1, snapshot.Name)
	stmt.BindBytes(2, snapshot.StoryKey[:])
	stmt.BindBytes(3, snapshot.State)
	stmt.BindText(4, snapshot.Transcript)
	stmt.BindInt64(5, int64(snapshot.Turn))
	bindStatus(stmt, 6, snapshot.Status)
	stmt.BindText(13, stamp(snapshot.CreatedAt))
	stmt.BindInt64(14, saveID)

	if _, err := stmt.Step(); err != nil {
		return fmt.Errorf("database: save %d: replace: %w", saveID, err)
	}
	return nil
}

// saveRowID converts a save identifier that came from outside.
func saveRowID(id string) (int64, error) {
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("database: save %q: %w", id, game.ErrSaveNotFound)
	}
	return n, nil
}
