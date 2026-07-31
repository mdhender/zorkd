package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mdhender/zorkd/internal/database"
	"github.com/mdhender/zorkd/internal/invite"
)

func testDBPath(t *testing.T) string {
	t.Helper()

	return filepath.Join(t.TempDir(), "zorkd.db")
}

// redeemable reports the address the printed link invites, by opening the
// database the subcommand wrote to.
func redeemable(t *testing.T, dbPath, link string) string {
	t.Helper()

	parsed, err := url.Parse(strings.TrimSpace(link))
	if err != nil {
		t.Fatalf("the printed line is not a URL: %v", err)
	}
	token := parsed.Query().Get("invite")
	if token == "" {
		t.Fatalf("the printed link carries no invitation: %q", link)
	}

	ctx := context.Background()
	db, err := database.Open(ctx, dbPath, false)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer func() { _ = db.Close() }()

	invitations, err := invite.NewService(db.Invitations())
	if err != nil {
		t.Fatalf("invite.NewService() error = %v", err)
	}

	invitation, err := invitations.Invited(ctx, token)
	if err != nil {
		t.Fatalf("the printed invitation is not redeemable: %v", err)
	}
	return invitation.Email
}

func TestRunInvitePrintsALinkOnce(t *testing.T) {
	dbPath := testDBPath(t)
	if err := runInit([]string{"-database", dbPath}, io.Discard, io.Discard); err != nil {
		t.Fatalf("runInit() error = %v", err)
	}

	var stdout bytes.Buffer
	args := []string{"-database", dbPath, "-base-url", "https://example.com", "  Player@Example.COM "}

	if err := runInvite(args, &stdout, io.Discard); err != nil {
		t.Fatalf("runInvite() error = %v", err)
	}

	link := strings.TrimSpace(stdout.String())
	if !strings.HasPrefix(link, "https://example.com/register?invite=") {
		t.Fatalf("printed %q, want a registration link", link)
	}
	if lines := strings.Count(strings.TrimSpace(stdout.String()), "\n"); lines != 0 {
		t.Errorf("printed %d lines, want one", lines+1)
	}

	// The address is normalized when the invitation is issued, so it matches
	// what registration will normalize the typed one to.
	if got := redeemable(t, dbPath, link); got != "player@example.com" {
		t.Errorf("the invitation is for %q, want %q", got, "player@example.com")
	}
}

// A typo is caught while somebody is still looking at it, rather than when the
// invited person cannot register.
func TestRunInviteRefusesABadAddress(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "not an address", args: []string{"player"}},
		{name: "a display form", args: []string{"Zork <zork@example.com>"}},
		{name: "no address", args: nil},
		{name: "two addresses", args: []string{"a@example.com", "b@example.com"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout bytes.Buffer
			args := append([]string{"-database", testDBPath(t)}, tt.args...)

			dbPath := args[1]

			if err := runInvite(args, &stdout, io.Discard); err == nil {
				t.Fatal("runInvite() = nil, want a refusal")
			}
			if stdout.Len() != 0 {
				t.Errorf("a refused invitation still printed %q", stdout.String())
			}
			// And it did not leave a database behind on its way out.
			if _, err := os.Stat(dbPath); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("a refused invitation created %s", dbPath)
			}
		})
	}
}

func TestRunInviteRefusesABadBaseURL(t *testing.T) {
	for _, base := range []string{"", "not a url", "/register"} {
		t.Run(base, func(t *testing.T) {
			var stdout bytes.Buffer
			args := []string{"-database", testDBPath(t), "-base-url", base, "player@example.com"}

			if err := runInvite(args, &stdout, io.Discard); err == nil {
				t.Fatal("runInvite() = nil, want a refusal")
			}
			if stdout.Len() != 0 {
				t.Errorf("a refused invitation still printed %q", stdout.String())
			}
		})
	}
}

// run dispatches a first argument that is not a flag, and refuses one it does
// not know. Serving is what everything else still means.
func TestRunDispatchesSubcommands(t *testing.T) {
	t.Run("unknown", func(t *testing.T) {
		err := run([]string{"summon"}, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "summon") {
			t.Fatalf("run() = %v, want a refusal naming the subcommand", err)
		}
	})

	// A flag is not a subcommand: it reaches the server, which refuses it the
	// way it always has.
	t.Run("a flag is still a flag", func(t *testing.T) {
		err := run([]string{"-nonesuch"}, io.Discard)
		if err == nil {
			t.Fatal("run() = nil, want the flag package's refusal")
		}
		if strings.Contains(err.Error(), "subcommand") {
			t.Errorf("run() = %v, want a flag error", err)
		}
	})
}
