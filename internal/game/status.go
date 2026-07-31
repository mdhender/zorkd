package game

import "github.com/maloquacious/zmachine"

// A StatusLine is the status bar the story last reported.
//
// It mirrors [zmachine.StatusLine] rather than holding one, because it is
// stored: the store beneath this package has no business importing the engine,
// and this is the one engine value that has to survive a turn. The fields the
// engine reports that no bar draws — the object number behind the room name —
// are left out.
//
// Available is false until the story has updated the line at least once, and
// every other field is meaningless until it is true.
type StatusLine struct {
	Available bool
	Name      string

	// TimeGame chooses which pair below is meaningful: Score and Moves for a
	// score game, Hours and Minutes for a time game. Zork is a score game.
	TimeGame bool

	Score int16
	Moves int16

	Hours   uint8
	Minutes uint8
}

// statusOf converts what the engine reported into what is stored.
func statusOf(status zmachine.StatusLine) StatusLine {
	return StatusLine{
		Available: status.Available,
		Name:      status.Name,
		TimeGame:  status.TimeGame,
		Score:     status.Score,
		Moves:     status.Turns,
		Hours:     status.Hours,
		Minutes:   status.Minutes,
	}
}
