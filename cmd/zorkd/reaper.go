package main

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// reapInterval is how often expired rows are collected.
//
// Hours rather than minutes, deliberately. Nothing is served from an expired
// row — a session that somebody comes back to is checked against the clock and
// deleted on the way past — so the sweep only collects rows nobody returns to,
// and no behaviour changes between the moment one expires and the moment it is
// removed. Sweeping oftener would buy nothing and would put a query on the
// database every few minutes for the life of the process.
const reapInterval = 6 * time.Hour

// A sweep is one kind of expired row the reaper collects: a name for the log
// line, and a function that removes what has expired and reports how many rows
// it removed.
//
// It reports a count and never the rows themselves. A session token, even an
// expired one, is not something to write down.
type sweep struct {
	name   string
	remove func(context.Context) (int, error)
}

// reap runs the sweeps on a ticker until ctx is cancelled.
//
// One goroutine and one interval covers everything that expires. Nothing here
// needs its own schedule, and separate reapers would only drift apart.
func reap(ctx context.Context, logger *slog.Logger, every time.Duration, sweeps ...sweep) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runSweeps(ctx, logger, sweeps...)
		}
	}
}

// runSweeps runs each sweep once.
//
// A sweep that fails is logged and the next tick still happens: one failed
// query must not quietly end periodic cleanup for the life of the process.
func runSweeps(ctx context.Context, logger *slog.Logger, sweeps ...sweep) {
	for _, s := range sweeps {
		removed, err := s.remove(ctx)
		switch {
		case errors.Is(err, context.Canceled):
			// The server is shutting down mid-sweep. Nothing was left in a bad
			// state, and the rows keep until the next one, so this is not a
			// failure worth reporting.
			return
		case err != nil:
			logger.Warn("sweeping expired "+s.name+" failed", "error", err)
		case removed > 0:
			logger.Info("swept expired "+s.name, "count", removed)
		}
	}
}
