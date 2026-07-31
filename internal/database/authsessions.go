package database

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"

	"github.com/mdhender/zorkd/internal/session"
)

// AuthSessions stores browser sessions. It implements [session.Store].
//
// Rows are keyed by the SHA-256 of the session token. The token itself is never
// written here, so a copy of this database cannot be used to log in as anybody.
type AuthSessions struct {
	db *DB
}

var _ session.Store = (*AuthSessions)(nil)

// CreateSession stores a session under the token's hash.
func (a *AuthSessions) CreateSession(ctx context.Context, tokenHash []byte, s session.Session) error {
	userID, err := userRowID(s.UserID)
	if err != nil {
		return fmt.Errorf("database: create session: %w", err)
	}

	conn, release, err := a.db.conn(ctx)
	if err != nil {
		return err
	}
	defer release()

	err = sqlitex.Execute(conn,
		`INSERT INTO auth_sessions (token_hash, user_id, created_at, expires_at) VALUES (?, ?, ?, ?);`,
		&sqlitex.ExecOptions{Args: []any{tokenHash, userID, now(), stamp(s.ExpiresAt)}})
	if err != nil {
		return fmt.Errorf("database: create session: %w", err)
	}

	return nil
}

// SessionByToken returns the session stored under the token's hash.
func (a *AuthSessions) SessionByToken(ctx context.Context, tokenHash []byte) (session.Session, error) {
	conn, release, err := a.db.conn(ctx)
	if err != nil {
		return session.Session{}, err
	}
	defer release()

	var (
		stored session.Session
		found  bool
		badAt  string
	)

	err = sqlitex.Execute(conn,
		`SELECT user_id, expires_at FROM auth_sessions WHERE token_hash = ?;`,
		&sqlitex.ExecOptions{
			Args: []any{tokenHash},
			ResultFunc: func(stmt *sqlite.Stmt) error {
				found = true
				stored.UserID = strconv.FormatInt(stmt.GetInt64("user_id"), 10)

				text := stmt.GetText("expires_at")
				expires, err := time.Parse(time.RFC3339, text)
				if err != nil {
					badAt = text
					return nil
				}
				stored.ExpiresAt = expires
				return nil
			},
		})
	if err != nil {
		return session.Session{}, fmt.Errorf("database: session lookup: %w", err)
	}
	if !found {
		return session.Session{}, session.ErrNoSession
	}
	if badAt != "" {
		return session.Session{}, fmt.Errorf("database: session lookup: unreadable expiry %q", badAt)
	}

	return stored, nil
}

// DeleteSession removes the session. Deleting one that is not there is not an
// error: logging out twice is not a failure.
func (a *AuthSessions) DeleteSession(ctx context.Context, tokenHash []byte) error {
	conn, release, err := a.db.conn(ctx)
	if err != nil {
		return err
	}
	defer release()

	err = sqlitex.Execute(conn,
		`DELETE FROM auth_sessions WHERE token_hash = ?;`,
		&sqlitex.ExecOptions{Args: []any{tokenHash}})
	if err != nil {
		return fmt.Errorf("database: delete session: %w", err)
	}

	return nil
}

// DeleteExpiredSessions removes every session that expired before the given
// time.
func (a *AuthSessions) DeleteExpiredSessions(ctx context.Context, before time.Time) (int, error) {
	conn, release, err := a.db.conn(ctx)
	if err != nil {
		return 0, err
	}
	defer release()

	err = sqlitex.Execute(conn,
		`DELETE FROM auth_sessions WHERE expires_at < ?;`,
		&sqlitex.ExecOptions{Args: []any{stamp(before)}})
	if err != nil {
		return 0, fmt.Errorf("database: delete expired sessions: %w", err)
	}

	return conn.Changes(), nil
}
