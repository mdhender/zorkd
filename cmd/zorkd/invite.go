package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"

	"github.com/mdhender/zorkd/internal/auth"
	"github.com/mdhender/zorkd/internal/database"
	"github.com/mdhender/zorkd/internal/invite"
)

// runInvite issues one invitation and prints the link that redeems it.
//
// There is no admin surface and this should not grow one: a subcommand that
// writes a row and prints a token fits a deployment that is a binary and its
// database. The printed line is the only place the plaintext token ever exists
// — the database holds only its SHA-256 — so a lost invitation is reissued
// rather than looked up.
//
// It opens an initialized database itself rather than talking to a running
// server, so it needs no authenticated route to protect.
func runInvite(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("zorkd invite", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var (
		dbPath  = fs.String("database", env("ZORK_DATABASE", "zorkd.db"), "path to the SQLite database")
		baseURL = fs.String("base-url", env("ZORK_BASE_URL", "http://localhost:8080"), "where this server is reached, for the printed link")
	)

	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: zorkd invite [flags] address\n\nIssues an invitation to register and prints the link once.\n\nFlags:\n")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return fmt.Errorf("invite: one email address is required")
	}

	base, err := url.Parse(*baseURL)
	if err != nil || base.Host == "" {
		return fmt.Errorf("invite: -base-url %q: not a URL with a host", *baseURL)
	}

	// The address is normalized before the database is opened, for the reason
	// the cookie policy is checked before it in run: a typo is caught while
	// somebody is still looking at it, and an invitation that was never going
	// to be issued does not leave a database file behind on its way out.
	address, err := auth.NormalizeEmail(fs.Arg(0))
	if err != nil {
		return fmt.Errorf("invite: %w", err)
	}

	ctx := context.Background()

	// The absolute path is in the error for the same reason the server logs it:
	// -database defaults to a relative one, and may otherwise name the wrong
	// file when this command is run from another directory.
	dbFile, err := filepath.Abs(*dbPath)
	if err != nil {
		return fmt.Errorf("database %s: %w", *dbPath, err)
	}

	db, err := database.Open(ctx, dbFile, false)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return missingDatabaseError(dbFile)
		}
		return err
	}
	defer func() { _ = db.Close() }()

	invitations, err := invite.NewService(db.Invitations())
	if err != nil {
		return err
	}

	token, err := invitations.Create(ctx, address)
	if err != nil {
		return err
	}

	link := *base
	link.Path = "/register"
	link.RawQuery = url.Values{"invite": {token}}.Encode()

	fmt.Fprintln(stdout, link.String())
	return nil
}
