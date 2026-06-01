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

func TestLoad_AppliesDefaults(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "cfg.yaml")
	envPath := filepath.Join(dir, ".env")

	// No defaults block at all: cleanenv must fill every field from env-default.
	yaml := `env: test
routes:
  - name: r1
    protocol: tcp
    listen: ":18080"
    backends:
      - addr: "127.0.0.1:9001"
        weight: 1
`
	require.NoError(t, os.WriteFile(cfgPath, []byte(yaml), 0o600))
	require.NoError(t, os.WriteFile(envPath, []byte("CONFIG_PATH="+cfgPath+"\n"), 0o600))

	t.Setenv("CONFIG_PATH", cfgPath)

	cfg, err := config.Load(envPath)
	require.NoError(t, err)

	assert.Equal(t, 5*time.Second, cfg.Proxy.ConnectTimeout)
	assert.Equal(t, 60*time.Second, cfg.Proxy.IdleTimeout)
	assert.Equal(t, 1024, cfg.Proxy.MaxConns)
	assert.Equal(t, 60*time.Second, cfg.Proxy.HTTP.RequestTimeout)
	assert.Equal(t, 30*time.Second, cfg.Proxy.UDP.SessionIdle)
}
