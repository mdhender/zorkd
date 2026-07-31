package game

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/maloquacious/zmachine"
)

func TestClassify(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want Fault
	}{
		{name: "success", err: nil, want: FaultNone},
		{name: "canceled", err: context.Canceled, want: FaultCanceled},
		{name: "deadline", err: context.DeadlineExceeded, want: FaultTimeout},
		{name: "execution limit", err: zmachine.ErrExecutionLimit, want: FaultExecutionLimit},
		{name: "invalid state", err: zmachine.ErrInvalidState, want: FaultInvalidState},
		{name: "invalid story", err: zmachine.ErrInvalidStory, want: FaultInvalidStory},
		{name: "execution fault", err: zmachine.ErrExecutionFault, want: FaultExecution},
		{name: "invalid opcode", err: zmachine.ErrInvalidOpcode, want: FaultExecution},
		{name: "memory access", err: zmachine.ErrMemoryAccess, want: FaultExecution},
		{name: "invalid text", err: zmachine.ErrInvalidText, want: FaultExecution},
		{name: "unrecognized", err: errors.New("something else"), want: FaultInternal},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Classify(tt.err); got != tt.want {
				t.Errorf("Classify(%v) = %v, want %v", tt.err, got, tt.want)
			}

			if tt.err == nil {
				return
			}

			// The Runner wraps whatever the engine returned, so classification
			// has to survive the wrapping it will always be given.
			wrapped := fmt.Errorf("game: zork1: start: %w", tt.err)
			if got := Classify(wrapped); got != tt.want {
				t.Errorf("Classify(wrapped) = %v, want %v", got, tt.want)
			}
		})
	}
}

// Cancellation wraps no engine sentinel, so it has to be tested for before
// them. This is the case that regresses if that order is ever rearranged: a
// disconnected client reported as an interpreter bug.
func TestClassifyChecksContextFirst(t *testing.T) {
	err := fmt.Errorf("game: zork1: run: %w", fmt.Errorf("turn abandoned: %w", context.Canceled))

	if got := Classify(err); got != FaultCanceled {
		t.Fatalf("Classify() = %v, want %v", got, FaultCanceled)
	}
	if errors.Is(err, zmachine.ErrExecutionFault) {
		t.Fatal("a cancelled turn wraps an engine sentinel; the ordering assumption no longer holds")
	}
}

func TestFaultRetryable(t *testing.T) {
	retryable := map[Fault]bool{FaultTimeout: true}

	for _, fault := range allFaults() {
		if got, want := fault.Retryable(), retryable[fault]; got != want {
			t.Errorf("%v.Retryable() = %v, want %v", fault, got, want)
		}
	}
}

func TestFaultString(t *testing.T) {
	seen := make(map[string]Fault)

	for _, fault := range allFaults() {
		name := fault.String()
		if name == "" || name == "unknown" {
			t.Errorf("Fault(%d).String() = %q", int(fault), name)
		}
		if prior, ok := seen[name]; ok {
			t.Errorf("Fault(%d) and Fault(%d) both stringify to %q", int(prior), int(fault), name)
		}
		seen[name] = fault
	}

	if got := Fault(-1).String(); got != "unknown" {
		t.Errorf("Fault(-1).String() = %q, want %q", got, "unknown")
	}
}

// faultAttrs must report the program counter, because an execution fault in
// late-game code is only diagnosable from the address it happened at.
func TestFaultAttrsCarryTheProgramCounter(t *testing.T) {
	err := fmt.Errorf("game: zork1: run: %w", &zmachine.ExecutionError{
		PC:     0x5601,
		Op:     "2OP:20 add",
		Detail: "division by zero",
		Err:    zmachine.ErrExecutionFault,
	})

	attrs := fmt.Sprint(faultAttrs(err)...)

	for _, want := range []string{"0x5601", "2OP:20 add", "division by zero", "execution"} {
		if !strings.Contains(attrs, want) {
			t.Errorf("faultAttrs() = %s, missing %q", attrs, want)
		}
	}
}

func allFaults() []Fault {
	return []Fault{
		FaultNone,
		FaultCanceled,
		FaultTimeout,
		FaultExecutionLimit,
		FaultInvalidState,
		FaultInvalidStory,
		FaultExecution,
		FaultInternal,
	}
}
