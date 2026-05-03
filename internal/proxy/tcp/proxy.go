package tcp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"syscall"
	"time"

	domainErr "github.com/sanchey92/flowgate/internal/domain/errors"
)

type Stream interface {
	Handle(ctx context.Context, conn net.Conn)
}

type Limiter interface {
	Acquire(ctx context.Context) error
	Release()
}

type Backoff interface {
	Duration() time.Duration
	Wait(ctx context.Context) error
	Reset()
}

type Proxy struct {
	name   string
	listen string
	log    *slog.Logger

	mu       sync.Mutex
	listener net.Listener
	cancel   context.CancelFunc
	done     chan struct{}
	started  bool
	stopped  bool

	wg      sync.WaitGroup
	limiter Limiter
	backoff Backoff
	handler Stream
}

func New(name, listen string, h Stream, l Limiter, b Backoff, log *slog.Logger) *Proxy {
	return &Proxy{
		name:    name,
		listen:  listen,
		log:     log,
		limiter: l,
		backoff: b,
		handler: h,
	}
}

func (p *Proxy) Addr() net.Addr {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.listener == nil {
		return nil
	}
	return p.listener.Addr()
}

func (p *Proxy) Start(ctx context.Context) error {
	p.mu.Lock()

	if p.stopped {
		p.mu.Unlock()
		return domainErr.ErrProxyStopped
	}
	if p.started {
		p.mu.Unlock()
		return domainErr.ErrProxyStarted
	}

	lc := net.ListenConfig{}
	ln, err := lc.Listen(ctx, "tcp", p.listen)
	if err != nil {
		p.mu.Unlock()
		return fmt.Errorf("tcp proxy: %q: listen %s: %w", p.name, p.listen, err)
	}

	loopCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	p.listener = ln
	p.cancel = cancel
	p.done = done
	p.started = true
	p.wg.Add(1)
	p.mu.Unlock()

	go p.acceptLoop(loopCtx, ln) //nolint:contextcheck // loop lifetime is managed by p.cancel, intentionally detached from parent ctx
	go p.complete(done)
	go p.watchParentCtx(ctx, done)

	p.log.Info("tcp proxy started", slog.String("addr", ln.Addr().String()))
	return nil
}

func (p *Proxy) watchParentCtx(ctx context.Context, done <-chan struct{}) {
	select {
	case <-ctx.Done():
		p.stop()
	case <-done:
	}
}

func (p *Proxy) acceptLoop(ctx context.Context, ln net.Listener) {
	defer p.wg.Done()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) || ctx.Err() != nil {
				p.log.Info("tcp listener closed, accept loop exiting")
				return
			}
			if isAcceptRetriable(err) {
				p.log.Warn("accept error, backing off",
					slog.Duration("backoff", p.backoff.Duration()),
					slog.Any("error", err))
				if waitErr := p.backoff.Wait(ctx); waitErr != nil {
					return
				}
				continue
			}
			p.log.Error("accept fatal error, stopping proxy", slog.Any("error", err))
			go p.stop()
			return
		}
		p.backoff.Reset()

		if err := p.limiter.Acquire(ctx); err != nil {
			_ = conn.Close()
			return
		}
		if ctx.Err() != nil {
			p.limiter.Release()
			_ = conn.Close()
			return
		}
		p.wg.Add(1)
		go p.serveConn(ctx, conn)
	}
}

func (p *Proxy) serveConn(ctx context.Context, conn net.Conn) {
	defer p.wg.Done()
	defer p.limiter.Release()

	p.handler.Handle(ctx, conn)
}

//nolint:contextcheck // ctx may be nil; falling back to Background is intentional for graceful shutdown
func (p *Proxy) Shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}

	done := p.stop()
	if done == nil {
		return nil
	}

	select {
	case <-done:
		p.log.Info("tcp proxy shutdown complete")
		return nil
	case <-ctx.Done():
		p.log.Warn("tcp proxy shutdown timed out, active connections still alive",
			slog.Any("ctx_err", ctx.Err()))
		return fmt.Errorf("tcp proxy %q: shutdown: %w", p.name, ctx.Err())
	}
}

func (p *Proxy) stop() <-chan struct{} {
	var (
		cancel   context.CancelFunc
		listener net.Listener
		done     chan struct{}
	)

	p.mu.Lock()
	if !p.started {
		p.mu.Unlock()
		return nil
	}
	done = p.done
	if !p.stopped {
		p.stopped = true
		cancel = p.cancel
		listener = p.listener
		p.listener = nil
	}
	p.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if listener != nil {
		if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			p.log.Warn("listener close error", slog.Any("error", err))
		}
	}
	return done
}

func (p *Proxy) complete(done chan struct{}) {
	p.wg.Wait()
	p.mu.Lock()
	if p.done == done {
		p.started = false
		p.listener = nil
		p.cancel = nil
	}
	p.mu.Unlock()
	close(done)
}

func isAcceptRetriable(err error) bool {
	return errors.Is(err, syscall.EMFILE) ||
		errors.Is(err, syscall.ENFILE) ||
		errors.Is(err, syscall.ECONNABORTED)
}
