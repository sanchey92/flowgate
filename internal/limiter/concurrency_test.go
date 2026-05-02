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
	if err := l.Acquire(ctx); err != nil {
		t.Fatalf("disabled limiter should acquire even with canceled context, got %v", err)
	}
	l.Release()
}

func TestConcurrencyAcquireRelease(t *testing.T) {
	l := limiter.NewConcurrency(1)
	if err := l.Acquire(context.Background()); err != nil {
		t.Fatalf("Acquire() = %v, want nil", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := l.Acquire(ctx); err == nil {
		t.Fatal("Acquire() on full limiter with canceled context = nil, want error")
	}

	l.Release()
	if err := l.Acquire(context.Background()); err != nil {
		t.Fatalf("Acquire() after Release() = %v, want nil", err)
	}
	l.Release()
}
