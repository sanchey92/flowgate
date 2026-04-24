package closer_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sanchey92/flowgate/pkg/closer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func newCloser(t *testing.T) *closer.Closer {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return closer.New(log, time.Second)
}

func TestAdd(t *testing.T) {
	tests := []struct {
		name      string
		preClose  bool
		fn        func(context.Context) error
		assertErr func(*testing.T, error)
	}{
		{
			name: "valid fn accepted",
			fn:   func(ctx context.Context) error { return nil },
			assertErr: func(t *testing.T, err error) {
				assert.NoError(t, err)
			},
		},
		{
			name: "nil fn rejected",
			fn:   nil,
			assertErr: func(t *testing.T, err error) {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "nil function")
			},
		},
		{
			name:     "add after close returns ErrClosed",
			preClose: true,
			fn:       func(ctx context.Context) error { return nil },
			assertErr: func(t *testing.T, err error) {
				assert.ErrorIs(t, err, closer.ErrClosed)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newCloser(t)
			if tt.preClose {
				require.NoError(t, c.Close(context.Background()))
			} else {
				t.Cleanup(func() { _ = c.Close(context.Background()) })
			}
			tt.assertErr(t, c.Add(tt.fn))
		})
	}
}

func TestCloseErrorAggregation(t *testing.T) {
	errA := errors.New("err-a")
	errB := errors.New("err-b")

	tests := []struct {
		name        string
		funcs       []func(context.Context) error
		wantNoError bool
		wantIs      []error
		wantSubstr  string
	}{
		{
			name:        "no funcs → no error",
			funcs:       nil,
			wantNoError: true,
		},
		{
			name: "all succeed → no error",
			funcs: []func(context.Context) error{
				func(ctx context.Context) error { return nil },
				func(ctx context.Context) error { return nil },
			},
			wantNoError: true,
		},
		{
			name: "single error propagated",
			funcs: []func(context.Context) error{
				func(ctx context.Context) error { return errA },
			},
			wantIs: []error{errA},
		},
		{
			name: "multiple errors joined",
			funcs: []func(context.Context) error{
				func(ctx context.Context) error { return errA },
				func(ctx context.Context) error { return errB },
			},
			wantIs: []error{errA, errB},
		},
		{
			name: "panic recovered and reported",
			funcs: []func(context.Context) error{
				func(ctx context.Context) error { panic("boom") },
			},
			wantSubstr: "panic recovered",
		},
		{
			name: "panic does not abort other closers",
			funcs: []func(context.Context) error{
				func(ctx context.Context) error { return errA },
				func(ctx context.Context) error { panic("boom") },
				func(ctx context.Context) error { return errB },
			},
			wantIs:     []error{errA, errB},
			wantSubstr: "panic recovered",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newCloser(t)
			for _, fn := range tt.funcs {
				require.NoError(t, c.Add(fn))
			}
			err := c.Close(context.Background())
			if tt.wantNoError {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			for _, want := range tt.wantIs {
				assert.ErrorIs(t, err, want)
			}
			if tt.wantSubstr != "" {
				assert.ErrorContains(t, err, tt.wantSubstr)
			}
		})
	}
}

func TestCloseLIFOOrder(t *testing.T) {
	var (
		order []int
		mu    sync.Mutex
	)

	c := newCloser(t)
	for i := range 5 {
		require.NoError(t, c.Add(func(ctx context.Context) error {
			mu.Lock()
			order = append(order, i)
			mu.Unlock()
			return nil
		}))
	}

	require.NoError(t, c.Close(context.Background()))
	assert.Equal(t, []int{4, 3, 2, 1, 0}, order)
}

func TestCloseIdempotent(t *testing.T) {
	var calls atomic.Int32
	errA := errors.New("err-a")

	c := newCloser(t)
	require.NoError(t, c.Add(func(ctx context.Context) error {
		calls.Add(1)
		return errA
	}))

	err1 := c.Close(context.Background())
	err2 := c.Close(context.Background())
	err3 := c.Close(context.Background())

	assert.ErrorIs(t, err1, errA)
	assert.ErrorIs(t, err2, errA)
	assert.ErrorIs(t, err3, errA)
	assert.EqualValues(t, 1, calls.Load(), "close func must run exactly once")
}

func TestCloseConcurrent(t *testing.T) {
	var calls atomic.Int32

	c := newCloser(t)
	require.NoError(t, c.Add(func(ctx context.Context) error {
		calls.Add(1)
		time.Sleep(10 * time.Millisecond)
		return nil
	}))

	const n = 20
	var wg sync.WaitGroup
	errs := make([]error, n)
	wg.Add(n)
	for i := range n {
		go func() {
			defer wg.Done()
			errs[i] = c.Close(context.Background())
		}()
	}
	wg.Wait()

	assert.EqualValues(t, 1, calls.Load())
	for _, err := range errs {
		assert.NoError(t, err)
	}
}

func TestCloseCtxAlreadyCancelled(t *testing.T) {
	c := newCloser(t)
	var called atomic.Bool
	require.NoError(t, c.Add(func(ctx context.Context) error {
		called.Store(true)
		return nil
	}))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := c.Close(ctx)
	assert.ErrorIs(t, err, context.Canceled)
	assert.False(t, called.Load(), "funcs must not run on pre-cancelled ctx")
}

func TestCloseHangSafe(t *testing.T) {
	release := make(chan struct{})
	done := make(chan struct{})
	t.Cleanup(func() {
		close(release)
		<-done
	})

	c := newCloser(t)
	require.NoError(t, c.Add(func(ctx context.Context) error {
		defer close(done)
		<-release
		return nil
	}))

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := c.Close(ctx)
	elapsed := time.Since(start)

	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, elapsed, 500*time.Millisecond, "Close must abandon hanging fn within ctx deadline")
}

func TestCloseSkipsRemainingAfterTimeout(t *testing.T) {
	var (
		called []int
		mu     sync.Mutex
	)
	record := func(i int) {
		mu.Lock()
		defer mu.Unlock()
		called = append(called, i)
	}

	release := make(chan struct{})
	done := make(chan struct{})
	t.Cleanup(func() {
		close(release)
		<-done
	})

	c := newCloser(t)
	for i := range 3 {
		require.NoError(t, c.Add(func(ctx context.Context) error {
			record(i)
			return nil
		}))
	}
	require.NoError(t, c.Add(func(ctx context.Context) error {
		defer close(done)
		<-release
		return nil
	}))
	for i := 4; i < 6; i++ {
		require.NoError(t, c.Add(func(ctx context.Context) error {
			record(i)
			return nil
		}))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	err := c.Close(ctx)
	assert.ErrorIs(t, err, context.DeadlineExceeded)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []int{5, 4}, called, "LIFO: 5,4 run → hang abandoned → 2,1,0 skipped")
}

func TestCloseWithTimeout(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	c := closer.New(log, 50*time.Millisecond)

	release := make(chan struct{})
	done := make(chan struct{})
	t.Cleanup(func() {
		close(release)
		<-done
	})
	require.NoError(t, c.Add(func(ctx context.Context) error {
		defer close(done)
		<-release
		return nil
	}))

	start := time.Now()
	err := c.CloseWithTimeout()
	elapsed := time.Since(start)

	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, elapsed, 500*time.Millisecond)
}

func TestWait(t *testing.T) {
	c := newCloser(t)
	require.NoError(t, c.Add(func(ctx context.Context) error { return nil }))

	waitDone := make(chan error, 1)
	go func() { waitDone <- c.Wait() }()

	select {
	case <-waitDone:
		t.Fatal("Wait returned before Close")
	case <-time.After(20 * time.Millisecond):
	}

	require.NoError(t, c.Close(context.Background()))

	select {
	case err := <-waitDone:
		assert.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("Wait did not return after Close")
	}
}
