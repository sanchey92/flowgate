package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"syscall"

	"github.com/sanchey92/flowgate/internal/config"
	"github.com/sanchey92/flowgate/internal/domain/model"
	"github.com/sanchey92/flowgate/internal/health/active"
	"github.com/sanchey92/flowgate/internal/proxy"
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
	settings := r.Effective(a.cfg.Defaults)
	routeLog := a.log.With(slog.String("route", r.Name))

	built, err := proxy.New(r, a.cfg.Defaults, settings, routeLog)
	if err != nil {
		return fmt.Errorf("app: route %q: %w", r.Name, err)
	}

	sched, err := buildScheduler(r, built.Backends, routeLog)
	if err != nil {
		return fmt.Errorf("app: route %q: %w", r.Name, err)
	}

	if err := a.startLifecycle(ctx, c, r.Name, built.Runner.Start, built.Runner.Shutdown); err != nil {
		return err
	}

	if sched != nil {
		if err := a.startLifecycle(ctx, c, r.Name, sched.Start, sched.Shutdown); err != nil {
			return err
		}
	}

	routeLog.Info("route started",
		slog.String("protocol", r.Protocol),
		slog.String("listen", built.Runner.Addr().String()),
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

func buildScheduler(r config.Route, backends []*model.Backend, log *slog.Logger) (*active.Scheduler, error) {
	if r.HealthCheck == nil || !r.HealthCheck.Active.Enabled {
		return nil, nil
	}

	hc := r.HealthCheck.Active
	cfg := &active.Config{
		Enabled:            hc.Enabled,
		Interval:           hc.Interval,
		Timeout:            hc.Timeout,
		UnhealthyThreshold: hc.UnhealthyThreshold,
		HealthyThreshold:   hc.HealthyThreshold,
		Path:               hc.Path,
		ExpectedStatus:     hc.ExpectedStatus,
	}

	sched, err := active.New(r.Protocol, cfg, backends, nil, log)
	if err != nil {
		return nil, fmt.Errorf("health: %w", err)
	}
	return sched, nil
}

func (a *App) shutdown(parent context.Context, c *closer.Closer) {
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(parent), a.cfg.Server.ShutdownTimeout)
	defer cancel()
	_ = c.Close(shutdownCtx)
}
