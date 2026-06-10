package retry

import (
	"context"
	"fmt"
	"math/rand/v2"
	"time"
)

func (p *Policy) BackoffDelay(retry int) time.Duration {
	if p.backoffBase <= 0 {
		return 0
	}

	exp := p.backoffBase
	for i := 0; i < retry && exp < p.backoffMax; i++ {
		exp *= 2
	}
	if exp > p.backoffMax {
		exp = p.backoffMax
	}

	jitter := time.Duration(rand.Int64N(int64(p.backoffBase))) //nolint:gosec // джиттер бэкоффа, криптостойкость не нужна

	delay := exp + jitter
	if delay > p.backoffMax {
		delay = p.backoffMax
	}
	return delay
}

func Sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("backoff interrupted: %w", ctx.Err())
	}
}
