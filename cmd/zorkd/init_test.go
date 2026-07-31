package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mdhender/zorkd/internal/database"
	"github.com/mdhender/zorkd/migrations"
)

func TestRunInitCreatesAndMigratesDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "zorkd.db")
	var stdout bytes.Buffer
	if err := runInit([]string{"-database", path}, &stdout, io.Discard); err != nil {
		t.Fatalf("runInit() error = %v", err)
	}
	abs, _ := filepath.Abs(path)
	if got := strings.TrimSpace(stdout.String()); got != abs {
		t.Errorf("runInit() printed %q, want %q", got, abs)
	}

	db, err := database.Open(context.Background(), path, false)
	if err != nil {
		t.Fatalf("open initialized database: %v", err)
	}
	defer func() { _ = db.Close() }()
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

func TestRunInitRefusesExistingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "zorkd.db")
	want := []byte("do not replace me")
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runInit([]string{"-database", path}, io.Discard, io.Discard); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("runInit() error = %v, want fs.ErrExist", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("existing database changed to %q", got)
	}
}

func TestCommandsRefuseMissingDatabase(t *testing.T) {
	for _, tt := range []struct {
		name string
		run  func(path string) error
	}{
		{name: "serve", run: func(path string) error {
			return serve([]string{"-database", path}, io.Discard)
		}},
		{name: "invite", run: func(path string) error {
			return runInvite([]string{"-database", path, "player@example.com"}, io.Discard, io.Discard)
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "zorkd.db")
			err := tt.run(path)
			abs, _ := filepath.Abs(path)
			want := "no database here. Run \"zorkd init -database " + abs + "\" to make one."
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("error = %v, want remedy %q", err, want)
			}
			entries, readErr := os.ReadDir(root)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if len(entries) != 0 {
				t.Errorf("refusal created %v", entries)
			}
		})
	}
}
