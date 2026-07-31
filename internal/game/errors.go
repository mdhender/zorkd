package game

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/maloquacious/zmachine"
)

// Fault classifies why a turn failed.
//
// The classification exists because the responses differ: one of these means
// the player is still waiting and should be told, one means nobody is waiting
// at all, and one means the story itself is broken. What they have in common is
// that the Result is unusable and nothing may be written to storage.
type Fault int

const (
	// FaultNone means the turn succeeded.
	FaultNone Fault = iota

	// FaultCanceled means the client went away. There is nobody to answer.
	FaultCanceled

	// FaultTimeout means the turn ran out of wall-clock time. The player is
	// still there and their command was not played.
	FaultTimeout

	// FaultExecutionLimit means the story exceeded its instruction budget.
	FaultExecutionLimit

	// FaultInvalidState means the stored bytes are damaged or belong to a
	// different story file. Check the stored story key before assuming
	// corruption.
	FaultInvalidState

	// FaultInvalidStory means the story image is not a usable Version 3
	// story. This is a deployment defect, not a session defect.
	FaultInvalidStory

	// FaultExecution means the story hit something the Z-machine defines no
	// result for, or an instruction that does not exist. Log it with the
	// program counter and report it upstream.
	FaultExecution

	// FaultInternal means the failure came from this application rather than
	// from the engine, the story or the request.
	FaultInternal
)

// Classify reports why a call to the engine failed.
//
// Cancellation is tested first and deliberately so: it returns the context's
// own error, unwrapped, and wraps no engine sentinel, so a check that looks for
// engine sentinels first would report a disconnected client as an interpreter
// bug.
func Classify(err error) Fault {
	if err == nil {
		return FaultNone
	}

	switch {
	case errors.Is(err, context.Canceled):
		return FaultCanceled
	case errors.Is(err, context.DeadlineExceeded):
		return FaultTimeout
	case errors.Is(err, zmachine.ErrExecutionLimit):
		return FaultExecutionLimit
	case errors.Is(err, zmachine.ErrInvalidState):
		return FaultInvalidState
	case errors.Is(err, zmachine.ErrInvalidStory):
		return FaultInvalidStory
	case errors.Is(err, zmachine.ErrExecutionFault),
		errors.Is(err, zmachine.ErrInvalidOpcode),
		errors.Is(err, zmachine.ErrMemoryAccess),
		errors.Is(err, zmachine.ErrInvalidText):
		return FaultExecution
	}

	return FaultInternal
}

// Retryable reports whether replaying the same command against the same stored
// state is worth doing.
//
// Only a timeout is. A retry is safe in every case — nothing was written, and
// the engine has no side effects outside the machine that was thrown away — but
// safe is not the same as useful: an instruction-limit failure re-executes the
// same instructions and stops in the same place, and a cancelled request has
// nobody left waiting for an answer.
//
// A retry needs a fresh context. Retrying with the one that expired fails
// immediately.
func (f Fault) Retryable() bool { return f == FaultTimeout }

// String returns the fault's name, suitable for a log field.
func (f Fault) String() string {
	switch f {
	case FaultNone:
		return "none"
	case FaultCanceled:
		return "canceled"
	case FaultTimeout:
		return "timeout"
	case FaultExecutionLimit:
		return "execution_limit"
	case FaultInvalidState:
		return "invalid_state"
	case FaultInvalidStory:
		return "invalid_story"
	case FaultExecution:
		return "execution"
	case FaultInternal:
		return "internal"
	}
	return "unknown"
}

// faultAttrs returns the log fields that locate a fault inside the story.
//
// The program counter is the point of logging these at all: no test finishes a
// game, so the late-game code paths have never executed, and a fault there is
// only diagnosable if the address it happened at was recorded when it happened.
func faultAttrs(err error) []any {
	attrs := []any{slog.String("fault", Classify(err).String())}

	if exec, ok := errors.AsType[*zmachine.ExecutionError](err); ok {
		attrs = append(attrs,
			slog.String("pc", hex32(exec.PC)),
			slog.String("op", exec.Op),
			slog.String("detail", exec.Detail),
		)
	}

	if decode, ok := errors.AsType[*zmachine.DecodeError](err); ok {
		attrs = append(attrs,
			slog.String("addr", hex32(decode.Addr)),
			slog.Uint64("opcode", uint64(decode.Opcode)),
			slog.String("detail", decode.Detail),
		)
	}

	if mem, ok := errors.AsType[*zmachine.MemoryError](err); ok {
		attrs = append(attrs,
			slog.String("addr", hex32(mem.Addr)),
			slog.String("mem_op", mem.Op.String()),
			slog.String("region", mem.Region.String()),
		)
	}

	return attrs
}

// hex32 formats an address the way the engine's own errors write it, so a log
// line and the error text it came from can be compared by eye.
func hex32(v uint32) string { return fmt.Sprintf("%#x", v) }
