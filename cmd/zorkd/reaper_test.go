package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// tickInterval keeps the reaper tests fast. Nothing here asserts when a tick
// happened, only that ticks keep coming and then stop, so the interval only
// has to be short enough not to slow the suite down.
const tickInterval = time.Millisecond

// waitTimeout bounds a test that is waiting for something that should already
// have happened. It is a failure deadline, not a delay: a passing test never
// waits this long.
const waitTimeout = 10 * time.Second

// discardLogger is a logger for the tests that care about what the reaper did
// rather than what it wrote.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// counting returns a sweep that records each call and reports it on ticks,
// which never blocks the reaper: a test that stops receiving must not wedge
// the goroutine it is about to cancel.
func counting(name string, calls *atomic.Int64, ticks chan<- struct{}, err error) sweep {
	return sweep{
		name: name,
		remove: func(context.Context) (int, error) {
			calls.Add(1)
			select {
			case ticks <- struct{}{}:
			default:
			}
			if err != nil {
				return 0, err
			}
			return 1, nil
		},
	}
}

// awaitTick fails the test if the reaper does not sweep.
func awaitTick(t *testing.T, ticks <-chan struct{}, n int) {
	t.Helper()
	for i := range n {
		select {
		case <-ticks:
		case <-time.After(waitTimeout):
			t.Fatalf("no sweep after %v (tick %d of %d)", waitTimeout, i+1, n)
		}
	}
}

// awaitStop fails the test if the reaper's goroutine is still running.
func awaitStop(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(waitTimeout):
		t.Fatalf("reap did not return %v after the context was cancelled", waitTimeout)
	}
}

// start runs reap in its own goroutine and returns a channel closed when it
// returns, so every test can see the goroutine end rather than assume it did.
func start(ctx context.Context, logger *slog.Logger, sweeps ...sweep) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		reap(ctx, logger, tickInterval, sweeps...)
	}()
	return done
}

func TestReapSweepsOnEachTick(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls atomic.Int64
	ticks := make(chan struct{}, 1)

	done := start(ctx, discardLogger(), counting("sessions", &calls, ticks, nil))
	awaitTick(t, ticks, 3)

	cancel()
	awaitStop(t, done)

	if got := calls.Load(); got < 3 {
		t.Errorf("swept %d times, want at least 3", got)
	}
}

func TestReapSweepsEverythingOnOneTick(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var first, second atomic.Int64
	firstTicks := make(chan struct{}, 1)
	secondTicks := make(chan struct{}, 1)

	done := start(ctx, discardLogger(),
		counting("sessions", &first, firstTicks, nil),
		counting("widgets", &second, secondTicks, nil),
	)
	awaitTick(t, firstTicks, 1)
	awaitTick(t, secondTicks, 1)

	cancel()
	awaitStop(t, done)
}

// TestReapStopsWhenCancelled is the goroutine-leak test: the reaper is tied to
// the context the signal handler cancels, so it has to end with the server.
func TestReapStopsWhenCancelled(t *testing.T) {
	t.Run("before the first tick", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		var calls atomic.Int64

		cancel()
		done := start(ctx, discardLogger(), counting("sessions", &calls, nil, nil))
		awaitStop(t, done)
	})

	t.Run("after sweeping", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		var calls atomic.Int64
		ticks := make(chan struct{}, 1)

		done := start(ctx, discardLogger(), counting("sessions", &calls, ticks, nil))
		awaitTick(t, ticks, 1)

		cancel()
		awaitStop(t, done)

		// The goroutine has returned, so nothing can sweep again.
		if calls.Load() == 0 {
			t.Error("returned without ever sweeping")
		}
	})
}

// TestReapContinuesAfterFailure is the important one: a single failed query
// must not silently end periodic cleanup for the life of the process.
func TestReapContinuesAfterFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls atomic.Int64
	ticks := make(chan struct{}, 1)
	var logged bytes.Buffer

	// The buffer is written from the reaper's goroutine, so it is only read
	// after that goroutine has returned.
	logger := slog.New(slog.NewTextHandler(&logged, nil))
	done := start(ctx, logger, counting("sessions", &calls, ticks, errors.New("database is locked")))
	awaitTick(t, ticks, 3)

	cancel()
	awaitStop(t, done)

	if !strings.Contains(logged.String(), "sweeping expired sessions failed") {
		t.Errorf("the failure was not logged: %q", logged.String())
	}
}

func TestRunSweeps(t *testing.T) {
	tests := []struct {
		name     string
		removed  int
		err      error
		want     []string
		unwanted []string
	}{
		{
			name:    "removed rows are counted",
			removed: 7,
			want:    []string{"level=INFO", "swept expired sessions", "count=7"},
		},
		{
			// A quiet sweep is the ordinary case and says nothing, so a log
			// line about sessions means sessions actually went away.
			name:     "nothing to remove is not reported",
			removed:  0,
			unwanted: []string{"sessions"},
		},
		{
			name: "a failure is a warning",
			err:  errors.New("database is locked"),
			want: []string{"level=WARN", "sweeping expired sessions failed", "database is locked"},
		},
		{
			// Shutting down mid-sweep is not a failure: nothing was left in a
			// bad state, and the rows keep until the next start.
			name:     "cancellation is not a failure",
			err:      context.Canceled,
			unwanted: []string{"level=WARN", "level=ERROR"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logged bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&logged, nil))

			runSweeps(context.Background(), logger, sweep{
				name: "sessions",
				remove: func(context.Context) (int, error) {
					return tt.removed, tt.err
				},
			})

			for _, want := range tt.want {
				if !strings.Contains(logged.String(), want) {
					t.Errorf("log %q does not contain %q", logged.String(), want)
				}
			}
			for _, unwanted := range tt.unwanted {
				if strings.Contains(logged.String(), unwanted) {
					t.Errorf("log %q contains %q", logged.String(), unwanted)
				}
			}
		})
	}
}

// TestRunSweepsRunsEverySweep checks that a sweep which fails does not stop
// the ones after it on the same tick.
func TestRunSweepsRunsEverySweep(t *testing.T) {
	var second atomic.Int64

	runSweeps(context.Background(), discardLogger(),
		sweep{
			name:   "first",
			remove: func(context.Context) (int, error) { return 0, errors.New("database is locked") },
		},
		sweep{
			name: "second",
			remove: func(context.Context) (int, error) {
				second.Add(1)
				return 0, nil
			},
		},
	)

	if got := second.Load(); got != 1 {
		t.Errorf("the second sweep ran %d times, want 1", got)
	}
}
