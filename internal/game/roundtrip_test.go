package game

import (
	"bytes"
	"testing"

	"github.com/maloquacious/zmachine"
)

// route walks out of the house, down into the cellar and into the Troll Room,
// where the story rolls dice. A route that never touches the generator would
// prove nothing about carrying its state across a rebuild.
var route = []string{
	"open mailbox",
	"north",
	"east",
	"open window",
	"enter window",
	"west",
	"take lamp",
	"take sword",
	"move rug",
	"open trap door",
	"turn on lamp",
	"down",
	"north",
	"attack troll with sword",
	"attack troll with sword",
	"attack troll with sword",
}

// The property this whole application rests on: a machine rebuilt from the
// story and the previous turn's stored state continues exactly as a machine
// that was never put down.
//
// The engine guarantees it. This asserts it at our layer, so that a regression
// in how state is stored and handed back is caught here rather than by a player
// whose game quietly diverges.
func TestRebuiltMachineMatchesOneKeptAlive(t *testing.T) {
	const seed = 1988

	entry := testEntry(t, "zork1")

	// The machine that is never put down.
	live, err := zmachine.New(entry.Story,
		zmachine.WithRandomSeed(seed),
		zmachine.WithInstructionLimit(DefaultInstructionLimit),
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	// The machine that is rebuilt every turn, through the whole read-run-write
	// cycle a request handler will use.
	service := testService(t, WithRandomSeed(seed))

	kept, err := live.Start(t.Context())
	if err != nil {
		t.Fatalf("live Start() error = %v", err)
	}
	session, rebuilt, err := service.NewGame(t.Context(), player, entry.ID)
	if err != nil {
		t.Fatalf("NewGame() error = %v", err)
	}
	assertSameTurn(t, "start", kept, rebuilt)

	for i, command := range route {
		kept, err = live.Run(t.Context(), command)
		if err != nil {
			t.Fatalf("live Run(%q) error = %v", command, err)
		}

		turn, err := service.Play(t.Context(), player, session.ID, command)
		if err != nil {
			t.Fatalf("Play(%q) error = %v", command, err)
		}
		session, rebuilt = turn.Session, turn.Result

		if got, want := session.Turn, i+1; got != want {
			t.Errorf("session.Turn = %d, want %d", got, want)
		}

		assertSameTurn(t, command, kept, rebuilt)
	}
}

// The route above is only a proof about randomness if it actually rolls dice.
// Two seeds must not produce the same transcript.
func TestRouteExercisesTheGenerator(t *testing.T) {
	first := transcript(t, 1988)
	second := transcript(t, 2026)

	if first == second {
		t.Error("two seeds produced the same transcript; the route never touches the generator")
	}
}

// transcript plays the route with a fixed seed and returns everything printed.
func transcript(t *testing.T, seed uint64) string {
	t.Helper()

	service := testService(t, WithRandomSeed(seed))

	session, result, err := service.NewGame(t.Context(), player, "zork1")
	if err != nil {
		t.Fatalf("NewGame() error = %v", err)
	}

	var out bytes.Buffer
	out.WriteString(result.Output)

	for _, command := range route {
		turn, err := service.Play(t.Context(), player, session.ID, command)
		if err != nil {
			t.Fatalf("Play(%q) error = %v", command, err)
		}
		session = turn.Session
		out.WriteString(turn.Result.Output)
	}

	return out.String()
}

func testService(t *testing.T, opts ...RunnerOption) *Service {
	t.Helper()

	service, err := NewService(testLibrary(t), NewRunner(opts...), NewMemoryStore())
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return service
}

// assertSameTurn compares everything a player and a store would see.
func assertSameTurn(t *testing.T, command string, kept, rebuilt zmachine.Result) {
	t.Helper()

	if kept.Output != rebuilt.Output {
		t.Errorf("after %q the transcripts diverged:\nkept alive: %q\nrebuilt:    %q",
			command, kept.Output, rebuilt.Output)
	}
	if kept.UpperWindow != rebuilt.UpperWindow {
		t.Errorf("after %q the upper windows diverged", command)
	}
	if kept.StatusLine != rebuilt.StatusLine {
		t.Errorf("after %q the status lines diverged: %+v and %+v",
			command, kept.StatusLine, rebuilt.StatusLine)
	}
	if kept.Status != rebuilt.Status {
		t.Errorf("after %q the statuses diverged: %v and %v", command, kept.Status, rebuilt.Status)
	}
	if !bytes.Equal(kept.State, rebuilt.State) {
		t.Errorf("after %q the stored states diverged: %d and %d bytes",
			command, len(kept.State), len(rebuilt.State))
	}
}
