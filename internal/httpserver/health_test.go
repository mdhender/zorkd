package httpserver

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeProbeClock struct {
	mu    sync.Mutex
	now   time.Time
	ticks chan time.Time
}

func newFakeProbeClock() *fakeProbeClock {
	return &fakeProbeClock{now: time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC), ticks: make(chan time.Time, 16)}
}

func (c *fakeProbeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeProbeClock) Ticks(time.Duration) (<-chan time.Time, func()) {
	return c.ticks, func() {}
}

func (c *fakeProbeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

type pingReply struct{ err error }

type controlledPinger struct {
	started chan struct{}
	replies chan pingReply
	active  int
	max     int
	mu      sync.Mutex
}

func newControlledPinger() *controlledPinger {
	return &controlledPinger{started: make(chan struct{}, 16), replies: make(chan pingReply, 16)}
}

func (p *controlledPinger) Ping(ctx context.Context) error {
	p.mu.Lock()
	p.active++
	if p.active > p.max {
		p.max = p.active
	}
	p.mu.Unlock()
	p.started <- struct{}{}
	var err error
	select {
	case reply := <-p.replies:
		err = reply.err
	case <-ctx.Done():
		err = ctx.Err()
	}
	p.mu.Lock()
	p.active--
	p.mu.Unlock()
	return err
}

func startTestProbe(t *testing.T) (*Probe, *fakeProbeClock, *controlledPinger, context.CancelFunc, <-chan struct{}) {
	t.Helper()
	clock := newFakeProbeClock()
	pinger := newControlledPinger()
	probe := NewProbe(pinger, nil)
	probe.clock = clock
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { probe.Run(ctx); close(done) }()
	<-pinger.started
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return probe, clock, pinger, cancel, done
}

func awaitStatus(t *testing.T, probe *Probe, want HealthStatus) HealthSnapshot {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		snapshot := probe.Snapshot()
		if snapshot.Status == want {
			return snapshot
		}
		select {
		case <-deadline:
			t.Fatalf("status = %s, want %s", snapshot.Status, want)
		default:
		}
	}
}

func TestProbeStartsUnknownAndRecordsSuccess(t *testing.T) {
	probe, clock, pinger, _, _ := startTestProbe(t)
	if got := probe.Snapshot().Status; got != HealthUnknown {
		t.Fatalf("initial status = %s, want unknown", got)
	}
	clock.advance(25 * time.Millisecond)
	pinger.replies <- pingReply{}
	snapshot := awaitStatus(t, probe, HealthHealthy)
	if !snapshot.RecordedAt.Equal(clock.Now()) || snapshot.Duration != 25*time.Millisecond || snapshot.LastSuccessfulDuration != 25*time.Millisecond {
		t.Errorf("success snapshot = %+v", snapshot)
	}
}

func TestProbeFailureCountersSurviveFlapping(t *testing.T) {
	probe, clock, pinger, _, _ := startTestProbe(t)
	failure := errors.New("stuck")
	clock.advance(time.Second)
	pinger.replies <- pingReply{err: failure}
	first := awaitStatus(t, probe, HealthFailing)
	if first.ConsecutiveFailures != 1 || first.TotalFailures != 1 || !errors.Is(first.LastError, failure) || first.LastFailureDuration != time.Second {
		t.Fatalf("first failure = %+v", first)
	}

	clock.ticks <- clock.Now()
	<-pinger.started
	clock.advance(2 * time.Second)
	pinger.replies <- pingReply{}
	healthy := awaitStatus(t, probe, HealthHealthy)
	if healthy.ConsecutiveFailures != 0 || healthy.TotalFailures != 1 || !healthy.LastFailureAt.Equal(first.LastFailureAt) {
		t.Fatalf("success after failure = %+v", healthy)
	}

	clock.ticks <- clock.Now()
	<-pinger.started
	clock.advance(3 * time.Second)
	pinger.replies <- pingReply{err: failure}
	second := awaitStatus(t, probe, HealthFailing)
	if second.ConsecutiveFailures != 1 || second.TotalFailures != 2 || second.Duration != 3*time.Second {
		t.Fatalf("second failure = %+v", second)
	}
}

func TestProbeDerivesStaleHealthy(t *testing.T) {
	probe, clock, pinger, _, _ := startTestProbe(t)
	pinger.replies <- pingReply{}
	awaitStatus(t, probe, HealthHealthy)
	clock.advance(healthStaleAfter + time.Nanosecond)
	if got := probe.Snapshot().Status; got != HealthStale {
		t.Fatalf("old healthy status = %s, want stale", got)
	}
}

func TestProbeSkipsTicksAndReadersDoNotBlock(t *testing.T) {
	probe, clock, pinger, _, _ := startTestProbe(t)
	for range 5 {
		clock.ticks <- clock.Now()
	}
	read := make(chan HealthSnapshot, 1)
	go func() { read <- probe.Snapshot() }()
	select {
	case <-read:
	case <-time.After(time.Second):
		t.Fatal("Snapshot blocked on Ping")
	}
	select {
	case <-pinger.started:
		t.Fatal("an overlapping probe started")
	default:
	}
	pinger.replies <- pingReply{}
	awaitStatus(t, probe, HealthHealthy)
	pinger.mu.Lock()
	max := pinger.max
	pinger.mu.Unlock()
	if max != 1 {
		t.Fatalf("maximum concurrent pings = %d, want 1", max)
	}
}

func TestProbeCancellationReturns(t *testing.T) {
	_, _, _, cancel, done := startTestProbe(t)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return after cancellation")
	}
}

func TestProbeConcurrentReaders(t *testing.T) {
	probe, clock, pinger, _, _ := startTestProbe(t)
	var readers sync.WaitGroup
	for range 20 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for range 1000 {
				_ = probe.Snapshot()
			}
		}()
	}
	for i := range 20 {
		want := HealthHealthy
		reply := pingReply{}
		if i%2 != 0 {
			want = HealthFailing
			reply.err = errors.New("test failure")
		}
		pinger.replies <- reply
		awaitStatus(t, probe, want)
		clock.ticks <- clock.Now()
		<-pinger.started
	}
	pinger.replies <- pingReply{}
	readers.Wait()
}

func TestHealthEndpointStatesAndPrivacy(t *testing.T) {
	probe := NewProbe(testPinger{}, nil)
	server := &Server{healthProbe: probe, logger: slog.New(slog.DiscardHandler)}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", server.health)
	handler := server.logRequests(mux)
	now := time.Now()
	tests := []struct {
		name   string
		state  HealthSnapshot
		status int
	}{
		{"unknown", HealthSnapshot{Status: HealthUnknown}, http.StatusServiceUnavailable},
		{"healthy", HealthSnapshot{Status: HealthHealthy, RecordedAt: now}, http.StatusOK},
		{"failing", HealthSnapshot{Status: HealthFailing, RecordedAt: now, ConsecutiveFailures: 42, LastError: errors.New("secret database path")}, http.StatusServiceUnavailable},
		{"stale", HealthSnapshot{Status: HealthHealthy, RecordedAt: now.Add(-healthStaleAfter - time.Second)}, http.StatusServiceUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			probe.state.Store(&test.state)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d", response.Code, test.status)
			}
			body := response.Body.String()
			for _, private := range []string{"secret", "database", "42", string(test.state.Status)} {
				if strings.Contains(body, private) {
					t.Errorf("public body %q contains %q", body, private)
				}
			}
		})
	}
}

func TestHealthEndpointBypassesAuthentication(t *testing.T) {
	response := newTestClient(t).otherBrowser().do(httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("unauthenticated status = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
	if location := response.Header().Get("Location"); location != "" {
		t.Fatalf("unauthenticated health check redirected to %q", location)
	}
}

// logRequests closes over Server, so the endpoint's logging property is more
// directly tested at that middleware boundary than by unpacking Handler.
func TestHealthEndpointIsNotRequestLogged(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&output, nil))
	probe := NewProbe(testPinger{}, nil)
	probe.state.Store(&HealthSnapshot{Status: HealthHealthy, RecordedAt: time.Now()})
	server := &Server{healthProbe: probe, logger: logger}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", server.health)
	server.logRequests(mux).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if output.Len() != 0 {
		t.Fatalf("health request was logged: %s", output.String())
	}
}
