package game

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/maloquacious/zmachine"
)

func testLibrary(t *testing.T) *Library {
	t.Helper()

	lib, err := Embedded()
	if err != nil {
		t.Fatalf("Embedded() error = %v", err)
	}
	return lib
}

func testEntry(t *testing.T, id string) *Entry {
	t.Helper()

	entry, ok := testLibrary(t).ByID(id)
	if !ok {
		t.Fatalf("ByID(%q) not found", id)
	}
	return entry
}

func TestStartOpensEachGame(t *testing.T) {
	tests := []struct {
		id     string
		banner string
		room   string
	}{
		{id: "zork1", banner: "ZORK I: The Great Underground Empire", room: "West of House"},
		{id: "zork2", banner: "ZORK II: The Wizard of Frobozz", room: "Inside the Barrow"},
		{id: "zork3", banner: "ZORK III: The Dungeon Master", room: "Endless Stair"},
	}

	runner := NewRunner()

	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			entry := testEntry(t, tt.id)

			result, err := runner.Start(t.Context(), entry)
			if err != nil {
				t.Fatalf("Start() error = %v", err)
			}

			if result.Status != zmachine.WaitingForInput {
				t.Errorf("Status = %v, want %v", result.Status, zmachine.WaitingForInput)
			}
			if len(result.State) == 0 {
				t.Error("State is empty; there is nothing to resume from")
			}
			if !strings.Contains(result.Output, tt.banner) {
				t.Errorf("Output does not contain banner %q", tt.banner)
			}

			// The story prints its own release and serial. They must agree
			// with what the library reports, or the catalog is describing a
			// different file than the one being executed.
			credit := fmt.Sprintf("Release %d / Serial number %s", entry.Release(), entry.Serial())
			if !strings.Contains(result.Output, credit) {
				t.Errorf("Output does not contain %q", credit)
			}

			if !result.StatusLine.Available {
				t.Fatal("StatusLine.Available = false; the other fields are meaningless")
			}
			if result.StatusLine.Name != tt.room {
				t.Errorf("StatusLine.Name = %q, want %q", result.StatusLine.Name, tt.room)
			}
			if result.StatusLine.TimeGame {
				t.Error("StatusLine.TimeGame = true; Zork is a score game")
			}
			if result.StatusLine.Score != 0 || result.StatusLine.Turns != 0 {
				t.Errorf("opening score/turns = %d/%d, want 0/0",
					result.StatusLine.Score, result.StatusLine.Turns)
			}
		})
	}
}

func TestStartHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	result, err := NewRunner().Start(ctx, testEntry(t, "zork1"))
	if err == nil {
		t.Fatal("Start() = nil error, want cancellation")
	}
	if got := Classify(err); got != FaultCanceled {
		t.Errorf("Classify() = %v, want %v", got, FaultCanceled)
	}
	if got := Classify(err); got.Retryable() {
		t.Error("a cancelled turn reports as retryable; nobody is waiting for it")
	}
	assertResultDiscarded(t, result)
}

func TestStartHonorsDeadline(t *testing.T) {
	ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()

	result, err := NewRunner().Start(ctx, testEntry(t, "zork1"))
	if err == nil {
		t.Fatal("Start() = nil error, want deadline")
	}
	if got := Classify(err); got != FaultTimeout {
		t.Errorf("Classify() = %v, want %v", got, FaultTimeout)
	}
	if got := Classify(err); !got.Retryable() {
		t.Error("a timed-out turn reports as not retryable")
	}
	assertResultDiscarded(t, result)
}

func TestStartHonorsInstructionLimit(t *testing.T) {
	runner := NewRunner(WithInstructionLimit(1))

	result, err := runner.Start(t.Context(), testEntry(t, "zork1"))
	if err == nil {
		t.Fatal("Start() = nil error, want the instruction limit")
	}
	if got := Classify(err); got != FaultExecutionLimit {
		t.Errorf("Classify() = %v, want %v", got, FaultExecutionLimit)
	}
	if got := Classify(err); got.Retryable() {
		t.Error("an instruction-limit failure reports as retryable; a retry stops in the same place")
	}
	assertResultDiscarded(t, result)
}

// The turn's own deadline must not override a caller's nearer one: an HTTP
// request that is already over should not buy five more seconds of execution.
func TestStartKeepsTheNearerDeadline(t *testing.T) {
	ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()

	runner := NewRunner(WithTurnTimeout(time.Minute))

	if _, err := runner.Start(ctx, testEntry(t, "zork1")); Classify(err) != FaultTimeout {
		t.Errorf("Classify() = %v, want %v", Classify(err), FaultTimeout)
	}
}

func TestStartWithoutTurnTimeout(t *testing.T) {
	runner := NewRunner(WithTurnTimeout(0))

	if _, err := runner.Start(t.Context(), testEntry(t, "zork1")); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
}

func TestStartIsReproducibleWithASeed(t *testing.T) {
	entry := testEntry(t, "zork1")

	first, err := NewRunner(WithRandomSeed(42)).Start(t.Context(), entry)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	second, err := NewRunner(WithRandomSeed(42)).Start(t.Context(), entry)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	if first.Output != second.Output {
		t.Error("same seed produced different output")
	}
	if !bytes.Equal(first.State, second.State) {
		t.Error("same seed produced different state")
	}
}

func TestStartRejectsNilStory(t *testing.T) {
	if _, err := NewRunner().Start(t.Context(), nil); err == nil {
		t.Fatal("Start(nil) = nil error, want failure")
	}
}

// Engine diagnostics must reach the logger the host supplied, and story output
// must not.
func TestStartLogsToTheSuppliedLogger(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	result, err := NewRunner(WithLogger(logger)).Start(t.Context(), testEntry(t, "zork1"))
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	if strings.Contains(buf.String(), "West of House") {
		t.Error("story output reached the logger")
	}
	if len(result.Output) == 0 {
		t.Error("Output is empty")
	}
}

// opening starts a new game and returns the state its first turn stored.
func opening(t *testing.T, runner *Runner, id string) (*Entry, []byte) {
	t.Helper()

	entry := testEntry(t, id)

	result, err := runner.Start(t.Context(), entry)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if len(result.State) == 0 {
		t.Fatal("Start() returned no state; there is nothing to run against")
	}
	return entry, result.State
}

// The turn cycle end to end: every command below runs in a machine built from
// nothing but the story and the bytes the previous turn returned.
func TestRunPlaysASequence(t *testing.T) {
	runner := NewRunner()
	entry, state := opening(t, runner, "zork1")

	turns := []struct {
		command string
		output  string
		room    string
		score   int16
	}{
		{command: "open mailbox", output: "reveals a leaflet", room: "West of House"},
		{command: "read leaflet", output: "WELCOME TO ZORK", room: "West of House"},
		{command: "north", output: "North of House", room: "North of House"},
		{command: "east", output: "Behind House", room: "Behind House"},
		{command: "open window", output: "the window", room: "Behind House"},
		{command: "enter window", output: "Kitchen", room: "Kitchen", score: 10},
	}

	for i, tt := range turns {
		result, err := runner.Run(t.Context(), entry, state, tt.command)
		if err != nil {
			t.Fatalf("Run(%q) error = %v", tt.command, err)
		}

		if result.Status != zmachine.WaitingForInput {
			t.Fatalf("Run(%q) Status = %v, want %v", tt.command, result.Status, zmachine.WaitingForInput)
		}
		if !strings.Contains(result.Output, tt.output) {
			t.Errorf("Run(%q) Output = %q, want it to contain %q", tt.command, result.Output, tt.output)
		}
		if result.StatusLine.Name != tt.room {
			t.Errorf("Run(%q) StatusLine.Name = %q, want %q", tt.command, result.StatusLine.Name, tt.room)
		}
		if result.StatusLine.Score != tt.score {
			t.Errorf("Run(%q) StatusLine.Score = %d, want %d", tt.command, result.StatusLine.Score, tt.score)
		}
		if got, want := result.StatusLine.Turns, int16(i+1); got != want {
			t.Errorf("Run(%q) StatusLine.Turns = %d, want %d", tt.command, got, want)
		}
		if len(result.State) == 0 {
			t.Fatalf("Run(%q) returned no state", tt.command)
		}

		state = result.State
	}
}

// A state only restores into the story it was written from. Getting this wrong
// would decode one game's memory against another's.
func TestRunRejectsStateFromAnotherStory(t *testing.T) {
	runner := NewRunner()
	_, state := opening(t, runner, "zork1")

	result, err := runner.Run(t.Context(), testEntry(t, "zork2"), state, "look")
	if err == nil {
		t.Fatal("Run() = nil error, want the state to be refused")
	}
	if got := Classify(err); got != FaultInvalidState {
		t.Errorf("Classify() = %v, want %v", got, FaultInvalidState)
	}
	assertResultDiscarded(t, result)
}

// Stored state is untrusted input even though this server wrote it: it may have
// been damaged in storage or in transit.
func TestRunRejectsDamagedState(t *testing.T) {
	runner := NewRunner()
	entry, state := opening(t, runner, "zork1")

	corrupt := make([]byte, len(state))
	copy(corrupt, state)
	for i := range corrupt {
		corrupt[i] ^= 0xff
	}

	truncated := state[:len(state)/2]

	tests := []struct {
		name  string
		state []byte
	}{
		{name: "not a saved state", state: []byte("this is not a saved game")},
		{name: "bits flipped", state: corrupt},
		{name: "truncated", state: truncated},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := runner.Run(t.Context(), entry, tt.state, "look")
			if err == nil {
				t.Fatal("Run() = nil error, want the state to be refused")
			}
			if got := Classify(err); got != FaultInvalidState {
				t.Errorf("Classify() = %v, want %v", got, FaultInvalidState)
			}
			assertResultDiscarded(t, result)
		})
	}
}

func TestRunRejectsBadArguments(t *testing.T) {
	runner := NewRunner()
	entry, state := opening(t, runner, "zork1")

	tests := []struct {
		name    string
		story   *Entry
		state   []byte
		command string
	}{
		{name: "nil story", story: nil, state: state, command: "look"},
		{name: "nil state", story: entry, state: nil, command: "look"},
		{name: "empty state", story: entry, state: []byte{}, command: "look"},
		{name: "overlong command", story: entry, state: state, command: strings.Repeat("x", MaxCommandBytes+1)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := runner.Run(t.Context(), tt.story, tt.state, tt.command)
			if err == nil {
				t.Fatal("Run() = nil error, want failure")
			}
			assertResultDiscarded(t, result)
		})
	}
}

// A command at exactly the limit is the player's to make. The story's own
// buffer will do whatever it does with it; that is the story's business.
func TestRunAcceptsACommandAtTheLimit(t *testing.T) {
	runner := NewRunner()
	entry, state := opening(t, runner, "zork1")

	if _, err := runner.Run(t.Context(), entry, state, strings.Repeat("x", MaxCommandBytes)); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestRunHonorsItsBounds(t *testing.T) {
	entry, state := opening(t, NewRunner(), "zork1")

	canceled, cancel := context.WithCancel(t.Context())
	cancel()

	expired, cancelExpired := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancelExpired()

	tests := []struct {
		name   string
		ctx    context.Context
		runner *Runner
		want   Fault
	}{
		{name: "cancellation", ctx: canceled, runner: NewRunner(), want: FaultCanceled},
		{name: "deadline", ctx: expired, runner: NewRunner(), want: FaultTimeout},
		{name: "caller's nearer deadline", ctx: expired, runner: NewRunner(WithTurnTimeout(time.Minute)), want: FaultTimeout},
		{name: "instruction limit", ctx: t.Context(), runner: NewRunner(WithInstructionLimit(1)), want: FaultExecutionLimit},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := tt.runner.Run(tt.ctx, entry, state, "look")
			if err == nil {
				t.Fatal("Run() = nil error, want failure")
			}
			if got := Classify(err); got != tt.want {
				t.Errorf("Classify() = %v, want %v", got, tt.want)
			}
			assertResultDiscarded(t, result)
		})
	}
}

// A story that ends itself returns no state, and a caller must not store the
// nil over a good one unless the session is deliberately being closed.
func TestRunHalts(t *testing.T) {
	runner := NewRunner()
	entry, state := opening(t, runner, "zork1")

	confirm, err := runner.Run(t.Context(), entry, state, "quit")
	if err != nil {
		t.Fatalf("Run(quit) error = %v", err)
	}
	if confirm.Status != zmachine.WaitingForInput {
		t.Fatalf("Run(quit) Status = %v, want the confirmation prompt", confirm.Status)
	}

	result, err := runner.Run(t.Context(), entry, confirm.State, "yes")
	if err != nil {
		t.Fatalf("Run(yes) error = %v", err)
	}
	if result.Status != zmachine.Halted {
		t.Fatalf("Run(yes) Status = %v, want %v", result.Status, zmachine.Halted)
	}
	if result.State != nil {
		t.Errorf("a halted story returned %d bytes of state", len(result.State))
	}
}

func TestRunIsReproducibleWithASeed(t *testing.T) {
	entry, state := opening(t, NewRunner(WithRandomSeed(1988)), "zork1")

	first, err := NewRunner(WithRandomSeed(1988)).Run(t.Context(), entry, state, "look")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	second, err := NewRunner(WithRandomSeed(1988)).Run(t.Context(), entry, state, "look")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if first.Output != second.Output {
		t.Errorf("same seed produced different output:\n%q\n%q", first.Output, second.Output)
	}
	if !bytes.Equal(first.State, second.State) {
		t.Error("same seed produced different state")
	}
}

// A failed turn must leave nothing worth storing. The engine returns a zero
// Result on these paths and the Runner passes that through unchanged, because a
// caller that stored a partial result would overwrite a good state with one that
// resumes nowhere.
func assertResultDiscarded(t *testing.T, result zmachine.Result) {
	t.Helper()

	if result.Output != "" {
		t.Errorf("failed turn returned output %q", result.Output)
	}
	if result.State != nil {
		t.Errorf("failed turn returned %d bytes of state", len(result.State))
	}
	if result.StatusLine.Available {
		t.Error("failed turn returned a status line")
	}
}
