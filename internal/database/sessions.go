package database

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strconv"

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
	conn, release, err := s.db.conn(ctx)
	if err != nil {
		return game.Session{}, err
	}
	defer release()

	stmt, err := conn.Prepare(
		`INSERT INTO games (story_key, state, turn, version, halted, created_at, updated_at)
		 VALUES (?, ?, ?, 1, ?, ?, ?);`)
	if err != nil {
		return game.Session{}, fmt.Errorf("database: create session: %w", err)
	}
	defer stmt.Reset()

	stamp := now()
	stmt.BindBytes(1, session.StoryKey[:])
	bindState(stmt, 2, session.State)
	stmt.BindInt64(3, int64(session.Turn))
	stmt.BindBool(4, session.Halted)
	stmt.BindText(5, stamp)
	stmt.BindText(6, stamp)

	if _, err := stmt.Step(); err != nil {
		return game.Session{}, fmt.Errorf("database: create session: %w", err)
	}

	session.ID = strconv.FormatInt(conn.LastInsertRowID(), 10)
	session.Version = 1

	return session, nil
}

// Load returns the stored session.
func (s *Sessions) Load(ctx context.Context, id string) (game.Session, error) {
	rowID, err := rowID(id)
	if err != nil {
		return game.Session{}, err
	}

	conn, release, err := s.db.conn(ctx)
	if err != nil {
		return game.Session{}, err
	}
	defer release()

	return loadSession(conn, rowID)
}

// Update writes the session only if the stored version is still the one that
// was read.
//
// The condition is the whole point: two turns that started from the same state
// would otherwise both write, and the player of the losing one would watch a
// command vanish. The loser is refused instead, with ErrVersionConflict.
func (s *Sessions) Update(ctx context.Context, session game.Session) (game.Session, error) {
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
		    SET state = ?, turn = ?, halted = ?, version = version + 1, updated_at = ?
		  WHERE id = ? AND version = ?;`)
	if err != nil {
		return game.Session{}, fmt.Errorf("database: session %s: update: %w", session.ID, err)
	}
	defer stmt.Reset()

	bindState(stmt, 1, session.State)
	stmt.BindInt64(2, int64(session.Turn))
	stmt.BindBool(3, session.Halted)
	stmt.BindText(4, now())
	stmt.BindInt64(5, rowID)
	stmt.BindInt64(6, session.Version)

	if _, err := stmt.Step(); err != nil {
		return game.Session{}, fmt.Errorf("database: session %s: update: %w", session.ID, err)
	}

	if conn.Changes() == 0 {
		// Nothing matched: either the row is gone, or its version moved on
		// while this turn was being played.
		if _, err := loadSession(conn, rowID); err != nil {
			return game.Session{}, err
		}
		return game.Session{}, fmt.Errorf("database: session %s: %w", session.ID, game.ErrVersionConflict)
	}

	session.Version++
	return session, nil
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

func loadSession(conn *sqlite.Conn, rowID int64) (game.Session, error) {
	var (
		session game.Session
		found   bool
		badKey  int
	)

	err := sqlitex.Execute(conn,
		`SELECT id, story_key, state, turn, version, halted FROM games WHERE id = ?;`,
		&sqlitex.ExecOptions{
			Args: []any{rowID},
			ResultFunc: func(stmt *sqlite.Stmt) error {
				found = true

				session.ID = strconv.FormatInt(stmt.GetInt64("id"), 10)
				session.Turn = int(stmt.GetInt64("turn"))
				session.Version = stmt.GetInt64("version")
				session.Halted = stmt.GetBool("halted")

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
