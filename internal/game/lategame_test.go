package game

import (
	"bytes"
	"strings"
	"testing"

	"github.com/maloquacious/zmachine"
)

// lateGameSeed is the seed the route was built against.
//
// The troll fight rolls dice, so an unseeded run is a different game every
// time: the troll may take one blow or six, and it may knock the player out
// or kill them. This seed was chosen because the fight resolves in three
// blows and the player survives, which makes the route below a fixed
// sequence rather than a gamble. Changing it will almost certainly require
// rebuilding the route.
const lateGameSeed = 1988

// lateGameRoute plays Zork I from the mailbox to the Egyptian Room and back
// out through the temple.
//
// Every existing test in this package stops within sight of the white house,
// so nothing here has ever restored a state whose dynamic memory had moved
// far from the story image. This route does: it collects and cases four
// treasures, kills the troll, crosses the maze to the skeleton, drives off
// the cyclops, ropes down the dome, and prays out of the temple carrying the
// coffin. By the end the score is 110 of 350 and the thief is awake.
//
// Every command in it does something — there is no "You can't go that way"
// anywhere in the transcript — so a route that silently stops working will
// fail the landmark assertions rather than quietly wander.
var lateGameRoute = []string{
	"open mailbox",
	"take leaflet",
	"read leaflet",
	"drop leaflet",
	"north",
	"north",
	"climb tree",
	"take egg",
	"down",
	"south",
	"east",
	"open window",
	"enter window",
	"west",
	"take lamp",
	"take sword",
	"turn on lamp",
	"east",
	"up",
	"take rope",
	"take knife",
	"down",
	"west",
	"open case",
	"put egg in case",
	"move rug",
	"open trap door",
	"down",
	"north",
	"attack troll with sword",
	"attack troll with sword",
	"attack troll with sword",
	"west",
	"south",
	"east",
	"up",
	"take coins",
	"take key",
	"southwest",
	"east",
	"south",
	"southeast",
	"ulysses",
	"east",
	"east",
	"put coins in case",
	"open trap door",
	"down",
	"north",
	"east",
	"east",
	"southeast",
	"east",
	"tie rope to railing",
	"down",
	"take torch",
	"turn off lamp",
	"down",
	"east",
	"drop sword",
	"drop lamp",
	"take coffin",
	"west",
	"south",
	"pray",
	"east",
	"south",
	"east",
	"enter window",
	"west",
	"open coffin",
	"put coffin in case",
	"put torch in case",
}

// lateGameLandmarks are lines the route must produce, in this order.
//
// They are transcribed from the transcript the route actually printed, not
// invented, and each one is the story's own report that the route arrived
// somewhere: the cellar, the dead troll, the maze, the skeleton, the fleeing
// cyclops, the dome, the coffin, the thief, and the prayer out of the temple.
//
// Asserting them in order also asserts that the route did not take a shortcut:
// the cyclops cannot flee before the troll dies.
var lateGameLandmarks = []string{
	// Out of the house and down the trap door.
	"The trap door crashes shut, and you hear someone barring it.",
	"Cellar",
	// The troll fight, which is the reason the seed is fixed.
	"A nasty-looking troll, brandishing a bloody axe, blocks all passages out of the room.",
	"The unconscious troll cannot defend himself: He dies.",
	// Into the maze, and across it to the skeleton.
	"This is part of a maze of twisty little passages, all alike.",
	"A skeleton, probably the remains of a luckless adventurer, lies here.",
	"An old leather bag, bulging with coins, is here.",
	// Out of the maze past the cyclops, who opens the way back to the house.
	"Cyclops Room",
	"The cyclops, hearing the name of his father's deadly nemesis, flees the room by knocking down the wall on the east of the room.",
	"Strange Passage",
	// Back down, over the dome on the rope, and into the temple.
	"Dome Room",
	"The rope drops over the side and comes within ten feet of the floor.",
	"Sitting on the pedestal is a flaming torch, made of ivory.",
	"The solid-gold coffin used for the burial of Ramses II is here.",
	// The thief is abroad by now, which no shorter route reaches.
	"Someone carrying a large bag is casually leaning against one of the walls here.",
	// Praying at the altar is the only way out carrying the coffin.
	"On the altar is a large black book, open to page 569.",
	"This is a forest, with trees in all directions. To the east, there appears to be sunlight.",
	// And the treasures reach the case.
	"A sceptre, possibly that of ancient Egypt itself, is in the coffin.",
}

// The round-trip property, asserted over a route long enough for dynamic
// memory to have moved a long way from the story image.
//
// [TestRebuiltMachineMatchesOneKeptAlive] proves the same thing sixteen turns
// in, where a saved state is still nearly the story file. This proves it after
// seventy-three, with four treasures in the case, two villains disposed of, a
// rope tied to a railing, and the thief awake — the territory where a bug in
// how state is stored and handed back would actually have room to hide.
//
// Seventy-three turns sounds expensive and is not: rebuilding a machine copies
// only Zork's eleven kilobytes of dynamic memory, so the whole route runs in
// well under a tenth of a second and needs no [testing.Short] guard.
func TestLateGameRouteSurvivesTheRebuildCycle(t *testing.T) {
	entry := testEntry(t, "zork1")

	// The machine that is never put down.
	live, err := zmachine.New(entry.Story,
		zmachine.WithRandomSeed(lateGameSeed),
		zmachine.WithInstructionLimit(DefaultInstructionLimit),
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	// The machine that is rebuilt every turn, through the whole read-run-write
	// cycle a request handler will use.
	service := testService(t, WithRandomSeed(lateGameSeed))

	kept, err := live.Start(t.Context())
	if err != nil {
		t.Fatalf("live Start() error = %v", err)
	}
	session, rebuilt, err := service.NewGame(t.Context(), player, entry.ID)
	if err != nil {
		t.Fatalf("NewGame() error = %v", err)
	}
	assertSameTurn(t, "start", kept, rebuilt)

	var out bytes.Buffer
	out.WriteString(rebuilt.Output)

	for i, command := range lateGameRoute {
		kept, err = live.Run(t.Context(), command)
		if err != nil {
			t.Fatalf("turn %d: live Run(%q) error = %v", i+1, command, err)
		}

		turn, err := service.Play(t.Context(), player, session.ID, command)
		if err != nil {
			t.Fatalf("turn %d: Play(%q) error = %v", i+1, command, err)
		}
		session, rebuilt = turn.Session, turn.Result

		if got, want := session.Turn, i+1; got != want {
			t.Errorf("turn %d: session.Turn = %d, want %d", i+1, got, want)
		}

		assertSameTurn(t, command, kept, rebuilt)
		out.WriteString(rebuilt.Output)
	}

	assertLandmarks(t, out.String())

	// The route ends in the living room with the coffin, the sceptre, the
	// torch, the coins and the egg in the trophy case.
	status := rebuilt.StatusLine
	switch {
	case !status.Available:
		t.Error("the last turn reported no status line")
	case status.TimeGame:
		t.Error("StatusLine.TimeGame = true; Zork is a score game")
	default:
		if status.Name != "Living Room" {
			t.Errorf("the route ended in %q, want Living Room", status.Name)
		}
		if status.Score != 110 {
			t.Errorf("the route scored %d, want 110", status.Score)
		}
		if got, want := int(status.Turns), len(lateGameRoute); got != want {
			t.Errorf("the story counted %d moves, want %d", got, want)
		}
	}
}

// A route this long is only worth its runtime if it is the same route every
// time. If the seed stopped reaching the machines the transcript would drift
// and the landmarks above would start failing intermittently; this fails
// immediately and says why.
func TestLateGameRouteIsSeeded(t *testing.T) {
	first := lateGameTranscript(t, lateGameSeed)
	second := lateGameTranscript(t, lateGameSeed)

	if first != second {
		t.Error("two runs at the same seed produced different transcripts; the seed is not reaching the machine")
	}
	if lateGameTranscript(t, lateGameSeed+1) == first {
		t.Error("two seeds produced the same transcript; the route never touches the generator")
	}
}

// lateGameTranscript plays the route through the rebuild cycle and returns
// everything the story printed.
func lateGameTranscript(t *testing.T, seed uint64) string {
	t.Helper()

	service := testService(t, WithRandomSeed(seed))

	session, result, err := service.NewGame(t.Context(), player, "zork1")
	if err != nil {
		t.Fatalf("NewGame() error = %v", err)
	}

	var out bytes.Buffer
	out.WriteString(result.Output)

	for i, command := range lateGameRoute {
		turn, err := service.Play(t.Context(), player, session.ID, command)
		if err != nil {
			t.Fatalf("turn %d: Play(%q) error = %v", i+1, command, err)
		}
		session = turn.Session
		out.WriteString(turn.Result.Output)
	}

	return out.String()
}

// assertLandmarks walks the transcript once, requiring each landmark to appear
// after the one before it. Searching forward from the previous match is what
// makes the order part of the assertion rather than an accident of the text.
//
// The transcript is compared as the story printed it. The engine performs no
// wrapping, so there is no transport-specific formatting here to normalize
// away, and normalizing anything else would only hide a route that had stopped
// working.
func assertLandmarks(t *testing.T, transcript string) {
	t.Helper()

	at := 0
	for _, landmark := range lateGameLandmarks {
		found := strings.Index(transcript[at:], landmark)
		if found < 0 {
			if strings.Contains(transcript, landmark) {
				t.Fatalf("the route reached %q, but out of order", landmark)
			}
			t.Fatalf("the route never reached %q", landmark)
		}
		at += found + len(landmark)
	}
}
