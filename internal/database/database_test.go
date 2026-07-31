package database

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"

	"github.com/mdhender/zorkd/migrations"
)

func testPath(t *testing.T) string {
	t.Helper()

	return filepath.Join(t.TempDir(), "zorkd.db")
}

func openAt(t *testing.T, path string, create bool) *DB {
	t.Helper()

	db, err := Open(context.Background(), path, create)
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

	return openAt(t, testPath(t), true)
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

func TestPing(t *testing.T) {
	if err := testDB(t).Ping(context.Background()); err != nil {
		t.Fatalf("Ping() error = %v", err)
	}
}

// Reopening an existing database applies nothing and loses nothing. This is
// what every server restart does.
func TestOpenIsRepeatable(t *testing.T) {
	path := testPath(t)

	first := openAt(t, path, true)
	owner := testUser(t, first, "player@example.com")

	created, err := first.Sessions().Create(context.Background(), storedSession(owner))
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

	second := openAt(t, path, false)

	after, err := second.SchemaVersion(context.Background())
	if err != nil {
		t.Fatalf("SchemaVersion() error = %v", err)
	}
	if after != before {
		t.Errorf("reopening moved the schema from %d to %d", before, after)
	}

	loaded, err := second.Sessions().Load(context.Background(), owner, created.ID)
	if err != nil {
		t.Fatalf("Load() after reopening error = %v", err)
	}
	if string(loaded.State) != string(created.State) {
		t.Error("the stored state did not survive reopening")
	}
}

// A good path creates the database file, and nothing but the database file.
func TestOpenCreatesTheDatabaseFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "zorkd.db")

	db := openAt(t, path, true)

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%s) error = %v", path, err)
	}
	if !info.Mode().IsRegular() {
		t.Errorf("mode = %s, want a regular file", info.Mode())
	}

	all, err := migrations.All()
	if err != nil {
		t.Fatalf("migrations.All() error = %v", err)
	}
	version, err := db.SchemaVersion(context.Background())
	if err != nil {
		t.Fatalf("SchemaVersion() error = %v", err)
	}
	if want := all[len(all)-1].Version; version != want {
		t.Errorf("schema version = %d, want %d", version, want)
	}
}

func TestOpenModesRefuseTheWrongFilesystemState(t *testing.T) {
	t.Run("open requires an existing file", func(t *testing.T) {
		path := testPath(t)
		db, err := Open(context.Background(), path, false)
		if err == nil {
			_ = db.Close()
			t.Fatal("Open(create=false) succeeded for a missing file")
		}
		if !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("error = %v, want fs.ErrNotExist", err)
		}
		if _, statErr := os.Stat(path); !errors.Is(statErr, fs.ErrNotExist) {
			t.Errorf("refusal created %s", path)
		}
	})

	t.Run("create refuses an existing file", func(t *testing.T) {
		path := testPath(t)
		want := []byte("unchanged")
		if err := os.WriteFile(path, want, 0o600); err != nil {
			t.Fatal(err)
		}
		db, err := Open(context.Background(), path, true)
		if err == nil {
			_ = db.Close()
			t.Fatal("Open(create=true) succeeded for an existing file")
		}
		if !errors.Is(err, fs.ErrExist) {
			t.Errorf("error = %v, want fs.ErrExist", err)
		}
		got, readErr := os.ReadFile(path)
		if readErr != nil || string(got) != string(want) {
			t.Errorf("existing contents = %q, %v; want %q", got, readErr, want)
		}
	})
}

// Open must report a path it cannot use and leave the filesystem alone. The
// second half is the one that matters: an os.MkdirAll added later for
// convenience would leave the error assertions passing.
func TestOpenRejectsABadPathAndCreatesNothing(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, root string) string
		want  error
	}{
		{
			name: "parent directory missing",
			setup: func(t *testing.T, root string) string {
				return filepath.Join(root, "missing", "zorkd.db")
			},
			want: ErrParentMissing,
		},
		{
			name: "two parent directories missing",
			setup: func(t *testing.T, root string) string {
				return filepath.Join(root, "missing", "deeper", "zorkd.db")
			},
			want: ErrParentMissing,
		},
		{
			name: "parent is a regular file",
			setup: func(t *testing.T, root string) string {
				t.Helper()

				parent := filepath.Join(root, "not-a-directory")
				if err := os.WriteFile(parent, []byte("this is a file\n"), 0o600); err != nil {
					t.Fatalf("WriteFile() error = %v", err)
				}
				return filepath.Join(parent, "zorkd.db")
			},
			want: ErrParentNotDirectory,
		},
		{
			name: "path is an existing directory",
			setup: func(t *testing.T, root string) string {
				t.Helper()

				path := filepath.Join(root, "zorkd.db")
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatalf("Mkdir() error = %v", err)
				}
				return path
			},
			want: ErrNotRegularFile,
		},
	}

	// Collected across the subtests, which run in order, so the four cases can
	// be compared with each other rather than only with a sentinel.
	messages := make(map[string]string, len(tests))

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			path := tt.setup(t, root)
			before := treeOf(t, root)

			db, err := Open(context.Background(), path, true)
			if err == nil {
				t.Errorf("Open(%s) = nil error, want failure", path)
				if err := db.Close(); err != nil {
					t.Errorf("Close() error = %v", err)
				}
			} else {
				if !errors.Is(err, tt.want) {
					t.Errorf("Open(%s) error = %v, want %v", path, err, tt.want)
				}
				if abs, _ := filepath.Abs(path); !strings.Contains(err.Error(), abs) {
					t.Errorf("Open(%s) error = %q, want it to name %s", path, err, abs)
				}
				messages[tt.name] = err.Error()
			}

			if after := treeOf(t, root); !slices.Equal(before, after) {
				t.Errorf("Open(%s) changed the filesystem:\nbefore %v\nafter  %v", path, before, after)
			}
		})
	}

	// The four messages must stay tellable apart, or they collapse back into
	// the single "unable to open database file" this replaced.
	seen := make(map[string]string, len(messages))
	for name, message := range messages {
		if other, ok := seen[message]; ok {
			t.Errorf("%q and %q report the same error %q", name, other, message)
		}
		seen[message] = name
	}
	if len(messages) != len(tests) {
		t.Errorf("collected %d messages, want %d", len(messages), len(tests))
	}
}

// treeOf lists every path under root, so a test can assert that a failed Open
// left the directory exactly as it found it.
func treeOf(t *testing.T, root string) []string {
	t.Helper()

	var paths []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		paths = append(paths, rel+":"+d.Type().String())
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir(%s) error = %v", root, err)
	}

	slices.Sort(paths)
	return paths
}
