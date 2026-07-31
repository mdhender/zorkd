package database

import (
	"context"
	"fmt"
	"strconv"

	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"

	"github.com/mdhender/zorkd/internal/auth"
)

// Users stores accounts. It implements [auth.Store].
//
// The password hash is stored and returned as the opaque string the auth
// package produced. Nothing here parses it or decides anything from it.
type Users struct {
	db *DB
}

var _ auth.Store = (*Users)(nil)

// CreateUser stores a new account.
//
// A duplicate address is refused by the UNIQUE index rather than by a check
// this code makes first, which is what keeps two simultaneous registrations for
// the same address from both succeeding.
func (u *Users) CreateUser(ctx context.Context, record auth.Record) (auth.User, error) {
	conn, release, err := u.db.conn(ctx)
	if err != nil {
		return auth.User{}, err
	}
	defer release()

	stamp := now()
	err = sqlitex.Execute(conn,
		`INSERT INTO users (email, password_hash, created_at, updated_at) VALUES (?, ?, ?, ?);`,
		&sqlitex.ExecOptions{Args: []any{record.Email, record.PasswordHash, stamp, stamp}})
	if err != nil {
		if sqlite.ErrCode(err) == sqlite.ResultConstraintUnique {
			return auth.User{}, fmt.Errorf("database: create user: %w", auth.ErrEmailTaken)
		}
		return auth.User{}, fmt.Errorf("database: create user: %w", err)
	}

	return auth.User{
		ID:    strconv.FormatInt(conn.LastInsertRowID(), 10),
		Email: record.Email,
	}, nil
}

// UserByEmail returns the account registered under the normalized address.
func (u *Users) UserByEmail(ctx context.Context, email string) (auth.Record, error) {
	conn, release, err := u.db.conn(ctx)
	if err != nil {
		return auth.Record{}, err
	}
	defer release()

	var (
		record auth.Record
		found  bool
	)

	err = sqlitex.Execute(conn,
		`SELECT id, email, password_hash FROM users WHERE email = ?;`,
		&sqlitex.ExecOptions{
			Args: []any{email},
			ResultFunc: func(stmt *sqlite.Stmt) error {
				found = true
				record.ID = strconv.FormatInt(stmt.GetInt64("id"), 10)
				record.Email = stmt.GetText("email")
				record.PasswordHash = stmt.GetText("password_hash")
				return nil
			},
		})
	if err != nil {
		return auth.Record{}, fmt.Errorf("database: user %q: load: %w", email, err)
	}
	if !found {
		return auth.Record{}, fmt.Errorf("database: user %q: %w", email, auth.ErrUserNotFound)
	}

	return record, nil
}

// UserByID returns the account under id.
func (u *Users) UserByID(ctx context.Context, id string) (auth.User, error) {
	rowID, err := userRowID(id)
	if err != nil {
		return auth.User{}, err
	}

	conn, release, err := u.db.conn(ctx)
	if err != nil {
		return auth.User{}, err
	}
	defer release()

	var (
		user  auth.User
		found bool
	)

	err = sqlitex.Execute(conn,
		`SELECT id, email FROM users WHERE id = ?;`,
		&sqlitex.ExecOptions{
			Args: []any{rowID},
			ResultFunc: func(stmt *sqlite.Stmt) error {
				found = true
				user.ID = strconv.FormatInt(stmt.GetInt64("id"), 10)
				user.Email = stmt.GetText("email")
				return nil
			},
		})
	if err != nil {
		return auth.User{}, fmt.Errorf("database: user %s: load: %w", id, err)
	}
	if !found {
		return auth.User{}, fmt.Errorf("database: user %s: %w", id, auth.ErrUserNotFound)
	}

	return user, nil
}

// userRowID converts an identifier that came from outside. A malformed one is
// reported as a missing account rather than as a failure.
func userRowID(id string) (int64, error) {
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("database: user %q: %w", id, auth.ErrUserNotFound)
	}
	return n, nil
}
