package active

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/sanchey92/flowgate/internal/domain/model"
)

type Scheduler struct {
	checker  Checker
	backends []*model.Backend
	cfg      *Config
	onChange model.StatusListener
	log      *slog.Logger

	mu      sync.Mutex
	cancel  context.CancelFunc
	started bool
	stopped bool
	wg      sync.WaitGroup
}

func NewScheduler(
	checker Checker,
	backends []*model.Backend,
	cfg *Config,
	onChange model.StatusListener,
	log *slog.Logger,
) (*Scheduler, error) {
	cfg = cfg.withDefaults()
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}
	return &Scheduler{
		checker:  checker,
		backends: backends,
		cfg:      cfg,
		onChange: onChange,
		log:      log,
	}, nil
}

func (s *Scheduler) Start(_ context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.stopped {
		return fmt.Errorf("active: scheduler already stopped")
	}
	if s.started {
		return fmt.Errorf("active: scheduler already started")
	}

	//nolint:gosec // G118: cancel is retained in s.cancel and invoked in Shutdown
	runCtx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.started = true

	for _, b := range s.backends {
		s.wg.Add(1)
		go s.runBackend(runCtx, b) //nolint:contextcheck // loop lifetime is managed by s.cancel, intentionally detached from parent ctx
	}

	s.log.Info("active health checks started",
		slog.Int("backends", len(s.backends)),
		slog.Duration("interval", s.cfg.Interval),
		slog.Duration("timeout", s.cfg.Timeout),
	)

	return nil
}

func (s *Scheduler) runBackend(ctx context.Context, b *model.Backend) {
	defer s.wg.Done()

	//nolint:gosec // G404: jitter to desync probes across backends, not security-sensitive
	if !s.sleep(ctx, rand.N(s.cfg.Interval)) {
		return
	}

	ticker := time.NewTicker(s.cfg.Interval)
	defer ticker.Stop()

	var consecutiveSuccess, consecutiveFailure int

	for {
		s.probe(ctx, b, &consecutiveSuccess, &consecutiveFailure)

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Scheduler) sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (s *Scheduler) probe(ctx context.Context, b *model.Backend, cs, cf *int) {
	probeCtx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
	defer cancel()

	err := s.checker.Check(probeCtx, b)
	if err != nil {
		*cs = 0
		*cf++
	} else {
		*cf = 0
		*cs++
	}

	switch b.Status() {
	case model.StatusHealthy:
		if err != nil && *cf >= s.cfg.UnhealthyThreshold {
			s.transition(b, model.StatusHealthy, model.StatusUnhealthy, err)
		}
	case model.StatusUnhealthy:
		if err == nil && *cs >= s.cfg.HealthyThreshold {
			s.transition(b, model.StatusUnhealthy, model.StatusHealthy, nil)
		}
	case model.StatusDraining:
	}
}

func (s *Scheduler) transition(b *model.Backend, from, to model.BackendStatus, cause error) {
	if !b.Transition(from, to) {
		return
	}

	switch to {
	case model.StatusUnhealthy:
		s.log.Warn("backend marked unhealthy",
			slog.String("backend", b.ID),
			slog.String("addr", b.Addr),
			slog.Any("error", cause),
		)
	case model.StatusHealthy:
		s.log.Info("backend recovered",
			slog.String("backend", b.ID),
			slog.String("addr", b.Addr),
		)
	case model.StatusDraining:
		// active checks never transition a backend into draining
	}

	if s.onChange != nil {
		s.onChange(&model.StatusEvent{
			Backend: b,
			From:    from,
			To:      to,
			Reason:  "active health check",
			At:      time.Now(),
		})
	}
}

func (s *Scheduler) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	if !s.started || s.stopped {
		s.mu.Unlock()
		return nil
	}
	s.stopped = true
	cancel := s.cancel
	s.mu.Unlock()

	cancel()

	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		s.log.Info("active health checks stopped")
		return nil
	case <-ctx.Done():
		return fmt.Errorf("active: shutdown timeout: %w", ctx.Err())
	}
}
