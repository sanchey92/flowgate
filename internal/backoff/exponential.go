package backoff

import (
	"context"
	"fmt"
	"time"
)

type Exponential struct {
	base    time.Duration
	max     time.Duration
	current time.Duration
}

func NewExponential(baseDur, maxDur time.Duration) (Exponential, error) {
	if baseDur <= 0 {
		return Exponential{}, fmt.Errorf("backoff base must be positive: %s", baseDur)
	}
	if maxDur <= baseDur {
		return Exponential{}, fmt.Errorf("backoff max must be greater than base: max=%s base=%s", maxDur, baseDur)
	}
	return Exponential{
		base:    baseDur,
		max:     maxDur,
		current: baseDur,
	}, nil
}

func (b *Exponential) Duration() time.Duration {
	return b.current
}

func (b *Exponential) Reset() {
	b.current = b.base
}

func (b *Exponential) Wait(ctx context.Context) error {
	timer := time.NewTimer(b.current)
	defer timer.Stop()

	select {
	case <-timer.C:
		b.current = minDuration(b.current*2, b.max)
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
