package proxyhttp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	domainErr "github.com/sanchey92/flowgate/internal/domain/errors"
)

type RunnerSettings struct {
	ReadHeaderTimeout time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
}

type Runner struct {
	name   string
	listen string
	log    *slog.Logger

	server *http.Server

	mu       sync.Mutex
	listener net.Listener
	done     chan struct{}
	started  bool
	stopped  bool
}

func NewRunner(name, listen string, handler http.Handler, s RunnerSettings, log *slog.Logger) *Runner {
	return &Runner{
		name:   name,
		listen: listen,
		log:    log,
		server: &http.Server{
			Handler:           handler,
			ReadHeaderTimeout: s.ReadHeaderTimeout,
			WriteTimeout:      s.WriteTimeout,
			IdleTimeout:       s.IdleTimeout,
			ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
		},
	}
}

func (r *Runner) Addr() net.Addr {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.listener == nil {
		return nil
	}
	return r.listener.Addr()
}

func (r *Runner) Start(ctx context.Context) error {
	r.mu.Lock()

	if r.stopped {
		r.mu.Unlock()
		return domainErr.ErrProxyStopped
	}
	if r.started {
		r.mu.Unlock()
		return domainErr.ErrProxyStarted
	}

	lc := net.ListenConfig{}
	ln, err := lc.Listen(ctx, "tcp", r.listen)
	if err != nil {
		r.mu.Unlock()
		return fmt.Errorf("http proxy %q: listen %s: %w", r.name, r.listen, err)
	}

	done := make(chan struct{})
	r.listener = ln
	r.done = done
	r.started = true
	r.mu.Unlock()

	go r.serve(ln, done)

	r.log.Info("http proxy started", slog.String("addr", ln.Addr().String()))
	return nil
}

func (r *Runner) serve(ln net.Listener, done chan struct{}) {
	defer close(done)
	if err := r.server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		r.log.Error("http server serve",
			slog.String("name", r.name),
			slog.Any("error", err),
		)
	}
}

//nolint:contextcheck // ctx may be nil; falling back to Background is intentional for graceful shutdown
func (r *Runner) Shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}

	r.mu.Lock()
	if !r.started {
		r.mu.Unlock()
		return nil
	}
	done := r.done
	if r.stopped {
		r.mu.Unlock()
		if done != nil {
			<-done
		}
		return nil
	}
	r.stopped = true
	r.mu.Unlock()

	shutdownErr := r.server.Shutdown(ctx)

	<-done

	r.mu.Lock()
	r.listener = nil
	r.mu.Unlock()

	if shutdownErr != nil {
		r.log.Warn("http proxy shutdown timed out",
			slog.String("name", r.name),
			slog.Any("error", shutdownErr),
		)
		return fmt.Errorf("http proxy %q: shutdown: %w", r.name, shutdownErr)
	}

	r.log.Info("http proxy shutdown complete", slog.String("name", r.name))
	return nil
}
