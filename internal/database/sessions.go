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

// Sessions stores games in progress. It implements [game.Store].
//
// The state is written whole and read back whole. Nothing here parses it,
// compresses it, or derives a column from it: the score and the room come from
// the engine's status line, and a field recovered by hand would tie this
// application to a save format that is free to change.
type Sessions struct {
	db *DB
}

var _ game.Store = (*Sessions)(nil)

// Create stores a new session and returns it with its identifier and version.
func (s *Sessions) Create(ctx context.Context, session game.Session) (game.Session, error) {
	userID, err := userRowID(session.UserID)
	if err != nil {
		return game.Session{}, fmt.Errorf("database: create session: %w", err)
	}

	conn, release, err := s.db.conn(ctx)
	if err != nil {
		return game.Session{}, err
	}
	defer release()

	stmt, err := conn.Prepare(
		`INSERT INTO games (user_id, story_key, state, transcript, turn, version, halted,
		                    status_available, status_name, status_time_game,
		                    status_score, status_moves, status_hours, status_minutes,
		                    created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);`)
	if err != nil {
		return game.Session{}, fmt.Errorf("database: create session: %w", err)
	}
	defer stmt.Reset()

	at := now()
	stmt.BindInt64(1, userID)
	stmt.BindBytes(2, session.StoryKey[:])
	bindState(stmt, 3, session.State)
	stmt.BindText(4, session.Transcript)
	stmt.BindInt64(5, int64(session.Turn))
	stmt.BindBool(6, session.Halted)
	bindStatus(stmt, 7, session.Status)
	stmt.BindText(14, at)
	stmt.BindText(15, at)

	if _, err := stmt.Step(); err != nil {
		return game.Session{}, fmt.Errorf("database: create session: %w", err)
	}

	session.ID = strconv.FormatInt(conn.LastInsertRowID(), 10)
	session.Version = 1

	return session, nil
}

// Load returns the user's session.
//
// The owner is part of the query rather than a check made afterwards, so a
// session that belongs to somebody else cannot be read at all — and it reads as
// missing, because saying "not yours" would confirm that it exists.
func (s *Sessions) Load(ctx context.Context, userID, id string) (game.Session, error) {
	owner, err := userRowID(userID)
	if err != nil {
		return game.Session{}, fmt.Errorf("database: session %s: %w", id, game.ErrSessionNotFound)
	}

	rowID, err := rowID(id)
	if err != nil {
		return game.Session{}, err
	}

	conn, release, err := s.db.conn(ctx)
	if err != nil {
		return game.Session{}, err
	}
	defer release()

	return loadSession(conn, owner, rowID)
}

// Update writes the session only if the stored version is still the one that
// was read.
//
// The condition is the whole point: two turns that started from the same state
// would otherwise both write, and the player of the losing one would watch a
// command vanish. The loser is refused instead, with ErrVersionConflict.
func (s *Sessions) Update(ctx context.Context, session game.Session) (game.Session, error) {
	owner, err := userRowID(session.UserID)
	if err != nil {
		return game.Session{}, fmt.Errorf("database: session %s: %w", session.ID, game.ErrSessionNotFound)
	}

	rowID, err := rowID(session.ID)
	if err != nil {
		return game.Session{}, err
	}

	conn, release, err := s.db.conn(ctx)
	if err != nil {
		return game.Session{}, err
	}
	defer release()

	stmt, err := conn.Prepare(
		`UPDATE games
		    SET state = ?, transcript = ?, turn = ?, halted = ?,
		        status_available = ?, status_name = ?, status_time_game = ?,
		        status_score = ?, status_moves = ?, status_hours = ?, status_minutes = ?,
		        version = version + 1, updated_at = ?
		  WHERE id = ? AND user_id = ? AND version = ?;`)
	if err != nil {
		return game.Session{}, fmt.Errorf("database: session %s: update: %w", session.ID, err)
	}
	defer stmt.Reset()

	bindState(stmt, 1, session.State)
	stmt.BindText(2, session.Transcript)
	stmt.BindInt64(3, int64(session.Turn))
	stmt.BindBool(4, session.Halted)
	bindStatus(stmt, 5, session.Status)
	stmt.BindText(12, now())
	stmt.BindInt64(13, rowID)
	stmt.BindInt64(14, owner)
	stmt.BindInt64(15, session.Version)

	if _, err := stmt.Step(); err != nil {
		return game.Session{}, fmt.Errorf("database: session %s: update: %w", session.ID, err)
	}

	if conn.Changes() == 0 {
		// Nothing matched: the row is gone, it is not this user's, or its
		// version moved on while the turn was being played.
		if _, err := loadSession(conn, owner, rowID); err != nil {
			return game.Session{}, err
		}
		return game.Session{}, fmt.Errorf("database: session %s: %w", session.ID, game.ErrVersionConflict)
	}

	session.Version++
	return session, nil
}

// List returns the user's games, most recently played first.
//
// It reads neither the state nor the transcript: a menu is read to choose
// between games, not to play them, and the bytes that resume one are the
// largest thing in the row.
func (s *Sessions) List(ctx context.Context, userID string) ([]game.Summary, error) {
	owner, err := userRowID(userID)
	if err != nil {
		// Nobody has no games, which is the honest answer for an identifier
		// that could never have been assigned.
		return nil, nil
	}

	conn, release, err := s.db.conn(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	var (
		summaries []game.Summary
		bad       error
	)

	err = sqlitex.Execute(conn,
		`SELECT id, story_key, turn, halted, updated_at
		   FROM games WHERE user_id = ? ORDER BY updated_at DESC, id DESC;`,
		&sqlitex.ExecOptions{
			Args: []any{owner},
			ResultFunc: func(stmt *sqlite.Stmt) error {
				summary := game.Summary{
					ID:     strconv.FormatInt(stmt.GetInt64("id"), 10),
					Turn:   int(stmt.GetInt64("turn")),
					Halted: stmt.GetBool("halted"),
				}

				if n := stmt.GetLen("story_key"); n != sha256.Size {
					bad = fmt.Errorf("story key is %d bytes, want %d", n, sha256.Size)
					return nil
				}
				stmt.GetBytes("story_key", summary.StoryKey[:])

				updated, err := time.Parse(time.RFC3339, stmt.GetText("updated_at"))
				if err != nil {
					bad = fmt.Errorf("unreadable timestamp %q", stmt.GetText("updated_at"))
					return nil
				}
				summary.UpdatedAt = updated

				summaries = append(summaries, summary)
				return nil
			},
		})
	if err != nil {
		return nil, fmt.Errorf("database: user %s: list games: %w", userID, err)
	}
	if bad != nil {
		return nil, fmt.Errorf("database: user %s: list games: %w", userID, bad)
	}

	return summaries, nil
}

// Delete removes the user's game and every save it holds.
//
// The owner is in the WHERE clause, as it is everywhere else here, so a game
// that belongs to somebody else cannot be deleted — and it reads as missing,
// because saying "not yours" would confirm that it exists.
//
// The saves are not deleted by a statement of their own: saves.game_id declares
// ON DELETE CASCADE and every pooled connection is opened with foreign keys
// enforced (see Open), so the one DELETE takes them with it. SQLite enforces
// foreign keys only when asked to, which is why that pragma is not a detail:
// without it the cascade is declared and does nothing, and the saves would
// outlive the game as rows nothing can reach.
func (s *Sessions) Delete(ctx context.Context, userID, id string) error {
	owner, err := userRowID(userID)
	if err != nil {
		return fmt.Errorf("database: session %s: %w", id, game.ErrSessionNotFound)
	}

	rowID, err := rowID(id)
	if err != nil {
		return err
	}

	conn, release, err := s.db.conn(ctx)
	if err != nil {
		return err
	}
	defer release()

	err = sqlitex.Execute(conn, `DELETE FROM games WHERE id = ? AND user_id = ?;`,
		&sqlitex.ExecOptions{Args: []any{rowID, owner}})
	if err != nil {
		return fmt.Errorf("database: session %s: delete: %w", id, err)
	}
	if conn.Changes() == 0 {
		return fmt.Errorf("database: session %s: %w", id, game.ErrSessionNotFound)
	}

	return nil
}

// bindState writes the state, or SQL NULL when there is none.
//
// The distinction matters: a zero-length blob would satisfy the schema's rule
// that only a halted game may store nothing, and the next restore would fail
// on empty bytes rather than saying the game was over.
func bindState(stmt *sqlite.Stmt, param int, state []byte) {
	if state == nil {
		stmt.BindNull(param)
		return
	}
	stmt.BindBytes(param, state)
}

// readStatus reads the seven status-line columns back. Games and saves store
// the same seven under the same names.
func readStatus(stmt *sqlite.Stmt) game.StatusLine {
	return game.StatusLine{
		Available: stmt.GetBool("status_available"),
		Name:      stmt.GetText("status_name"),
		TimeGame:  stmt.GetBool("status_time_game"),
		Score:     int16(stmt.GetInt64("status_score")),
		Moves:     int16(stmt.GetInt64("status_moves")),
		Hours:     uint8(stmt.GetInt64("status_hours")),
		Minutes:   uint8(stmt.GetInt64("status_minutes")),
	}
}

// bindStatus writes the seven status-line columns, starting at param.
func bindStatus(stmt *sqlite.Stmt, param int, status game.StatusLine) {
	stmt.BindBool(param, status.Available)
	stmt.BindText(param+1, status.Name)
	stmt.BindBool(param+2, status.TimeGame)
	stmt.BindInt64(param+3, int64(status.Score))
	stmt.BindInt64(param+4, int64(status.Moves))
	stmt.BindInt64(param+5, int64(status.Hours))
	stmt.BindInt64(param+6, int64(status.Minutes))
}

func loadSession(conn *sqlite.Conn, owner, rowID int64) (game.Session, error) {
	var (
		session game.Session
		found   bool
		badKey  int
	)

	err := sqlitex.Execute(conn,
		`SELECT id, user_id, story_key, state, transcript, turn, version, halted,
		        status_available, status_name, status_time_game,
		        status_score, status_moves, status_hours, status_minutes
		   FROM games WHERE id = ? AND user_id = ?;`,
		&sqlitex.ExecOptions{
			Args: []any{rowID, owner},
			ResultFunc: func(stmt *sqlite.Stmt) error {
				found = true

				session.ID = strconv.FormatInt(stmt.GetInt64("id"), 10)
				session.UserID = strconv.FormatInt(stmt.GetInt64("user_id"), 10)
				session.Transcript = stmt.GetText("transcript")
				session.Turn = int(stmt.GetInt64("turn"))
				session.Version = stmt.GetInt64("version")
				session.Halted = stmt.GetBool("halted")

				session.Status = readStatus(stmt)

				if n := stmt.GetLen("story_key"); n != sha256.Size {
					badKey = n
					return nil
				}
				stmt.GetBytes("story_key", session.StoryKey[:])

				// A halted session stores no state, and that NULL reads back
				// as a zero-length column.
				if n := stmt.GetLen("state"); n > 0 {
					session.State = make([]byte, n)
					stmt.GetBytes("state", session.State)
				}

				return nil
			},
		})
	if err != nil {
		return game.Session{}, fmt.Errorf("database: session %d: load: %w", rowID, err)
	}
	if !found {
		return game.Session{}, fmt.Errorf("database: session %d: %w", rowID, game.ErrSessionNotFound)
	}
	if badKey != 0 {
		return game.Session{}, fmt.Errorf("database: session %d: story key is %d bytes, want %d",
			rowID, badKey, sha256.Size)
	}

	return session, nil
}

// rowID converts an identifier that came from outside.
//
// A malformed one is reported as a missing session rather than as a failure:
// nothing else in the system can tell the difference, and a browser asking for
// "../../etc/passwd" has simply asked for a session that does not exist.
func rowID(id string) (int64, error) {
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("database: session %q: %w", id, game.ErrSessionNotFound)
	}
	return n, nil
}
