package limiter_test

import (
	"context"
	"testing"

	"github.com/sanchey92/flowgate/internal/limiter"
)

func TestConcurrencyDisabled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	l := limiter.NewConcurrency(0)
	if !l.Acquire(ctx) {
		t.Fatal("disabled limiter should acquire even with canceled context")
	}
	l.Release()
}

func TestConcurrencyAcquireRelease(t *testing.T) {
	l := limiter.NewConcurrency(1)
	if !l.Acquire(context.Background()) {
		t.Fatal("Acquire() = false, want true")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if l.Acquire(ctx) {
		t.Fatal("Acquire() on full limiter with canceled context = true, want false")
	}

	l.Release()
	if !l.Acquire(context.Background()) {
		t.Fatal("Acquire() after Release() = false, want true")
	}
	l.Release()
}
