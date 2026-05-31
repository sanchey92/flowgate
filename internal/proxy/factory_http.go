package proxy

import (
	"fmt"
	"log/slog"

	"github.com/sanchey92/flowgate/internal/config"
	"github.com/sanchey92/flowgate/internal/domain/model"
	"github.com/sanchey92/flowgate/internal/pool"
	proxyhttp "github.com/sanchey92/flowgate/internal/proxy/http"
	"github.com/sanchey92/flowgate/internal/proxy/http/router"
)

var _ Runner = (*proxyhttp.Runner)(nil)

func newHTTP(r config.Route, defaults config.Defaults, s config.Settings, log *slog.Logger) (Built, error) {
	if r.HTTP == nil {
		return Built{}, fmt.Errorf("proxy: route %q: http config is required", r.Name)
	}

	groups, backends, err := buildHTTPGroups(r.Name, r.Balancer, r.HTTP.BackendGroups)
	if err != nil {
		return Built{}, err
	}

	rt, err := router.Build(r.HTTP.RoutingRules, log)
	if err != nil {
		return Built{}, fmt.Errorf("proxy: route %q: router: %w", r.Name, err)
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
		httpSet.RequestTimeout,
		log,
	)

	return Built{
		Runner: proxyhttp.NewRunner(r.Name, r.Listen, p, proxyhttp.RunnerSettings{
			ReadHeaderTimeout: httpSet.ReadHeaderTimeout,
			WriteTimeout:      httpSet.WriteTimeout,
			IdleTimeout:       s.IdleTimeout,
		}, log),
		Backends: backends,
	}, nil
}

func buildHTTPGroups(
	routeName, defaultBalancer string,
	groups []config.BackendGroup,
) (map[string]proxyhttp.Balancer, []*model.Backend, error) {
	out := make(map[string]proxyhttp.Balancer, len(groups))
	var all []*model.Backend
	for _, g := range groups {
		bal, models, err := buildBalancer(routeName+"/"+g.Name, g.EffectiveBalancer(defaultBalancer), g.Backends)
		if err != nil {
			return nil, nil, err
		}
		out[g.Name] = bal
		all = append(all, models...)
	}
	return out, all, nil
}
