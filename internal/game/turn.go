package game

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/maloquacious/zmachine"
)

// Defaults for a turn's two bounds. The deadline bounds wall-clock time; the
// instruction limit bounds work. A machine created without a limit still stops
// after ten million instructions per call, so neither bound can be removed —
// only tightened.
const (
	DefaultTurnTimeout      = 5 * time.Second
	DefaultInstructionLimit = 5_000_000
)

// A Runner executes turns.
//
// It holds the settings every turn shares and no per-session state, so one
// Runner serves every player. The machine that executes a turn is created
// inside the call and discarded before it returns.
type Runner struct {
	logger           *slog.Logger
	turnTimeout      time.Duration
	instructionLimit uint64
	seed             *uint64
}

// A RunnerOption configures a Runner.
type RunnerOption func(*Runner)

// NewRunner returns a Runner using the default bounds, discarding engine
// diagnostics, and seeding each machine's random generator unpredictably.
func NewRunner(opts ...RunnerOption) *Runner {
	r := &Runner{
		logger:           slog.New(slog.DiscardHandler),
		turnTimeout:      DefaultTurnTimeout,
		instructionLimit: DefaultInstructionLimit,
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// WithLogger sets the logger for engine diagnostics and execution faults.
//
// A nil logger is ignored. Story output never goes to the logger.
func WithLogger(logger *slog.Logger) RunnerOption {
	return func(r *Runner) {
		if logger != nil {
			r.logger = logger
		}
	}
}

// WithTurnTimeout bounds the wall-clock time one turn may take.
//
// A value of zero or less leaves the deadline entirely to the caller's context.
// Cancellation is checked periodically rather than after every instruction, so
// a deadline takes effect promptly but not instantly.
func WithTurnTimeout(d time.Duration) RunnerOption {
	return func(r *Runner) { r.turnTimeout = d }
}

// WithInstructionLimit bounds the number of instructions one turn may execute.
//
// A value of zero keeps [DefaultInstructionLimit]; the engine rejects a limit
// of zero, and there is no way to configure an unbounded machine.
func WithInstructionLimit(limit uint64) RunnerOption {
	return func(r *Runner) {
		if limit > 0 {
			r.instructionLimit = limit
		}
	}
}

// WithRandomSeed makes every machine the Runner creates start from the same
// predictable random state.
//
// This is for tests and for reproducing a reported turn. Leave it unset in
// production: an unseeded generator is the random state a new game is supposed
// to begin from, and a resumed session carries its generator state in the
// stored bytes either way.
func WithRandomSeed(seed uint64) RunnerOption {
	return func(r *Runner) { r.seed = &seed }
}

// Start begins a new game and runs to its first input boundary.
//
// It supplies no input, so the story prints its banner and opening room and
// then asks for a command. Store the returned State; from the next turn on
// there is no difference between it and the state Run returns.
//
// On failure the returned Result is unusable and the caller must write nothing
// to storage. Classify the error with [Classify].
func (r *Runner) Start(ctx context.Context, story *Entry) (zmachine.Result, error) {
	if story == nil {
		return zmachine.Result{}, errors.New("game: start: nil story")
	}

	ctx, cancel := r.bound(ctx)
	defer cancel()

	machine, err := r.newMachine(story)
	if err != nil {
		return zmachine.Result{}, fmt.Errorf("game: %s: new machine: %w", story.ID, err)
	}

	result, err := machine.Start(ctx)
	if err != nil {
		return zmachine.Result{}, r.fail(story, "start", err)
	}

	return result, nil
}

// newMachine builds the machine for one turn. Only dynamic memory is copied;
// the rest of the image is shared with the story.
func (r *Runner) newMachine(story *Entry) (*zmachine.Machine, error) {
	opts := []zmachine.Option{
		zmachine.WithLogger(r.logger),
		zmachine.WithInstructionLimit(r.instructionLimit),
	}
	if r.seed != nil {
		opts = append(opts, zmachine.WithRandomSeed(*r.seed))
	}

	return zmachine.New(story.Story, opts...)
}

// bound applies the Runner's deadline to the caller's context. A caller whose
// own deadline is nearer keeps it.
func (r *Runner) bound(ctx context.Context) (context.Context, context.CancelFunc) {
	if r.turnTimeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, r.turnTimeout)
}

// fail annotates a failed turn and logs the faults that indicate a broken story
// rather than a broken request.
func (r *Runner) fail(story *Entry, op string, err error) error {
	err = fmt.Errorf("game: %s: %s: %w", story.ID, op, err)

	if Classify(err) == FaultExecution {
		attrs := append([]any{
			slog.String("story", story.ID),
			slog.String("op", op),
		}, faultAttrs(err)...)

		r.logger.Error("execution fault", attrs...)
	}

	return err
}
