package game

import (
	"strings"
	"unicode"
)

// An Intent is what a player's line means to this application.
type Intent int

const (
	// IntentPlay is the ordinary case: the line belongs to the story, and the
	// engine answers it.
	IntentPlay Intent = iota

	// IntentSave is a request to write a named save.
	IntentSave

	// IntentRestore is a request to go back to one.
	IntentRestore
)

// String returns the intent's name, suitable for a log field.
func (i Intent) String() string {
	switch i {
	case IntentPlay:
		return "play"
	case IntentSave:
		return "save"
	case IntentRestore:
		return "restore"
	}
	return "unknown"
}

// A Command is a player's line after this application has looked at it.
type Command struct {
	// Intent is who answers the line.
	Intent Intent

	// Text is the line as typed, trimmed of surrounding space. It is what
	// reaches the engine when Intent is IntentPlay.
	Text string

	// Name is whatever followed SAVE or RESTORE. It is empty when the player
	// typed the bare verb, which means they have still to be asked.
	Name string
}

// Interpret decides whether a line is the story's to answer or this
// application's.
//
// SAVE and RESTORE are this application's. In Version 3 the story's own save
// and restore report failure without branching — legal, and the story copes,
// but in Zork I the player simply sees "Failed." Since this application owns
// persistence, those two lines are answered here and the engine never sees
// them.
//
// The match is deliberately narrow: the first word, and only those two words.
// Everything else passes through exactly as typed, because a heuristic that
// guessed wrong would swallow a command the player meant for the story. Taking
// the rest of the line as a save name is safe for the same reason it is
// useful — Zork's parser has no other sense of either verb, so nothing
// legitimate is being taken away.
func Interpret(line string) Command {
	text := strings.TrimSpace(line)

	verb, rest := text, ""
	if i := strings.IndexFunc(text, unicode.IsSpace); i >= 0 {
		verb, rest = text[:i], strings.TrimSpace(text[i+1:])
	}

	switch strings.ToLower(verb) {
	case "save":
		return Command{Intent: IntentSave, Text: text, Name: rest}
	case "restore":
		return Command{Intent: IntentRestore, Text: text, Name: rest}
	}

	return Command{Intent: IntentPlay, Text: text}
}
