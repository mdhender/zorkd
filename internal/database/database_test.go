package database

import (
	"context"
	"path/filepath"
	"testing"

	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"

	"github.com/mdhender/zorkd/migrations"
)

func testPath(t *testing.T) string {
	t.Helper()

	return filepath.Join(t.TempDir(), "zorkd.db")
}

func openAt(t *testing.T, path string) *DB {
	t.Helper()

	db, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})

	return db
}

func testDB(t *testing.T) *DB {
	t.Helper()

	return openAt(t, testPath(t))
}

// pragma reads a one-value PRAGMA, which is how the settings that are not
// visible in the schema are checked.
func pragma(t *testing.T, db *DB, query string) string {
	t.Helper()

	conn, release, err := db.conn(context.Background())
	if err != nil {
		t.Fatalf("conn() error = %v", err)
	}
	defer release()

	var value string
	err = sqlitex.ExecuteTransient(conn, query, &sqlitex.ExecOptions{
		ResultFunc: func(stmt *sqlite.Stmt) error {
			value = stmt.ColumnText(0)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("%s error = %v", query, err)
	}

	return value
}

func TestOpenPreparesTheDatabase(t *testing.T) {
	db := testDB(t)

	if got := pragma(t, db, "PRAGMA journal_mode;"); got != "wal" {
		t.Errorf("journal_mode = %q, want %q", got, "wal")
	}
	if got := pragma(t, db, "PRAGMA foreign_keys;"); got != "1" {
		t.Errorf("foreign_keys = %q, want %q", got, "1")
	}

	all, err := migrations.All()
	if err != nil {
		t.Fatalf("migrations.All() error = %v", err)
	}
	latest := all[len(all)-1].Version

	version, err := db.SchemaVersion(context.Background())
	if err != nil {
		t.Fatalf("SchemaVersion() error = %v", err)
	}
	if version != latest {
		t.Errorf("schema version = %d, want %d", version, latest)
	}
}

// Reopening an existing database applies nothing and loses nothing. This is
// what every server restart does.
func TestOpenIsRepeatable(t *testing.T) {
	path := testPath(t)

	first := openAt(t, path)
	created, err := first.Sessions().Create(context.Background(), storedSession())
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	before, err := first.SchemaVersion(context.Background())
	if err != nil {
		t.Fatalf("SchemaVersion() error = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	second := openAt(t, path)

	after, err := second.SchemaVersion(context.Background())
	if err != nil {
		t.Fatalf("SchemaVersion() error = %v", err)
	}
	if after != before {
		t.Errorf("reopening moved the schema from %d to %d", before, after)
	}

	loaded, err := second.Sessions().Load(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("Load() after reopening error = %v", err)
	}
	if string(loaded.State) != string(created.State) {
		t.Error("the stored state did not survive reopening")
	}
}

func TestOpenReportsABadPath(t *testing.T) {
	if _, err := Open(context.Background(), filepath.Join(t.TempDir(), "no-such-dir", "zorkd.db")); err == nil {
		t.Fatal("Open() = nil error, want failure")
	}
}
