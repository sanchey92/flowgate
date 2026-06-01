package route

import (
	"context"
	"fmt"
	"log/slog"
	"net"

	"github.com/sanchey92/flowgate/internal/config"
	"github.com/sanchey92/flowgate/internal/domain/model"
	"github.com/sanchey92/flowgate/internal/health/active"
	"github.com/sanchey92/flowgate/internal/health/passive"
	"github.com/sanchey92/flowgate/internal/proxy"
)

type Lifecycle struct {
	Start    func(context.Context) error
	Shutdown func(context.Context) error
}

type Route struct {
	Name       string
	Protocol   string
	Addr       func() net.Addr // bound address; meaningful only after the runner has started
	Backends   []*model.Backend
	Lifecycles []Lifecycle
}

func Assemble(r config.Route, proxyCfg config.Proxy, log *slog.Logger) (*Route, error) {
	log = log.With(slog.String("route", r.Name))

	built, err := proxy.New(r, proxyCfg, log)
	if err != nil {
		return nil, fmt.Errorf("route %q: %w", r.Name, err)
	}

	if hc := r.HealthCheck; hc != nil && hc.Passive.Enabled {
		if err := passive.Attach(built.Backends, passiveConfig(hc.Passive)); err != nil {
			return nil, fmt.Errorf("route %q: %w", r.Name, err)
		}
	}

	rt := &Route{
		Name:     r.Name,
		Protocol: r.Protocol,
		Addr:     built.Runner.Addr,
		Backends: built.Backends,
		Lifecycles: []Lifecycle{
			{Start: built.Runner.Start, Shutdown: built.Runner.Shutdown},
		},
	}

	sched, err := buildScheduler(r, built.Backends, log)
	if err != nil {
		return nil, fmt.Errorf("route %q: %w", r.Name, err)
	}
	if sched != nil {
		rt.Lifecycles = append(rt.Lifecycles, Lifecycle{
			Start:    sched.Start,
			Shutdown: sched.Shutdown,
		})
	}

	return rt, nil
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

func passiveConfig(c config.PassiveCheckConfig) *passive.Config {
	return &passive.Config{
		ErrorThreshold:   c.ErrorThreshold,
		Window:           c.Window,
		RecoveryInterval: c.RecoveryInterval,
	}
}
