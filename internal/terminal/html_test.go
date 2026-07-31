package terminal

import (
	"strings"
	"testing"

	"github.com/maloquacious/zmachine"
)

func renderTurn(t *testing.T, turn Turn) string {
	t.Helper()

	var b strings.Builder
	if err := turn.WriteHTML(&b); err != nil {
		t.Fatalf("WriteHTML() error = %v", err)
	}
	return b.String()
}

func renderStatus(t *testing.T, status zmachine.StatusLine) string {
	t.Helper()

	var b strings.Builder
	if err := WriteStatusHTML(&b, status); err != nil {
		t.Fatalf("WriteStatusHTML() error = %v", err)
	}
	return b.String()
}

// Story output is data, and the command is whatever the player typed. Neither
// is ever markup.
func TestHTMLEscapesEverythingUntrusted(t *testing.T) {
	const attack = `<script>alert("xss")</script>`

	tests := []struct {
		name string
		html string
	}{
		{
			name: "story output",
			html: renderTurn(t, Turn{Output: "You see " + attack + " here."}),
		},
		{
			name: "the player's command",
			html: renderTurn(t, Turn{Command: attack, Output: "I don't know that word."}),
		},
		{
			name: "the upper window",
			html: renderTurn(t, Turn{UpperWindow: attack, Output: ">"}),
		},
		{
			name: "the room name",
			html: renderStatus(t, zmachine.StatusLine{Available: true, Name: attack}),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if strings.Contains(tt.html, "<script>") {
				t.Errorf("unescaped markup reached the page:\n%s", tt.html)
			}
			if !strings.Contains(tt.html, "&lt;script&gt;") {
				t.Errorf("the text was lost rather than escaped:\n%s", tt.html)
			}
		})
	}
}

// The transcript is written into <pre> and wrapped by CSS, so the story's own
// whitespace reaches the page exactly as the story wrote it.
func TestHTMLPreservesTheStorysWhitespace(t *testing.T) {
	output := "West of House\nYou are standing in an open field west of a white house, with a boarded front door.\n\n    There is a small mailbox here.\n\n>"

	html := renderTurn(t, Turn{Command: "look", Output: output})

	// Every newline, blank line and leading space survives; only the prompt
	// changes, because ">" is markup and has to be escaped.
	if !strings.Contains(html, strings.ReplaceAll(output, ">", "&gt;")) {
		t.Errorf("the story's whitespace did not survive:\n%s", html)
	}
	if !strings.Contains(html, `<pre class="output">`) {
		t.Errorf("the transcript is not in a pre element:\n%s", html)
	}
}

func TestTurnHTMLOmitsWhatIsNotThere(t *testing.T) {
	opening := renderTurn(t, Turn{Output: "West of House\n\n>"})

	if strings.Contains(opening, `class="command"`) {
		t.Errorf("the opening turn echoed a command nobody typed:\n%s", opening)
	}
	if strings.Contains(opening, "upper-window") {
		t.Errorf("an empty upper window was rendered:\n%s", opening)
	}

	played := renderTurn(t, Turn{Command: "open mailbox", Output: "Opening the small mailbox reveals a leaflet.\n\n>"})
	if !strings.Contains(played, "&gt;open mailbox") {
		t.Errorf("the command was not echoed with its prompt:\n%s", played)
	}
}

func TestStatusHTML(t *testing.T) {
	score := renderStatus(t, zmachine.StatusLine{Available: true, Name: "Cellar", Score: 25, Turns: 11})

	for _, want := range []string{"Cellar", "Score: 25", "Moves: 11", `id="status-bar"`} {
		if !strings.Contains(score, want) {
			t.Errorf("status bar missing %q:\n%s", want, score)
		}
	}

	timed := renderStatus(t, zmachine.StatusLine{Available: true, Name: "Bedroom", TimeGame: true, Hours: 13, Minutes: 5})
	if !strings.Contains(timed, "Time: 1:05 pm") {
		t.Errorf("a time game rendered no time:\n%s", timed)
	}
	if strings.Contains(timed, "Score:") {
		t.Errorf("a time game rendered a score:\n%s", timed)
	}
}

// Until Available is true every other field is meaningless, so there is
// nothing honest to draw.
func TestStatusHTMLWritesNothingWhenUnavailable(t *testing.T) {
	if got := renderStatus(t, zmachine.StatusLine{Name: "West of House", Score: 10}); got != "" {
		t.Errorf("WriteStatusHTML() = %q, want nothing", got)
	}
}
