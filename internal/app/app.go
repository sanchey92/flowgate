package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"syscall"

	"github.com/sanchey92/flowgate/internal/config"
	"github.com/sanchey92/flowgate/internal/route"
	"github.com/sanchey92/flowgate/pkg/closer"
)

type App struct {
	cfg *config.Config
	log *slog.Logger
}

func New(cfg *config.Config, log *slog.Logger) *App {
	return &App{cfg: cfg, log: log}
}

func (a *App) Run(ctx context.Context) error {
	if len(a.cfg.Routes) == 0 {
		return errors.New("app: no routes configured")
	}

	//nolint:contextcheck // closer owns its lifetime via signals + explicit Close; AfterFunc below ties it to ctx
	c := closer.New(a.log, a.cfg.Server.ShutdownTimeout, syscall.SIGINT, syscall.SIGTERM)
	context.AfterFunc(ctx, func() { a.shutdown(ctx, c) })

	for i := range a.cfg.Routes {
		if err := a.startRoute(ctx, c, a.cfg.Routes[i]); err != nil {
			a.shutdown(ctx, c)
			return err
		}
	}

	a.log.Info("app started", slog.Int("routes", len(a.cfg.Routes)))

	if err := c.Wait(); err != nil {
		return fmt.Errorf("app: shutdown: %w", err)
	}
	return nil
}

func (a *App) startRoute(ctx context.Context, c *closer.Closer, r config.Route) error {
	rt, err := route.Assemble(r, a.cfg.Defaults, a.log)
	if err != nil {
		return fmt.Errorf("app: %w", err)
	}

	for _, lc := range rt.Lifecycles {
		if err := a.startLifecycle(ctx, c, rt.Name, lc.Start, lc.Shutdown); err != nil {
			return err
		}
	}

	a.log.Info("route started",
		slog.String("route", rt.Name),
		slog.String("protocol", rt.Protocol),
		slog.String("listen", rt.Addr().String()),
	)
	return nil
}

func (a *App) startLifecycle(
	ctx context.Context,
	c *closer.Closer,
	routeName string,
	start, shutdown func(context.Context) error,
) error {
	if err := start(ctx); err != nil {
		return fmt.Errorf("app: route %q: start: %w", routeName, err)
	}

	if err := c.Add(shutdown); err != nil {
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), a.cfg.Server.ShutdownTimeout)
		defer cancel()
		_ = shutdown(shutdownCtx)
		return fmt.Errorf("app: route %q: register shutdown: %w", routeName, err)
	}
	return nil
}

func (a *App) shutdown(parent context.Context, c *closer.Closer) {
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(parent), a.cfg.Server.ShutdownTimeout)
	defer cancel()
	_ = c.Close(shutdownCtx)
}
