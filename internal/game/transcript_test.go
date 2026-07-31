package game

import (
	"strings"
	"testing"
)

// The story's output ends with its prompt and no newline after it, so the
// player's line has to continue that line. Anything else puts the command on a
// line of its own with a naked ">" above it.
func TestAppendTranscript(t *testing.T) {
	const opening = "West of House\n\n>"

	got := appendTranscript(opening, "open mailbox", "Opening the small mailbox reveals a leaflet.\n\n>")

	want := "West of House\n\n>open mailbox\nOpening the small mailbox reveals a leaflet.\n\n>"
	if got != want {
		t.Errorf("appendTranscript() =\n%q\nwant\n%q", got, want)
	}
}

func TestTrimTranscript(t *testing.T) {
	tests := []struct {
		name       string
		transcript string
		limit      int
		want       string
	}{
		{"under the limit", "one\ntwo\n", 100, "one\ntwo\n"},
		{"at the limit", "12345", 5, "12345"},
		{"cuts at a line boundary", "oldest\nolder\nnewest\n", 14, "older\nnewest\n"},
		{"one line longer than the limit", "aaaaaaaaaa", 4, "aaaa"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := trimTranscript(tt.transcript, tt.limit); got != tt.want {
				t.Errorf("trimTranscript() = %q, want %q", got, tt.want)
			}
		})
	}
}

// The transcript is what a refresh redraws, and it must not grow without limit.
func TestTranscriptIsKeptAndBounded(t *testing.T) {
	store := NewMemoryStore()
	service := serviceOver(t, store)

	session, opening, err := service.NewGame(t.Context(), player, "zork1")
	if err != nil {
		t.Fatalf("NewGame() error = %v", err)
	}
	if session.Transcript != opening.Output {
		t.Error("the opening turn was not kept")
	}

	for _, command := range []string{"open mailbox", "take leaflet", "read leaflet"} {
		session, _, err = service.Play(t.Context(), player, session.ID, command)
		if err != nil {
			t.Fatalf("Play(%q) error = %v", command, err)
		}
	}

	for _, want := range []string{"West of House", ">open mailbox", "WELCOME TO ZORK"} {
		if !strings.Contains(session.Transcript, want) {
			t.Errorf("the transcript is missing %q", want)
		}
	}

	// And the status bar the last turn reported travels with it.
	if !session.Status.Available || session.Status.Name != "West of House" {
		t.Errorf("status = %+v, want the bar the last turn reported", session.Status)
	}

	// Long enough to have to trim, played against a real story.
	for range 600 {
		session, _, err = service.Play(t.Context(), player, session.ID, "look")
		if err != nil {
			t.Fatalf("Play() error = %v", err)
		}
		if len(session.Transcript) > MaxTranscriptBytes {
			t.Fatalf("the transcript reached %d bytes, over the %d limit",
				len(session.Transcript), MaxTranscriptBytes)
		}
	}

	if len(session.Transcript) < MaxTranscriptBytes/2 {
		t.Errorf("the transcript is only %d bytes; the trim is taking too much",
			len(session.Transcript))
	}
	if strings.Contains(session.Transcript, "WELCOME TO ZORK") {
		t.Error("the oldest lines were not the ones trimmed")
	}
	if !strings.HasSuffix(session.Transcript, ">") {
		t.Error("the transcript does not end at the prompt")
	}
}

// The lobby lists a player's games, most recently played first.
func TestGamesLists(t *testing.T) {
	service := serviceOver(t, NewMemoryStore())

	first, _, err := service.NewGame(t.Context(), player, "zork1")
	if err != nil {
		t.Fatalf("NewGame() error = %v", err)
	}
	second, _, err := service.NewGame(t.Context(), player, "zork2")
	if err != nil {
		t.Fatalf("NewGame() error = %v", err)
	}
	if _, _, err := service.Play(t.Context(), player, first.ID, "north"); err != nil {
		t.Fatalf("Play() error = %v", err)
	}

	games, err := service.Games(t.Context(), player)
	if err != nil {
		t.Fatalf("Games() error = %v", err)
	}
	if len(games) != 2 {
		t.Fatalf("Games() returned %d games, want 2", len(games))
	}
	if games[0].ID != first.ID {
		t.Errorf("Games()[0] = %s, want the one just played (%s)", games[0].ID, first.ID)
	}
	if games[0].Turn != 1 {
		t.Errorf("Turn = %d, want 1", games[0].Turn)
	}
	if games[1].ID != second.ID {
		t.Errorf("Games()[1] = %s, want %s", games[1].ID, second.ID)
	}

	// One player's list is their own.
	theirs, err := service.Games(t.Context(), "2")
	if err != nil {
		t.Fatalf("Games() error = %v", err)
	}
	if len(theirs) != 0 {
		t.Errorf("another user sees %d games, want 0", len(theirs))
	}

	if _, err := service.Games(t.Context(), ""); err == nil {
		t.Error("Games() with no user = nil error, want failure")
	}
}
