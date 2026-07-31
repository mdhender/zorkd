package main

import (
	"bytes"
	"flag"
	"strings"
	"testing"
)

// session runs zorkplay over a scripted set of commands and returns what the
// player would have seen.
func session(t *testing.T, args []string, commands ...string) (stdout, stderr string, err error) {
	t.Helper()

	var out, errs bytes.Buffer
	script := ""
	if len(commands) > 0 {
		script = strings.Join(commands, "\n") + "\n"
	}

	err = run(args, strings.NewReader(script), &out, &errs)
	return out.String(), errs.String(), err
}

// The whole point of the program: a sequence of commands, each played in a
// machine built from the story and the previous turn's state.
func TestPlayASession(t *testing.T) {
	stdout, _, err := session(t, nil, "open mailbox", "read leaflet", "north")
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}

	for _, want := range []string{
		"ZORK I: The Great Underground Empire",
		"reveals a leaflet",
		"WELCOME TO ZORK",
		"North of House",
		"Score: 0   Moves: 3",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("session output missing %q", want)
		}
	}
}

// The story's text is wrapped to the requested width on its way to the screen;
// the engine performs no wrapping of its own.
func TestPlayWrapsToTheRequestedWidth(t *testing.T) {
	const width = 40

	stdout, _, err := session(t, []string{"-width", "40"}, "look")
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}

	for line := range strings.SplitSeq(stdout, "\n") {
		if len([]rune(line)) > width {
			t.Errorf("line is %d columns wide, want at most %d: %q", len([]rune(line)), width, line)
		}
	}
	if !strings.Contains(stdout, "You are standing in an open field") {
		t.Error("the opening description did not survive wrapping")
	}
}

// End of input ends the session, the way closing a terminal does.
func TestPlayStopsAtEndOfInput(t *testing.T) {
	stdout, _, err := session(t, nil)
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if !strings.Contains(stdout, "West of House") {
		t.Error("session output missing the opening room")
	}
}

// A story that ends itself ends the program, and the commands after it are
// never played.
func TestPlayStopsWhenTheStoryHalts(t *testing.T) {
	stdout, _, err := session(t, nil, "quit", "yes", "open mailbox")
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if strings.Contains(stdout, "reveals a leaflet") {
		t.Error("a command was played after the story halted")
	}
}

// An overlong line is refused before the engine sees it, and the session
// continues: a typo is not a reason to end someone's game.
func TestPlayRefusesAnOverlongCommand(t *testing.T) {
	long := strings.Repeat("x", 400)

	stdout, stderr, err := session(t, nil, long, "open mailbox")
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if !strings.Contains(stderr, "limit is") {
		t.Errorf("stderr = %q, want a complaint about the command length", stderr)
	}
	if !strings.Contains(stdout, "reveals a leaflet") {
		t.Error("the session did not continue after an overlong command")
	}
}

func TestPlayEachGame(t *testing.T) {
	tests := []struct {
		game string
		want string
	}{
		{game: "zork1", want: "ZORK I: The Great Underground Empire"},
		{game: "zork2", want: "ZORK II: The Wizard of Frobozz"},
		{game: "zork3", want: "ZORK III: The Dungeon Master"},
	}

	for _, tt := range tests {
		t.Run(tt.game, func(t *testing.T) {
			stdout, _, err := session(t, []string{tt.game}, "look")
			if err != nil {
				t.Fatalf("run() error = %v", err)
			}
			if !strings.Contains(stdout, tt.want) {
				t.Errorf("session output missing %q", tt.want)
			}
		})
	}
}

// A seeded session is reproducible, which is what makes a reported turn worth
// reporting.
func TestPlayWithASeedIsReproducible(t *testing.T) {
	first, _, err := session(t, []string{"-seed", "1988"}, "open mailbox", "north", "east")
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
	second, _, err := session(t, []string{"-seed", "1988"}, "open mailbox", "north", "east")
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}

	if first != second {
		t.Error("the same seed produced a different session")
	}
}

func TestListGames(t *testing.T) {
	stdout, _, err := session(t, []string{"-list"})
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}

	for _, want := range []string{"zork1", "zork2", "zork3", "release 119", "serial 880429"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("-list output missing %q", want)
		}
	}
	if strings.Contains(stdout, "West of House") {
		t.Error("-list started a game")
	}
}

func TestRunRejectsBadArguments(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "unknown game", args: []string{"zork4"}},
		{name: "too many games", args: []string{"zork1", "zork2"}},
		{name: "unknown flag", args: []string{"-nope"}},
		{name: "unparsable timeout", args: []string{"-timeout", "soon"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout, _, err := session(t, tt.args, "look")
			if err == nil {
				t.Fatal("run() = nil error, want failure")
			}
			if strings.Contains(stdout, "West of House") {
				t.Error("a game started despite the bad arguments")
			}
		})
	}
}

// -help is a request, not a failure, but it still must not start a game.
func TestHelpDoesNotStartAGame(t *testing.T) {
	stdout, stderr, err := session(t, []string{"-help"})
	if err != flag.ErrHelp {
		t.Errorf("run(-help) error = %v, want %v", err, flag.ErrHelp)
	}
	if !strings.Contains(stderr, "usage: zorkplay") {
		t.Errorf("stderr = %q, want usage", stderr)
	}
	if strings.Contains(stdout, "West of House") {
		t.Error("-help started a game")
	}
}
