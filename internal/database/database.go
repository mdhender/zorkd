// Package database stores application data in SQLite.
//
// Access is explicit and small: statements are written out, and the schema is
// built by versioned migrations rather than inferred from Go types.
package database

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"

	"github.com/mdhender/zorkd/migrations"
)

// busyTimeout bounds how long a connection waits for a writer to finish before
// reporting SQLITE_BUSY. WAL allows one writer at a time, and a turn's write is
// short, so waiting briefly is better than failing a player's command.
const busyTimeout = 5 * time.Second

// A DB is an open SQLite database with its schema up to date.
//
// It is safe for concurrent use: connections come from a pool, and each caller
// holds one for the length of a single statement or transaction.
type DB struct {
	pool   *sqlitex.Pool
	closed sync.Once
}

// Errors reported by [Open] for a path it will not open.
//
// They are distinct because SQLite is not: a missing parent directory, a parent
// that is a file, and a path that is a directory all reach the caller as one
// "unable to open database file", which says nothing about what to fix.
var (
	// ErrParentMissing reports that the directory that would hold the database
	// does not exist. Open does not create it; see [Open].
	ErrParentMissing = errors.New("parent directory does not exist")

	// ErrParentNotDirectory reports that the path that would hold the database
	// exists but is not a directory.
	ErrParentNotDirectory = errors.New("parent path is not a directory")

	// ErrNotRegularFile reports that the database path itself exists and is
	// something other than a regular file.
	ErrNotRegularFile = errors.New("path is not a regular file")
)

// Open opens the database at path and applies any migrations it has not seen.
// When create is true, Open creates a new file and refuses to replace or open
// one that already exists. When create is false, the file must already exist.
//
// The path is a file. An in-memory database would give each pooled connection
// its own empty copy, which is not a database at all.
//
// The path is resolved with [filepath.Abs] and its directory must already
// exist. In creation mode Open creates a database file and nothing else: a
// missing directory is reported rather than built. The path to the database is
// usually relative and comes from a flag, so a server started in the wrong
// directory would otherwise quietly construct a tree nobody asked for and
// serve an empty store out of it, which is the failure this check exists to
// prevent. Do not add an os.MkdirAll here, however tidy it would make a
// deployment script.
func Open(ctx context.Context, path string, create bool) (*DB, error) {
	abs, err := checkPath(path, create)
	if err != nil {
		return nil, err
	}
	if create {
		file, err := os.OpenFile(abs, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return nil, fmt.Errorf("database: create %s: %w", abs, err)
		}
		if err := file.Close(); err != nil {
			return nil, fmt.Errorf("database: create %s: close: %w", abs, err)
		}
	}

	pool, err := sqlitex.NewPool(abs, sqlitex.PoolOptions{
		Flags: sqlite.OpenReadWrite | sqlite.OpenWAL,
		PrepareConn: func(conn *sqlite.Conn) error {
			conn.SetBusyTimeout(busyTimeout)
			return sqlitex.ExecuteTransient(conn, "PRAGMA foreign_keys = ON;", nil)
		},
	})
	if err != nil {
		return nil, fmt.Errorf("database: open %s: %w", abs, err)
	}

	db := &DB{pool: pool}
	if err := db.migrate(ctx); err != nil {
		pool.Close()
		return nil, err
	}

	return db, nil
}

// checkPath resolves path and reports, distinctly, every reason the database
// there cannot be opened.
//
// It creates nothing. In particular it must never create the parent directory:
// see [Open] for why that is deliberate.
//
// The absolute path is in every message because the path given is usually
// relative, and a relative path in an error names a file only to whoever knows
// the working directory the server was started in.
func checkPath(path string, create bool) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("database: %s: resolve path: %w", path, err)
	}

	dir := filepath.Dir(abs)
	switch info, err := os.Stat(dir); {
	case errors.Is(err, fs.ErrNotExist):
		return "", fmt.Errorf("database: %s: %s: %w", abs, dir, ErrParentMissing)
	case err != nil:
		return "", fmt.Errorf("database: %s: parent %s: %w", abs, dir, err)
	case !info.IsDir():
		return "", fmt.Errorf("database: %s: %s: %w", abs, dir, ErrParentNotDirectory)
	}

	switch info, err := os.Stat(abs); {
	case errors.Is(err, fs.ErrNotExist):
		if !create {
			return "", fmt.Errorf("database: %s: %w", abs, fs.ErrNotExist)
		}
	case err != nil:
		return "", fmt.Errorf("database: %s: %w", abs, err)
	case info.IsDir():
		return "", fmt.Errorf("database: %s: %w: it is a directory", abs, ErrNotRegularFile)
	case !info.Mode().IsRegular():
		return "", fmt.Errorf("database: %s: %w: mode is %s", abs, ErrNotRegularFile, info.Mode())
	case create:
		return "", fmt.Errorf("database: %s: %w", abs, fs.ErrExist)
	}

	return abs, nil
}

// Close releases every connection in the pool.
//
// It is safe to call more than once, so a server that closes on shutdown and a
// caller that closes on its own way out do not have to agree about which of
// them owns the database.
func (db *DB) Close() error {
	var err error
	db.closed.Do(func() { err = db.pool.Close() })

	if err != nil {
		return fmt.Errorf("database: close: %w", err)
	}
	return nil
}

// Ping verifies that the pool can provide a connection and execute a trivial
// query. The supplied context bounds both operations.
func (db *DB) Ping(ctx context.Context) error {
	conn, release, err := db.conn(ctx)
	if err != nil {
		return fmt.Errorf("database: ping: %w", err)
	}
	defer release()
	if err := sqlitex.ExecuteTransient(conn, "SELECT 1;", nil); err != nil {
		return fmt.Errorf("database: ping: %w", err)
	}
	return nil
}

// Sessions returns the store of games in progress.
func (db *DB) Sessions() *Sessions { return &Sessions{db: db} }

// Users returns the store of accounts.
func (db *DB) Users() *Users { return &Users{db: db} }

// AuthSessions returns the store of browser sessions.
func (db *DB) AuthSessions() *AuthSessions { return &AuthSessions{db: db} }

// Invitations returns the store of invitations to register.
func (db *DB) Invitations() *Invitations { return &Invitations{db: db} }

// conn takes a connection from the pool and returns it along with the function
// that puts it back.
func (db *DB) conn(ctx context.Context) (*sqlite.Conn, func(), error) {
	conn, err := db.pool.Take(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("database: connection: %w", err)
	}
	return conn, func() { db.pool.Put(conn) }, nil
}

// migrate applies every migration the database has not already seen.
//
// The applied version is SQLite's own user_version, so the schema carries its
// version in the file rather than in a table this application has to keep
// honest.
func (db *DB) migrate(ctx context.Context) error {
	all, err := migrations.All()
	if err != nil {
		return err
	}

	conn, release, err := db.conn(ctx)
	if err != nil {
		return err
	}
	defer release()

	current, err := userVersion(conn)
	if err != nil {
		return err
	}

	for _, migration := range all {
		if migration.Version <= current {
			continue
		}

		// ExecuteScript wraps the file in a savepoint, so a migration that
		// fails part way leaves the schema as it was.
		if err := sqlitex.ExecuteScript(conn, migration.SQL, nil); err != nil {
			return fmt.Errorf("database: migration %s: %w", migration.Name, err)
		}

		// user_version takes no parameter, so it is formatted in. The value
		// came from a file name this binary embedded.
		set := fmt.Sprintf("PRAGMA user_version = %d;", migration.Version)
		if err := sqlitex.ExecuteTransient(conn, set, nil); err != nil {
			return fmt.Errorf("database: migration %s: record version: %w", migration.Name, err)
		}
	}

	return nil
}

func userVersion(conn *sqlite.Conn) (int, error) {
	var version int

	err := sqlitex.ExecuteTransient(conn, "PRAGMA user_version;", &sqlitex.ExecOptions{
		ResultFunc: func(stmt *sqlite.Stmt) error {
			version = int(stmt.ColumnInt64(0))
			return nil
		},
	})
	if err != nil {
		return 0, fmt.Errorf("database: read schema version: %w", err)
	}

	return version, nil
}

// SchemaVersion reports the migration the database has been brought up to.
func (db *DB) SchemaVersion(ctx context.Context) (int, error) {
	conn, release, err := db.conn(ctx)
	if err != nil {
		return 0, err
	}
	defer release()

	return userVersion(conn)
}

// timeFormat is RFC 3339 with a fixed nine-digit fraction.
//
// The width matters: [time.RFC3339Nano] trims trailing zeros, which sorts a
// whole second before a fraction of one, and session expiry is compared as a
// string in SQL.
const timeFormat = "2006-01-02T15:04:05.000000000Z07:00"

// stamp formats a timestamp for a TEXT column. UTC sorts the way it reads.
func stamp(t time.Time) string { return t.UTC().Format(timeFormat) }

// now is the current time, formatted for a TEXT column.
func now() string { return stamp(time.Now()) }
