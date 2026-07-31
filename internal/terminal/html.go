package terminal

import (
	"fmt"
	"html/template"
	"io"

	"github.com/maloquacious/zmachine"
)

// The story's whitespace is meaningful — blank lines between paragraphs,
// indentation, the prompt with the cursor beside it — so the transcript is
// written into <pre> and wrapped by CSS (white-space: pre-wrap) rather than by
// inserting newlines. The stored text stays exactly as the story wrote it.
//
// The upper window is deliberately not wrapped at all. It overlays fixed screen
// positions, so folding a long line would break the alignment that is its only
// purpose; it is allowed to scroll sideways instead.
var (
	turnHTML = template.Must(template.New("turn").Parse(
		`<div class="turn">
{{if .Command}}<pre class="command">&gt;{{.Command}}</pre>
{{end}}{{if .UpperWindow}}<pre class="upper-window">{{.UpperWindow}}</pre>
{{end}}<pre class="output">{{.Output}}</pre>
</div>
`))

	statusHTML = template.Must(template.New("status").Parse(
		`<div class="status-bar" id="status-bar">
<span class="room">{{.Name}}</span>
{{if .TimeGame}}<span class="time">{{.Time}}</span>
{{else}}<span class="score">Score: {{.Score}}</span>
<span class="moves">Moves: {{.Moves}}</span>
{{end}}</div>
`))
)

// WriteHTML writes one turn as an HTML fragment.
//
// Story output and the player's own command are escaped on the way in. Neither
// is ever trusted markup: the story is data, and the command came from whoever
// typed it.
func (t Turn) WriteHTML(w io.Writer) error {
	if err := turnHTML.Execute(w, t); err != nil {
		return fmt.Errorf("terminal: render turn: %w", err)
	}
	return nil
}

// WriteStatusHTML writes the status bar as an HTML fragment.
//
// It writes nothing when the engine reported no status line. Every other field
// is meaningless until Available is true, and showing a bar built from them
// would be showing something that was never true.
func WriteStatusHTML(w io.Writer, status zmachine.StatusLine) error {
	if !status.Available {
		return nil
	}

	view := struct {
		Name     string
		Score    int16
		Moves    int16
		TimeGame bool
		Time     string
	}{
		Name:     status.Name,
		Score:    status.Score,
		Moves:    status.Turns,
		TimeGame: status.TimeGame,
	}
	if status.TimeGame {
		view.Time = scoreAndMoves(status)
	}

	if err := statusHTML.Execute(w, view); err != nil {
		return fmt.Errorf("terminal: render status line: %w", err)
	}
	return nil
}
