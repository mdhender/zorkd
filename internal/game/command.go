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

	// IntentRestart is a request to begin the story again from the start.
	IntentRestart
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
	case IntentRestart:
		return "restart"
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
	// typed the bare verb, which means they have still to be asked, and always
	// empty for RESTART, which takes no argument.
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
// RESTART is this application's for a different reason. The engine implements
// the opcode perfectly well, but the transcript and the move count sit beside
// the state rather than inside it, and nothing in the Result says a restart
// happened — so a restart the engine carried out would leave the abandoned game
// on the screen with the new one printed underneath. It is answered here, where
// the screen can go back with the state.
//
// The match is deliberately narrow: the first word, and only those three words.
// Everything else passes through exactly as typed, because a heuristic that
// guessed wrong would swallow a command the player meant for the story. Taking
// the rest of the line as a save name is safe for the same reason it is
// useful — Zork's parser has no other sense of any of these verbs, so nothing
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
	case "restart":
		// Nothing follows RESTART: there is one game to begin again, and the
		// player is asked to confirm rather than to name anything.
		return Command{Intent: IntentRestart, Text: text}
	}

	return Command{Intent: IntentPlay, Text: text}
}
