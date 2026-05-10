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
	"github.com/sanchey92/flowgate/internal/limiter"
	"github.com/sanchey92/flowgate/internal/pool"
	"github.com/sanchey92/flowgate/internal/proxy/proxyproto"
	"github.com/sanchey92/flowgate/internal/proxy/tcp"
	"github.com/sanchey92/flowgate/internal/proxy/udp"
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

type Kind string

const (
	KindTCP Kind = "tcp"
	KindUDP Kind = "udp"
)

func New(r config.Route, s config.Settings, bal balancer.Balancer, log *slog.Logger) (Runner, error) {
	switch Kind(strings.ToLower(strings.TrimSpace(r.Protocol))) {
	case KindTCP:
		return newTCP(r, s, bal, log)
	case KindUDP:
		return newUDP(r, s, bal, log), nil
	default:
		return nil, fmt.Errorf("proxy: route %q: unknown protocol %q", r.Name, r.Protocol)
	}
}

func newTCP(r config.Route, s config.Settings, bal balancer.Balancer, log *slog.Logger) (Runner, error) {
	bo, err := backoff.NewExponential(s.Backoff.Base, s.Backoff.Max)
	if err != nil {
		return nil, fmt.Errorf("proxy: route %q: backoff: %w", r.Name, err)
	}

	ppMode, err := proxyproto.ParseMode(r.ProxyProtocol)
	if err != nil {
		return nil, fmt.Errorf("proxy: route %q: proxyproto mode: %w", r.Name, err)
	}

	bp := pool.NewBufferPool(s.BufSize)
	lim := limiter.NewConcurrency(s.MaxConns)

	handler := tcp.NewHandler(
		bal,
		bp,
		tcp.Timeouts{
			Connect:         s.ConnectTimeout,
			Idle:            s.IdleTimeout,
			KeepAlivePeriod: s.KeepAlive,
		},
		ppMode,
		s.ProxyProtoHdrTimeout,
		log,
		nil,
	)

	return tcp.New(r.Name, r.Listen, handler, lim, &bo, log), nil
}

func newUDP(r config.Route, s config.Settings, bal balancer.Balancer, log *slog.Logger) Runner {
	return udp.New(
		r.Name,
		r.Listen,
		bal,
		udp.Timeouts{
			SessionIdle: s.UDP.SessionIdle,
			BackendRead: s.UDP.BackendRead,
			Dial:        s.UDP.Dial,
		},
		log,
		nil,
	)
}
