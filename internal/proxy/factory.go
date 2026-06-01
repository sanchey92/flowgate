package proxy

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strings"

	"github.com/sanchey92/flowgate/internal/backoff"
	"github.com/sanchey92/flowgate/internal/balancer"
	"github.com/sanchey92/flowgate/internal/config"
	"github.com/sanchey92/flowgate/internal/domain/model"
	"github.com/sanchey92/flowgate/internal/limiter"
	"github.com/sanchey92/flowgate/internal/pool"
	"github.com/sanchey92/flowgate/internal/proxy/proxyproto"
	"github.com/sanchey92/flowgate/internal/proxy/tcp"
	"github.com/sanchey92/flowgate/internal/proxy/udp"
	"github.com/sanchey92/flowgate/internal/registry"
)

type Runner interface {
	Start(ctx context.Context) error
	Shutdown(ctx context.Context) error
	Addr() net.Addr
}

var (
	_ Runner = (*tcp.Proxy)(nil)
	_ Runner = (*udp.Proxy)(nil)
)

type Built struct {
	Runner   Runner
	Backends []*model.Backend
}

type Kind string

const (
	KindTCP  Kind = "tcp"
	KindUDP  Kind = "udp"
	KindHTTP Kind = "http"
)

func New(r config.Route, proxyCfg config.Proxy, log *slog.Logger) (Built, error) {
	switch Kind(strings.ToLower(strings.TrimSpace(r.Protocol))) {
	case KindTCP:
		return newTCP(r, proxyCfg, log)
	case KindUDP:
		return newUDP(r, proxyCfg, log)
	case KindHTTP:
		return newHTTP(r, proxyCfg, log)
	default:
		return Built{}, fmt.Errorf("proxy: route %q: unknown protocol %q", r.Name, r.Protocol)
	}
}

func newTCP(r config.Route, proxyCfg config.Proxy, log *slog.Logger) (Built, error) {
	bal, backends, err := buildBalancer(r.Name, r.Balancer, r.Backends)
	if err != nil {
		return Built{}, err
	}

	bo, err := backoff.NewExponential(proxyCfg.Backoff.Base, proxyCfg.Backoff.Max)
	if err != nil {
		return Built{}, fmt.Errorf("proxy: route %q: backoff: %w", r.Name, err)
	}

	ppMode, err := proxyproto.ParseMode(r.ProxyProtocol)
	if err != nil {
		return Built{}, fmt.Errorf("proxy: route %q: proxyproto mode: %w", r.Name, err)
	}

	bp := pool.NewBufferPool(proxyCfg.BufSize)
	lim := limiter.NewConcurrency(proxyCfg.MaxConns)

	handler := tcp.NewHandler(
		bal,
		bp,
		tcp.Timeouts{
			Connect:         proxyCfg.ConnectTimeout,
			Idle:            proxyCfg.IdleTimeout,
			KeepAlivePeriod: proxyCfg.KeepAlive,
		},
		ppMode,
		proxyCfg.ProxyProtoHdrTimeout,
		log,
		nil,
	)

	return Built{
		Runner:   tcp.New(r.Name, r.Listen, handler, lim, &bo, log),
		Backends: backends,
	}, nil
}

func newUDP(r config.Route, proxyCfg config.Proxy, log *slog.Logger) (Built, error) {
	bal, backends, err := buildBalancer(r.Name, r.Balancer, r.Backends)
	if err != nil {
		return Built{}, err
	}

	return Built{
		Runner: udp.New(
			r.Name,
			r.Listen,
			bal,
			udp.Timeouts{
				SessionIdle: proxyCfg.UDP.SessionIdle,
				BackendRead: proxyCfg.UDP.BackendRead,
				Dial:        proxyCfg.UDP.Dial,
			},
			log,
			nil,
		),
		Backends: backends,
	}, nil
}

func buildBalancer(routeName, kind string, backends []config.Backend) (balancer.Balancer, []*model.Backend, error) {
	if len(backends) == 0 {
		return nil, nil, fmt.Errorf("proxy: route %q: no backends", routeName)
	}
	models := buildBackends(backends)
	reg := registry.NewInMemory(models)
	bal, err := balancer.New(kind, reg)
	if err != nil {
		return nil, nil, fmt.Errorf("proxy: route %q: balancer: %w", routeName, err)
	}
	return bal, models, nil
}

func buildBackends(in []config.Backend) []*model.Backend {
	out := make([]*model.Backend, 0, len(in))
	for i, b := range in {
		out = append(out, model.NewBackend(b.Addr, b.Weight, i))
	}
	return out
}
