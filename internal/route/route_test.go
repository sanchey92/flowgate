package route

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sanchey92/flowgate/internal/config"
)

func testLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func testDefaults() config.Proxy {
	return config.Proxy{
		ConnectTimeout:       time.Second,
		IdleTimeout:          3 * time.Second,
		KeepAlive:            30 * time.Second,
		MaxConns:             1024,
		BufSize:              32768,
		ProxyProtoHdrTimeout: time.Second,
		Backoff:              config.Backoff{Base: 20 * time.Millisecond, Max: 200 * time.Millisecond},
	}
}

// tcpRoute is a minimal route that proxy.New can build without binding a socket
// (Assemble never starts the runner).
func tcpRoute() config.Route {
	return config.Route{
		Name:     "r1",
		Protocol: "tcp",
		Listen:   "127.0.0.1:0",
		Balancer: "round_robin",
		Backends: []config.Backend{{Addr: "127.0.0.1:9001", Weight: 1}},
	}
}

func TestAssemble_NoHealthChecks(t *testing.T) {
	rt, err := Assemble(tcpRoute(), testDefaults(), testLog())
	require.NoError(t, err)

	require.Equal(t, "r1", rt.Name)
	require.Equal(t, "tcp", rt.Protocol)
	require.NotNil(t, rt.Addr, "Addr thunk must be set for the app to log the bound address")
	require.Len(t, rt.Backends, 1)
	require.Len(t, rt.Lifecycles, 1, "only the proxy runner has a lifecycle")

	// Without a breaker attached, failures must not take the backend out of rotation.
	b := rt.Backends[0]
	for i := 0; i < 20; i++ {
		b.Observe(true)
	}
	require.True(t, b.Available(), "backend stays available when no passive breaker is attached")
}

func TestAssemble_PassiveAttachesBreaker(t *testing.T) {
	r := tcpRoute()
	r.HealthCheck = &config.HealthCheckConfig{
		Passive: config.PassiveCheckConfig{
			Enabled:          true,
			ErrorThreshold:   3,
			Window:           time.Minute,
			RecoveryInterval: time.Minute,
		},
	}

	rt, err := Assemble(r, testDefaults(), testLog())
	require.NoError(t, err)
	require.Len(t, rt.Lifecycles, 1, "passive attaches a breaker but adds no lifecycle")
	require.Len(t, rt.Backends, 1)

	b := rt.Backends[0]
	require.True(t, b.Available(), "healthy before failures")

	for i := 0; i < 3; i++ { // reach ErrorThreshold
		b.Observe(true)
	}
	require.False(t, b.Available(), "breaker opens at threshold and pulls the backend out")
}

func TestAssemble_ActiveAddsLifecycle(t *testing.T) {
	r := tcpRoute()
	r.HealthCheck = &config.HealthCheckConfig{
		Active: config.ActiveCheckConfig{
			Enabled:            true,
			Interval:           10 * time.Second,
			Timeout:            time.Second,
			UnhealthyThreshold: 3,
			HealthyThreshold:   2,
		},
	}

	rt, err := Assemble(r, testDefaults(), testLog())
	require.NoError(t, err)
	require.Len(t, rt.Lifecycles, 2, "active health scheduler adds a second lifecycle")
}
