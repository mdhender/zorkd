// Package games embeds the Zork story files the server runs.
//
// The story images ship inside the executable rather than being read from disk
// at startup. Deployment is then the binary and its database, and a server can
// never come up missing the game it was meant to serve.
//
// Each file is a third-party work under its own LICENSE. See README.md.
package games

import _ "embed"

//go:embed zork1/zork1-r119-880429.z3
var zork1 []byte

//go:embed zork2/zork2-r63-860811.z3
var zork2 []byte

//go:embed zork3/zork3-r25-860811.z3
var zork3 []byte

// Story is one embedded story file.
type Story struct {
	// ID names the game in URLs, configuration and stored sessions.
	// Sessions refer to it, so it is stable.
	ID string

	// Title is the game's display name.
	Title string

	// File is the story file's name. It carries the release and serial
	// number from the story's own header, because a saved state only
	// restores against the exact story it was made from.
	File string

	// Data is the story image.
	//
	// The slice is shared by every caller and must not be modified.
	// zmachine.LoadStory copies what it is given, so execution never
	// writes here, but nothing else may either.
	Data []byte
}

// All returns every embedded story, in release order.
//
// The returned slice is fresh on each call; the Data slices it refers to are
// not.
func All() []Story {
	return []Story{
		{ID: "zork1", Title: "Zork I: The Great Underground Empire", File: "zork1-r119-880429.z3", Data: zork1},
		{ID: "zork2", Title: "Zork II: The Wizard of Frobozz", File: "zork2-r63-860811.z3", Data: zork2},
		{ID: "zork3", Title: "Zork III: The Dungeon Master", File: "zork3-r25-860811.z3", Data: zork3},
	}
}
