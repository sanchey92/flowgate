package closer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"time"
)

const defaultShutdownTimeout = 5 * time.Second

var ErrClosed = errors.New("closer: already closed")

type Closer struct {
	mu              sync.Mutex
	once            sync.Once
	done            chan struct{}
	funcs           []func(context.Context) error
	log             *slog.Logger
	shutdownTimeout time.Duration
	closed          bool
	err             error
}

func New(log *slog.Logger, shutdownTimeout time.Duration, signals ...os.Signal) *Closer {
	if shutdownTimeout <= 0 {
		shutdownTimeout = defaultShutdownTimeout
	}

	c := &Closer{
		done:            make(chan struct{}),
		log:             log,
		shutdownTimeout: shutdownTimeout,
	}

	if len(signals) > 0 {
		go c.handleSignals(signals...)
	}

	return c
}

func (c *Closer) Wait() error {
	<-c.done
	return c.err
}

func (c *Closer) Close(ctx context.Context) error {
	return c.closeAll(ctx)
}

func (c *Closer) CloseWithTimeout() error {
	ctx, cancel := context.WithTimeout(context.Background(), c.shutdownTimeout)
	defer cancel()

	return c.closeAll(ctx)
}

func (c *Closer) Add(fn func(context.Context) error) error {
	if fn == nil {
		return fmt.Errorf("closer: nil function")
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		c.log.Warn("Add called after Close, function will not be registered")
		return ErrClosed
	}
	c.funcs = append(c.funcs, fn)
	return nil
}

func (c *Closer) handleSignals(signals ...os.Signal) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, signals...)
	defer signal.Stop(ch)

	select {
	case <-ch:
		c.log.Info("signal detected, starting graceful shutdown")

		go func() {
			select {
			case <-ch:
				c.log.Info("second signal received, forcing exit")
				os.Exit(1)
			case <-c.done:
			}
		}()

		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), c.shutdownTimeout)
		defer shutdownCancel()

		if err := c.closeAll(shutdownCtx); err != nil {
			c.log.Error("shutdown error", slog.Any("error", err))
		}

	case <-c.done:
	}
}

func (c *Closer) closeAll(ctx context.Context) error {
	c.once.Do(func() {
		defer close(c.done)

		c.mu.Lock()
		funcs := c.funcs
		c.funcs = nil
		c.closed = true
		c.mu.Unlock()

		total := len(funcs)
		if total == 0 {
			c.log.Info("no functions to close")
			return
		}

		if ctx.Err() != nil {
			c.log.Warn("context already canceled, skipped shutdown", slog.Any("err", ctx.Err()))
			c.err = ctx.Err()
			return
		}

		c.log.Info("closing all resources", slog.Int("count", total))

		for i := total - 1; i >= 0; i-- {
			if ctx.Err() != nil {
				c.log.Warn("shutdown timeout exceeded, skipping remaining",
					slog.Int("skipped", i+1),
					slog.Any("err", ctx.Err()))
				break
			}
			c.runClose(ctx, funcs[i], total-i, total)
		}

		if err := ctx.Err(); err != nil {
			c.err = errors.Join(c.err, err)
			return
		}

		c.log.Info("all resources closed")
	})
	<-c.done
	return c.err
}

func (c *Closer) runClose(ctx context.Context, f func(context.Context) error, seq, total int) {
	errCh := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				errCh <- fmt.Errorf("panic recovered in closer: %v", r)
				c.log.Error("panic in close function", slog.Any("error", r))
			}
		}()
		errCh <- f(ctx)
	}()

	select {
	case err := <-errCh:
		if err != nil {
			c.log.Error("close error",
				slog.Int("seq", seq), slog.Int("total", total),
				slog.Any("error", err))
			c.err = errors.Join(c.err, err)
		}
	case <-ctx.Done():
		c.log.Warn("close function did not finish before deadline, abandoning",
			slog.Int("seq", seq), slog.Int("total", total),
			slog.Any("err", ctx.Err()))
	}
}
