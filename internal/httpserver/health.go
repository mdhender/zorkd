package httpserver

import (
	"context"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"
)

const (
	// HealthProbeInterval is the fixed database observation cadence.
	HealthProbeInterval = 30 * time.Second
	// HealthProbeTimeout is deliberately long enough to distinguish a stuck
	// pool from ordinary SQLite contention.
	HealthProbeTimeout = time.Minute
	healthStaleAfter   = 3 * HealthProbeInterval
)

// Pinger is the database operation consumed by Probe.
type Pinger interface {
	Ping(context.Context) error
}

// HealthStatus describes the freshness and outcome of a database observation.
type HealthStatus string

const (
	HealthUnknown HealthStatus = "unknown"
	HealthHealthy HealthStatus = "healthy"
	HealthFailing HealthStatus = "failing"
	HealthStale   HealthStatus = "stale"
)

// HealthSnapshot is one immutable, internally consistent probe observation.
type HealthSnapshot struct {
	Status                 HealthStatus
	RecordedAt             time.Time
	Duration               time.Duration
	LastSuccessfulDuration time.Duration
	ConsecutiveFailures    uint64
	TotalFailures          uint64
	LastFailureAt          time.Time
	LastFailureDuration    time.Duration
	LastError              error
}

type probeClock interface {
	Now() time.Time
	Ticks(time.Duration) (<-chan time.Time, func())
}

type realProbeClock struct{}

func (realProbeClock) Now() time.Time { return time.Now() }
func (realProbeClock) Ticks(d time.Duration) (<-chan time.Time, func()) {
	ticker := time.NewTicker(d)
	return ticker.C, ticker.Stop
}

// Probe periodically observes a Pinger and publishes whole snapshots with an
// atomic swap. Reading it never waits for a database connection.
type Probe struct {
	pinger Pinger
	logger *slog.Logger
	clock  probeClock
	state  atomic.Pointer[HealthSnapshot]
}

// NewProbe returns an unstarted database health probe.
func NewProbe(pinger Pinger, logger *slog.Logger) *Probe {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	p := &Probe{pinger: pinger, logger: logger, clock: realProbeClock{}}
	p.state.Store(&HealthSnapshot{Status: HealthUnknown})
	return p
}

// Snapshot returns the latest observation, deriving staleness at read time.
func (p *Probe) Snapshot() HealthSnapshot {
	current := *p.state.Load()
	if !current.RecordedAt.IsZero() && p.clock.Now().Sub(current.RecordedAt) > healthStaleAfter {
		current.Status = HealthStale
	}
	return current
}

type probeResult struct {
	finished time.Time
	duration time.Duration
	err      error
}

// Run probes immediately and then at a fixed cadence until ctx is cancelled.
// Ticks that arrive while Ping is running are intentionally discarded.
func (p *Probe) Run(ctx context.Context) {
	ticks, stopTicks := p.clock.Ticks(HealthProbeInterval)
	defer stopTicks()

	results := make(chan probeResult, 1)
	var cancel context.CancelFunc
	inFlight := false
	lastLogged := HealthUnknown
	start := func() {
		started := p.clock.Now()
		probeCtx, stop := context.WithTimeout(ctx, HealthProbeTimeout)
		cancel = stop
		inFlight = true
		go func() {
			err := p.pinger.Ping(probeCtx)
			finished := p.clock.Now()
			results <- probeResult{finished: finished, duration: finished.Sub(started), err: err}
		}()
	}
	logTransition := func(status HealthStatus) {
		if status != lastLogged {
			p.logger.InfoContext(ctx, "database health changed", "from", lastLogged, "to", status)
			lastLogged = status
		}
	}

	start()
	for {
		select {
		case <-ctx.Done():
			if cancel != nil {
				cancel()
			}
			if inFlight {
				<-results
			}
			return
		case <-ticks:
			logTransition(p.Snapshot().Status)
			if !inFlight {
				start()
			}
		case result := <-results:
			cancel()
			inFlight = false
			// Cancelling the server also cancels the in-flight Ping. If its
			// result wins the select race with ctx.Done, shutdown is still not
			// a database failure and must not change the recorded health.
			if ctx.Err() != nil {
				return
			}
			previous := p.state.Load()
			next := &HealthSnapshot{
				RecordedAt:             result.finished,
				Duration:               result.duration,
				LastSuccessfulDuration: previous.LastSuccessfulDuration,
				ConsecutiveFailures:    previous.ConsecutiveFailures,
				TotalFailures:          previous.TotalFailures,
				LastFailureAt:          previous.LastFailureAt,
				LastFailureDuration:    previous.LastFailureDuration,
			}
			if result.err == nil {
				next.Status = HealthHealthy
				next.ConsecutiveFailures = 0
				next.LastSuccessfulDuration = result.duration
			} else {
				next.Status = HealthFailing
				next.ConsecutiveFailures++
				next.TotalFailures++
				next.LastFailureAt = result.finished
				next.LastFailureDuration = result.duration
				next.LastError = result.err
			}
			p.state.Store(next)
			logTransition(next.Status)
		}
	}
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	if s.healthProbe.Snapshot().Status == HealthHealthy {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
		return
	}
	http.Error(w, "unavailable", http.StatusServiceUnavailable)
}
