package config_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sanchey92/flowgate/internal/config"
)

func defaults() config.Defaults {
	return config.Defaults{
		ConnectTimeout:       5 * time.Second,
		IdleTimeout:          60 * time.Second,
		KeepAlive:            30 * time.Second,
		MaxConns:             1024,
		BufSize:              32768,
		ProxyProtoHdrTimeout: 3 * time.Second,
		Backoff:              config.Backoff{Base: 100 * time.Millisecond, Max: 5 * time.Second},
		UDP: config.UDPDefaults{
			SessionIdle: 30 * time.Second,
			BackendRead: 5 * time.Second,
			Dial:        2 * time.Second,
		},
	}
}

func TestRoute_Effective_FallsBackToDefaults(t *testing.T) {
	d := defaults()
	r := config.Route{Protocol: "tcp"}
	got := r.Effective(d)

	assert.Equal(t, d.ConnectTimeout, got.ConnectTimeout)
	assert.Equal(t, d.IdleTimeout, got.IdleTimeout)
	assert.Equal(t, d.KeepAlive, got.KeepAlive)
	assert.Equal(t, d.ProxyProtoHdrTimeout, got.ProxyProtoHdrTimeout)
	assert.Equal(t, d.MaxConns, got.MaxConns)
	assert.Equal(t, d.BufSize, got.BufSize)
	assert.Equal(t, d.Backoff, got.Backoff)
	assert.Equal(t, d.UDP.SessionIdle, got.UDP.SessionIdle)
	assert.Equal(t, d.UDP.BackendRead, got.UDP.BackendRead)
	assert.Equal(t, d.UDP.Dial, got.UDP.Dial)
}

func TestRoute_Effective_OverridesNonZero(t *testing.T) {
	d := defaults()
	r := config.Route{
		Protocol: "tcp",
		Timeouts: config.Timeouts{
			ConnectTimeout:       1 * time.Second,
			IdleTimeout:          2 * time.Second,
			ProxyProtoHdrTimeout: 7 * time.Second,
			UDP: config.UDPTimeouts{
				SessionIdle: 99 * time.Second,
			},
		},
	}
	got := r.Effective(d)

	assert.Equal(t, 1*time.Second, got.ConnectTimeout)
	assert.Equal(t, 2*time.Second, got.IdleTimeout)
	assert.Equal(t, d.KeepAlive, got.KeepAlive)
	assert.Equal(t, 7*time.Second, got.ProxyProtoHdrTimeout)
	assert.Equal(t, 99*time.Second, got.UDP.SessionIdle)
	assert.Equal(t, d.UDP.BackendRead, got.UDP.BackendRead)
	assert.Equal(t, d.UDP.Dial, got.UDP.Dial)
}

func TestLoad_ReadsYAML(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "cfg.yaml")
	envPath := filepath.Join(dir, ".env")

	yaml := `env: test
log_level: info
server:
  instance_id: fg-test
  shutdown_timeout: 5s
routes:
  - name: r1
    protocol: tcp
    listen: ":18080"
    balancer: round_robin
    backends:
      - addr: "127.0.0.1:9001"
        weight: 1
`
	require.NoError(t, os.WriteFile(cfgPath, []byte(yaml), 0o600))
	require.NoError(t, os.WriteFile(envPath, []byte("CONFIG_PATH="+cfgPath+"\n"), 0o600))

	t.Setenv("CONFIG_PATH", cfgPath)

	cfg, err := config.Load(envPath)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.Equal(t, "test", cfg.Env)
	assert.Equal(t, "fg-test", cfg.Server.InstanceID)
	require.Len(t, cfg.Routes, 1)
	assert.Equal(t, "r1", cfg.Routes[0].Name)
	assert.Equal(t, "round_robin", cfg.Routes[0].Balancer)
}
