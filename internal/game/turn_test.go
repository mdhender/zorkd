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
