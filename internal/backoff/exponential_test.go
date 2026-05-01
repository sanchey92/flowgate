package backoff_test

import (
	"context"
	"testing"
	"time"

	"github.com/sanchey92/flowgate/internal/backoff"
)

func TestExponentialWaitAndReset(t *testing.T) {
	b, err := backoff.NewExponential(time.Millisecond, 2*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}

	if got := b.Duration(); got != time.Millisecond {
		t.Fatalf("Duration() = %v, want %v", got, time.Millisecond)
	}
	if !b.Wait(context.Background()) {
		t.Fatal("Wait() = false, want true")
	}
	if got := b.Duration(); got != 2*time.Millisecond {
		t.Fatalf("Duration() after Wait() = %v, want %v", got, 2*time.Millisecond)
	}

	b.Reset()
	if got := b.Duration(); got != time.Millisecond {
		t.Fatalf("Duration() after Reset() = %v, want %v", got, time.Millisecond)
	}
}

func TestNewExponentialRejectsInvalidBase(t *testing.T) {
	if _, err := backoff.NewExponential(0, 2*time.Millisecond); err == nil {
		t.Fatal("NewExponential() error = nil, want error")
	}
}

func TestNewExponentialRejectsMaxAtOrBelowBase(t *testing.T) {
	tests := []struct {
		name string
		max  time.Duration
	}{
		{name: "equal", max: time.Millisecond},
		{name: "below", max: time.Millisecond - time.Nanosecond},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := backoff.NewExponential(time.Millisecond, tt.max); err == nil {
				t.Fatal("NewExponential() error = nil, want error")
			}
		})
	}
}

func TestExponentialWaitContextCanceled(t *testing.T) {
	b, err := backoff.NewExponential(time.Hour, 2*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if b.Wait(ctx) {
		t.Fatal("Wait() = true, want false")
	}
}
