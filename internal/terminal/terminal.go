// Package terminal renders a turn for a character terminal.
//
// The engine hands back text, not display operations: it performs no word
// wrapping, has no notion of screen width, and reports the status line rather
// than printing it. Everything a player sees is therefore built here.
//
// This is the plain-text presentation, for a real terminal. The browser is a
// different terminal with different rules — CSS wraps there, so no newline is
// ever inserted — and its markup lives with the server that sends it.
package terminal

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/maloquacious/zmachine"
)

// DefaultWidth is the column count the presentation targets.
const DefaultWidth = 80

// A Turn is one exchange: what the player typed, and what the story answered.
type Turn struct {
	// Command is the line the player typed. It is echoed in HTML, where the
	// browser has no local echo of its own, and left out of the plain-text
	// rendering, where the terminal already showed it.
	Command string

	Output      string
	UpperWindow string
	Status      zmachine.StatusLine
}

// NewTurn pairs an engine result with the command that produced it. The
// command is empty for the opening turn, which no player asked for.
func NewTurn(command string, result zmachine.Result) Turn {
	return Turn{
		Command:     command,
		Output:      result.Output,
		UpperWindow: result.UpperWindow,
		Status:      result.StatusLine,
	}
}

// Text renders the turn for a character terminal of the given width.
//
// The status bar is drawn first, then the upper window, then the story text
// wrapped to the width. The player's own line is not echoed: the terminal that
// read it already did.
func (t Turn) Text(width int) string {
	var b strings.Builder

	if bar := StatusBar(t.Status, width); bar != "" {
		b.WriteString("\n")
		b.WriteString(bar)
		b.WriteString("\n")
	}
	if t.UpperWindow != "" {
		b.WriteString(t.UpperWindow)
		b.WriteString("\n")
	}
	b.WriteString(Wrap(t.Output, width))

	return b.String()
}

// StatusBar renders the status line the engine reported.
//
// It returns the empty string when no status line is available, which is the
// only honest rendering of it: every other field is meaningless until then, and
// a caller showing the previous bar is showing something that was once true.
func StatusBar(status zmachine.StatusLine, width int) string {
	if !status.Available {
		return ""
	}

	right := scoreAndMoves(status)
	if width <= 0 {
		return status.Name + "   " + right
	}

	// The room name gives way first: it is the field that can be long, and
	// the score is the field a player is watching.
	name, scored := status.Name, utf8.RuneCountInString(right)
	if utf8.RuneCountInString(name)+scored+3 > width {
		name = truncate(name, width-scored-3)
	}

	gap := max(width-utf8.RuneCountInString(name)-scored, 1)

	return name + strings.Repeat(" ", gap) + right
}

func scoreAndMoves(status zmachine.StatusLine) string {
	// A time game reports the hour instead of a score. Zork is a score game;
	// this branch is here because the engine can report either.
	if status.TimeGame {
		return "Time: " + Clock(status)
	}

	return fmt.Sprintf("Score: %d   Moves: %d", status.Score, status.Turns)
}

// Clock formats the hour a time game reports.
//
// It is meaningless unless [zmachine.StatusLine.TimeGame] is true. Zork is a
// score game and will never reach it; the engine can report either, so both are
// rendered.
func Clock(status zmachine.StatusLine) string {
	hour, suffix := status.Hours%12, "am"
	if hour == 0 {
		hour = 12
	}
	if status.Hours >= 12 {
		suffix = "pm"
	}
	return fmt.Sprintf("%d:%02d %s", hour, status.Minutes, suffix)
}

// Wrap folds text to the given width, preserving everything the story meant by
// its own whitespace.
//
// Blank lines, leading indentation and runs of spaces inside a line survive, as
// does the absence of a trailing newline — Zork's prompt is a bare ">" with the
// cursor left beside it, and a newline appended there would move the player's
// typing to the next line. A word longer than the width is left whole and
// allowed to overhang rather than broken in half.
//
// A width of zero or less returns the text unchanged, which is what a caller
// wrapping in CSS wants.
func Wrap(text string, width int) string {
	if width <= 0 || text == "" {
		return text
	}

	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = wrapLine(line, width)
	}

	return strings.Join(lines, "\n")
}

func wrapLine(line string, width int) string {
	if utf8.RuneCountInString(line) <= width {
		return line
	}

	// Continuation lines keep the line's own indentation, so an indented
	// paragraph stays indented instead of unravelling to the left margin.
	indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
	if utf8.RuneCountInString(indent) >= width {
		return line
	}

	var (
		out  strings.Builder
		cur  = indent
		curN = utf8.RuneCountInString(indent)
		rest = line[len(indent):]
		gap  string
	)

	for rest != "" {
		space := len(rest) - len(strings.TrimLeft(rest, " \t"))
		gap, rest = rest[:space], rest[space:]
		if rest == "" {
			// Trailing whitespace belongs to the line it was written on.
			cur += gap
			break
		}

		end := strings.IndexAny(rest, " \t")
		if end < 0 {
			end = len(rest)
		}
		word, wordN := rest[:end], utf8.RuneCountInString(rest[:end])
		rest = rest[end:]

		switch {
		case curN == utf8.RuneCountInString(indent) && cur == indent:
			// First word on a line goes on it however long it is.
			cur += gap + word
			curN += utf8.RuneCountInString(gap) + wordN
		case curN+utf8.RuneCountInString(gap)+wordN <= width:
			cur += gap + word
			curN += utf8.RuneCountInString(gap) + wordN
		default:
			// The break replaces the run of spaces that would have preceded
			// the word. There is nowhere else for it to go.
			out.WriteString(cur)
			out.WriteString("\n")
			cur, curN = indent+word, utf8.RuneCountInString(indent)+wordN
		}
	}

	out.WriteString(cur)
	return out.String()
}

// truncate shortens a string to n runes, marking that it was cut.
func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}

	runes := []rune(s)
	if n == 1 {
		return string(runes[:1])
	}
	return string(runes[:n-1]) + "…"
}
