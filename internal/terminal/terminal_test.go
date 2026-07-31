package terminal

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/maloquacious/zmachine"
)

func TestWrap(t *testing.T) {
	tests := []struct {
		name  string
		text  string
		width int
		want  string
	}{
		{
			name:  "empty",
			text:  "",
			width: 20,
			want:  "",
		},
		{
			name:  "short enough to leave alone",
			text:  "West of House",
			width: 20,
			want:  "West of House",
		},
		{
			name:  "folds at the last space that fits",
			text:  "You are standing in an open field west of a white house.",
			width: 20,
			want:  "You are standing in\nan open field west\nof a white house.",
		},
		{
			name:  "blank lines survive",
			text:  "Kitchen\n\nYou are in the kitchen.\n",
			width: 20,
			want:  "Kitchen\n\nYou are in the\nkitchen.\n",
		},
		{
			name:  "indentation survives on continuation lines",
			text:  "    WELCOME TO ZORK! This is a game of adventure.",
			width: 24,
			want:  "    WELCOME TO ZORK!\n    This is a game of\n    adventure.",
		},
		{
			name:  "runs of spaces inside a line survive",
			text:  "Score: 10   Moves: 3",
			width: 40,
			want:  "Score: 10   Moves: 3",
		},
		{
			name:  "a word longer than the width overhangs rather than breaking",
			text:  "supercalifragilistic expialidocious",
			width: 10,
			want:  "supercalifragilistic\nexpialidocious",
		},
		{
			name:  "the prompt keeps its missing newline",
			text:  "Opening the small mailbox reveals a leaflet.\n\n>",
			width: 30,
			want:  "Opening the small mailbox\nreveals a leaflet.\n\n>",
		},
		{
			name:  "a width of zero leaves the text to CSS",
			text:  "You are standing in an open field west of a white house.",
			width: 0,
			want:  "You are standing in an open field west of a white house.",
		},
		{
			name:  "trailing spaces stay on the line they were written on",
			text:  "one two three   ",
			width: 9,
			want:  "one two\nthree   ",
		},
		{
			name:  "an exact fit is not folded",
			text:  "12345 7890",
			width: 10,
			want:  "12345 7890",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Wrap(tt.text, tt.width); got != tt.want {
				t.Errorf("Wrap() =\n%q\nwant\n%q", got, tt.want)
			}
		})
	}
}

// Wrapping may fold the story's text but must never change what it says.
func TestWrapPreservesTheWords(t *testing.T) {
	text := "You are in a dark and damp cellar with a narrow passageway leading north,\nand a crawlway to the south.\n\n>"

	for width := 8; width <= 100; width++ {
		wrapped := Wrap(text, width)

		if strings.Join(strings.Fields(wrapped), " ") != strings.Join(strings.Fields(text), " ") {
			t.Fatalf("width %d changed the words: %q", width, wrapped)
		}
		if strings.HasSuffix(text, "\n") != strings.HasSuffix(wrapped, "\n") {
			t.Fatalf("width %d changed the trailing newline", width)
		}
	}
}

// A wrapped line must fit, except for the single word that cannot.
func TestWrapRespectsTheWidth(t *testing.T) {
	const width = 32

	text := "This is a small room with passages to the east and south and a forbidding hole leading west. Bloodstains and deep scratches (perhaps made by an axe) mar the walls."

	for line := range strings.SplitSeq(Wrap(text, width), "\n") {
		if utf8.RuneCountInString(line) > width {
			t.Errorf("line is %d columns wide, want at most %d: %q",
				utf8.RuneCountInString(line), width, line)
		}
	}
}

func TestStatusBar(t *testing.T) {
	tests := []struct {
		name   string
		status zmachine.StatusLine
		width  int
		want   string
	}{
		{
			name:   "no status line is no bar",
			status: zmachine.StatusLine{Name: "West of House", Score: 10},
			width:  40,
			want:   "",
		},
		{
			name:   "score game",
			status: zmachine.StatusLine{Available: true, Name: "West of House", Score: 10, Turns: 3},
			width:  40,
			want:   "West of House       Score: 10   Moves: 3",
		},
		{
			name:   "time game",
			status: zmachine.StatusLine{Available: true, Name: "Bedroom", TimeGame: true, Hours: 13, Minutes: 5},
			width:  40,
			want:   "Bedroom                    Time: 1:05 pm",
		},
		{
			name:   "midnight is twelve",
			status: zmachine.StatusLine{Available: true, Name: "Bedroom", TimeGame: true, Hours: 0, Minutes: 0},
			width:  40,
			want:   "Bedroom                   Time: 12:00 am",
		},
		{
			name:   "a long room name gives way to the score",
			status: zmachine.StatusLine{Available: true, Name: "The Entrance to Hades", Score: 10, Turns: 3},
			width:  30,
			want:   "The En…   Score: 10   Moves: 3",
		},
		{
			name:   "a width of zero leaves the spacing to CSS",
			status: zmachine.StatusLine{Available: true, Name: "Cellar", Score: 25, Turns: 11},
			width:  0,
			want:   "Cellar   Score: 25   Moves: 11",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StatusBar(tt.status, tt.width); got != tt.want {
				t.Errorf("StatusBar() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTurnText(t *testing.T) {
	turn := NewTurn("look", zmachine.Result{
		Output:     "Kitchen\nYou are in the kitchen of the white house.\n\n>",
		StatusLine: zmachine.StatusLine{Available: true, Name: "Kitchen", Score: 10, Turns: 6},
	})

	got := turn.Text(40)

	want := "\nKitchen             Score: 10   Moves: 6\nKitchen\nYou are in the kitchen of the white\nhouse.\n\n>"
	if got != want {
		t.Errorf("Text() =\n%q\nwant\n%q", got, want)
	}
	if strings.Contains(got, "look") {
		t.Error("Text() echoed the command; the terminal that read it already did")
	}
}

// The upper window overlays fixed screen positions, so it is presented whole
// rather than folded into the transcript.
func TestTurnTextKeepsTheUpperWindowIntact(t *testing.T) {
	upper := "Sonar:  contact bearing 045   range 2000 yards, closing fast on the vessel"

	turn := NewTurn("look", zmachine.Result{Output: ">", UpperWindow: upper})

	if !strings.Contains(turn.Text(40), upper) {
		t.Errorf("Text() folded the upper window:\n%q", turn.Text(40))
	}
}
