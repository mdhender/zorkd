package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"path/filepath"

	"github.com/mdhender/zorkd/internal/database"
)

// runInit creates and migrates a database, refusing to touch an existing one.
func runInit(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("zorkd init", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("database", env("ZORK_DATABASE", "zorkd.db"), "path to the SQLite database")
	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: zorkd init [flags]\n\nCreates and migrates a new database.\n\nFlags:\n")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		fs.Usage()
		return fmt.Errorf("init: unexpected argument %q", fs.Arg(0))
	}

	abs, err := filepath.Abs(*dbPath)
	if err != nil {
		return fmt.Errorf("database %s: %w", *dbPath, err)
	}
	db, err := database.Open(context.Background(), abs, true)
	if err != nil {
		return err
	}
	if err := db.Close(); err != nil {
		return err
	}

	fmt.Fprintln(stdout, abs)
	return nil
}

func missingDatabaseError(path string) error {
	return fmt.Errorf("database: %s: no database here. Run \"zorkd init -database %s\" to make one.", path, path)
}
