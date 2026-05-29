package active

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/sanchey92/flowgate/internal/domain/model"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

var errProbe = errors.New("probe failed")

// scriptChecker returns one scripted result per Check call: true => healthy
// (nil), false => failure. It is the deterministic stand-in for a real probe.
type scriptChecker struct {
	results []bool
	i       int
}

func (c *scriptChecker) Check(context.Context, *model.Backend) error {
	if c.i >= len(c.results) {
		panic("scriptChecker: Check called more times than scripted results")
	}
	res := c.results[c.i]
	c.i++
	if res {
		return nil
	}
	return errProbe
}

// alwaysFail is a checker whose every probe fails — used by the concurrent test.
type alwaysFail struct{}

func (alwaysFail) Check(context.Context, *model.Backend) error { return errProbe }

// eventRecorder captures StatusEvents delivered through the onChange hook.
type eventRecorder struct {
	mu     sync.Mutex
	events []model.StatusEvent
}

func (r *eventRecorder) listener(e *model.StatusEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, *e)
}

func (r *eventRecorder) snapshot() []model.StatusEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]model.StatusEvent(nil), r.events...)
}

type transition struct {
	from, to model.BackendStatus
}

func (r *eventRecorder) transitions() []transition {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.events) == 0 {
		return nil
	}
	out := make([]transition, len(r.events))
	for i, e := range r.events {
		out[i] = transition{from: e.From, to: e.To}
	}
	return out
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newBackend(t *testing.T, addr string, st model.BackendStatus) *model.Backend {
	t.Helper()
	b := model.NewBackend(addr, 1, 0)
	switch st {
	case model.StatusHealthy:
		// zero value — nothing to do
	case model.StatusUnhealthy:
		require.True(t, b.Transition(model.StatusHealthy, model.StatusUnhealthy))
	case model.StatusDraining:
		require.True(t, b.Drain())
	}
	require.Equal(t, st, b.Status())
	return b
}

// TestScheduler_probe drives the threshold/transition logic one probe at a time,
// sharing the same local counters runBackend would, so the assertions are fully
// deterministic and free of any timer/goroutine flakiness.
func TestScheduler_probe(t *testing.T) {
	const (
		F = false // failed probe
		S = true  // successful probe
	)

	tests := []struct {
		name            string
		unhealthy       int
		healthy         int
		initial         model.BackendStatus
		results         []bool
		wantStatus      model.BackendStatus
		wantTransitions []transition
	}{
		{
			name:       "healthy: failures below threshold stay healthy",
			unhealthy:  3,
			healthy:    2,
			initial:    model.StatusHealthy,
			results:    []bool{F, F},
			wantStatus: model.StatusHealthy,
		},
		{
			name:       "healthy: reaching unhealthy threshold transitions",
			unhealthy:  3,
			healthy:    2,
			initial:    model.StatusHealthy,
			results:    []bool{F, F, F},
			wantStatus: model.StatusUnhealthy,
			wantTransitions: []transition{
				{from: model.StatusHealthy, to: model.StatusUnhealthy},
			},
		},
		{
			name:       "healthy: a success resets the failure streak",
			unhealthy:  3,
			healthy:    2,
			initial:    model.StatusHealthy,
			results:    []bool{F, F, S, F, F},
			wantStatus: model.StatusHealthy,
		},
		{
			name:       "healthy: threshold of one transitions immediately",
			unhealthy:  1,
			healthy:    1,
			initial:    model.StatusHealthy,
			results:    []bool{F},
			wantStatus: model.StatusUnhealthy,
			wantTransitions: []transition{
				{from: model.StatusHealthy, to: model.StatusUnhealthy},
			},
		},
		{
			name:       "healthy: no duplicate event once unhealthy",
			unhealthy:  2,
			healthy:    2,
			initial:    model.StatusHealthy,
			results:    []bool{F, F, F, F},
			wantStatus: model.StatusUnhealthy,
			wantTransitions: []transition{
				{from: model.StatusHealthy, to: model.StatusUnhealthy},
			},
		},
		{
			name:       "unhealthy: successes below threshold stay unhealthy",
			unhealthy:  3,
			healthy:    2,
			initial:    model.StatusUnhealthy,
			results:    []bool{S},
			wantStatus: model.StatusUnhealthy,
		},
		{
			name:       "unhealthy: reaching healthy threshold recovers",
			unhealthy:  3,
			healthy:    2,
			initial:    model.StatusUnhealthy,
			results:    []bool{S, S},
			wantStatus: model.StatusHealthy,
			wantTransitions: []transition{
				{from: model.StatusUnhealthy, to: model.StatusHealthy},
			},
		},
		{
			name:       "unhealthy: a failure resets the success streak",
			unhealthy:  3,
			healthy:    2,
			initial:    model.StatusUnhealthy,
			results:    []bool{S, F, S},
			wantStatus: model.StatusUnhealthy,
		},
		{
			name:       "draining is never revived by successes",
			unhealthy:  3,
			healthy:    2,
			initial:    model.StatusDraining,
			results:    []bool{S, S, S},
			wantStatus: model.StatusDraining,
		},
		{
			name:       "draining is never failed by failures",
			unhealthy:  3,
			healthy:    2,
			initial:    model.StatusDraining,
			results:    []bool{F, F, F, F},
			wantStatus: model.StatusDraining,
		},
		{
			name:       "full flap: healthy -> unhealthy -> healthy",
			unhealthy:  3,
			healthy:    2,
			initial:    model.StatusHealthy,
			results:    []bool{F, F, F, S, S},
			wantStatus: model.StatusHealthy,
			wantTransitions: []transition{
				{from: model.StatusHealthy, to: model.StatusUnhealthy},
				{from: model.StatusUnhealthy, to: model.StatusHealthy},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &eventRecorder{}
			cfg := &Config{
				UnhealthyThreshold: tt.unhealthy,
				HealthyThreshold:   tt.healthy,
			}
			s, err := NewScheduler(&scriptChecker{results: tt.results}, nil, cfg, rec.listener, testLogger())
			require.NoError(t, err)

			b := newBackend(t, "10.0.0.1:80", tt.initial)

			var cs, cf int
			for range tt.results {
				s.probe(context.Background(), b, &cs, &cf)
			}

			require.Equal(t, tt.wantStatus, b.Status(), "final status")
			require.Equal(t, tt.wantTransitions, rec.transitions(), "recorded transitions")
		})
	}
}

// TestScheduler_lifecycle exercises Start/Shutdown ordering and idempotency.
// Backends are nil so Start spawns no goroutines — this keeps the table about
// state transitions, not timing.
func TestScheduler_lifecycle(t *testing.T) {
	const (
		opStart    = "start"
		opShutdown = "shutdown"
	)

	tests := []struct {
		name    string
		ops     []string
		wantErr string // substring expected from the LAST op; "" means no error
	}{
		{name: "shutdown without start is a no-op", ops: []string{opShutdown}, wantErr: ""},
		{name: "start then shutdown", ops: []string{opStart, opShutdown}, wantErr: ""},
		{name: "double start is rejected", ops: []string{opStart, opStart}, wantErr: "already started"},
		{name: "double shutdown is idempotent", ops: []string{opStart, opShutdown, opShutdown}, wantErr: ""},
		{name: "start after shutdown is rejected", ops: []string{opStart, opShutdown, opStart}, wantErr: "already stopped"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := NewScheduler(alwaysFail{}, nil, &Config{}, nil, testLogger())
			require.NoError(t, err)
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				_ = s.Shutdown(ctx)
			})

			var lastErr error
			for _, op := range tt.ops {
				switch op {
				case opStart:
					lastErr = s.Start(context.Background())
				case opShutdown:
					ctx, cancel := context.WithTimeout(context.Background(), time.Second)
					lastErr = s.Shutdown(ctx)
					cancel()
				}
			}

			if tt.wantErr == "" {
				require.NoError(t, lastErr)
			} else {
				require.ErrorContains(t, lastErr, tt.wantErr)
			}
		})
	}
}

// TestNew_validatesConfig checks that the one-shot constructor surfaces config
// errors and defaults a zero config into a valid one.
func TestNew_validatesConfig(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *Config
		wantErr string
	}{
		{
			name: "valid config",
			cfg:  &Config{Interval: 10 * time.Second, Timeout: time.Second},
		},
		{
			name: "zero config is defaulted and valid",
			cfg:  &Config{},
		},
		{
			name:    "interval below timeout is rejected",
			cfg:     &Config{Interval: time.Second, Timeout: 2 * time.Second},
			wantErr: "interval",
		},
		{
			name:    "interval equal to timeout is rejected",
			cfg:     &Config{Interval: time.Second, Timeout: time.Second},
			wantErr: "interval",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := New("tcp", tt.cfg, nil, nil, testLogger())
			if tt.wantErr == "" {
				require.NoError(t, err)
				require.NotNil(t, s)
			} else {
				require.ErrorContains(t, err, tt.wantErr)
				require.Nil(t, s)
			}
		})
	}
}

// TestScheduler_runUntilUnhealthy drives the real goroutine path end to end:
// every probe fails, both backends must flip to unhealthy exactly once, and a
// clean Shutdown must leave no goroutines behind (verified by goleak + -race).
func TestScheduler_runUntilUnhealthy(t *testing.T) {
	rec := &eventRecorder{}
	cfg := &Config{
		Interval:           20 * time.Millisecond,
		Timeout:            5 * time.Millisecond,
		UnhealthyThreshold: 1,
		HealthyThreshold:   1,
	}
	backends := []*model.Backend{
		model.NewBackend("10.0.0.1:80", 1, 0),
		model.NewBackend("10.0.0.2:80", 1, 1),
	}

	s, err := NewScheduler(alwaysFail{}, backends, cfg, rec.listener, testLogger())
	require.NoError(t, err)
	require.NoError(t, s.Start(context.Background()))

	require.Eventually(t, func() bool {
		return backends[0].Status() == model.StatusUnhealthy &&
			backends[1].Status() == model.StatusUnhealthy
	}, 2*time.Second, 5*time.Millisecond, "both backends should become unhealthy")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, s.Shutdown(shutdownCtx))

	// After Shutdown all goroutines are stopped, so the recorder is stable:
	// exactly one Healthy->Unhealthy event per backend, no flapping.
	events := rec.snapshot()
	require.Len(t, events, len(backends))
	for _, e := range events {
		require.Equal(t, model.StatusHealthy, e.From)
		require.Equal(t, model.StatusUnhealthy, e.To)
		require.Equal(t, "active health check", e.Reason)
		require.NotNil(t, e.Backend)
		require.False(t, e.At.IsZero())
	}
}
