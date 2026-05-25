package proxy

import (
	"fmt"
	"log/slog"

	"github.com/sanchey92/flowgate/internal/config"
	"github.com/sanchey92/flowgate/internal/pool"
	proxyhttp "github.com/sanchey92/flowgate/internal/proxy/http"
	"github.com/sanchey92/flowgate/internal/proxy/http/router"
)

var _ Runner = (*proxyhttp.Runner)(nil)

func newHTTP(r config.Route, defaults config.Defaults, s config.Settings, log *slog.Logger) (Runner, error) {
	if r.HTTP == nil {
		return nil, fmt.Errorf("proxy: route %q: http config is required", r.Name)
	}

	groups, err := buildHTTPGroups(r.Name, r.Balancer, r.HTTP.BackendGroups)
	if err != nil {
		return nil, err
	}

	rt, err := router.Build(r.HTTP.RoutingRules, log)
	if err != nil {
		return nil, fmt.Errorf("proxy: route %q: router: %w", r.Name, err)
	}

	httpSet := r.HTTP.EffectiveTimeouts(defaults.HTTP)

	tr := proxyhttp.BuildTransport(proxyhttp.TransportSettings{
		ConnectTimeout:        s.ConnectTimeout,
		ResponseHeaderTimeout: httpSet.ResponseHeaderTimeout,
		IdleConnTimeout:       s.IdleTimeout,
		KeepAlivePeriod:       s.KeepAlive,
	})

	bp := pool.NewBufferPool(s.BufSize)

	p := proxyhttp.New(
		rt,
		groups,
		tr,
		bp,
		r.HTTP.HeaderRules,
		r.HTTP.StandardHeaders,
		r.HTTP.WebSocket,
		log,
	)

	return proxyhttp.NewRunner(r.Name, r.Listen, p, proxyhttp.RunnerSettings{
		ReadHeaderTimeout: httpSet.ReadHeaderTimeout,
		WriteTimeout:      httpSet.WriteTimeout,
		IdleTimeout:       s.IdleTimeout,
	}, log), nil
}

func buildHTTPGroups(routeName, defaultBalancer string, groups []config.BackendGroup) (map[string]proxyhttp.Balancer, error) {
	out := make(map[string]proxyhttp.Balancer, len(groups))
	for _, g := range groups {
		bal, err := buildBalancer(routeName+"/"+g.Name, g.EffectiveBalancer(defaultBalancer), g.Backends)
		if err != nil {
			return nil, err
		}
		out[g.Name] = bal
	}
	return out, nil
}
