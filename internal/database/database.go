// Package database stores application data in SQLite.
//
// Access is explicit and small: statements are written out, and the schema is
// built by versioned migrations rather than inferred from Go types.
package database

import (
	"context"
	"fmt"
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

// Open opens the database at path, creating it if necessary, and applies any
// migrations it has not seen.
//
// The path is a file. An in-memory database would give each pooled connection
// its own empty copy, which is not a database at all.
func Open(ctx context.Context, path string) (*DB, error) {
	pool, err := sqlitex.NewPool(path, sqlitex.PoolOptions{
		// The default flags open read-write, create, and WAL.
		PrepareConn: func(conn *sqlite.Conn) error {
			conn.SetBusyTimeout(busyTimeout)
			return sqlitex.ExecuteTransient(conn, "PRAGMA foreign_keys = ON;", nil)
		},
	})
	if err != nil {
		return nil, fmt.Errorf("database: open %s: %w", path, err)
	}

	db := &DB{pool: pool}
	if err := db.migrate(ctx); err != nil {
		pool.Close()
		return nil, err
	}

	return db, nil
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

// Sessions returns the store of games in progress.
func (db *DB) Sessions() *Sessions { return &Sessions{db: db} }

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

// now formats a timestamp for a TEXT column. UTC and RFC 3339 sort the way they
// read.
func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }
