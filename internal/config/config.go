package config

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/ilyakaznacheev/cleanenv"
	"github.com/joho/godotenv"
)

type Config struct {
	Env      string   `yaml:"env" env:"ENV" env-default:"dev"`
	LogLevel string   `yaml:"log_level" env:"LOG_LEVEL" env-default:"info"`
	Server   Server   `yaml:"server"`
	Defaults Defaults `yaml:"defaults"`
	Routes   []Route  `yaml:"routes"`
}

type Server struct {
	InstanceID      string        `yaml:"instance_id"`
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout" env-default:"10s"`
}

type Defaults struct {
	ConnectTimeout       time.Duration `yaml:"connect_timeout" env-default:"5s"`
	IdleTimeout          time.Duration `yaml:"idle_timeout" env-default:"60s"`
	KeepAlive            time.Duration `yaml:"keepalive" env-default:"30s"`
	MaxConns             int           `yaml:"max_conns" env-default:"1024"`
	BufSize              int           `yaml:"buf_size" env-default:"32768"`
	ProxyProtoHdrTimeout time.Duration `yaml:"proxyproto_header_timeout" env-default:"3s"`
	Backoff              Backoff       `yaml:"backoff"`
	UDP                  UDPDefaults   `yaml:"udp"`
	HTTP                 HTTPDefaults  `yaml:"http"`
}

type HTTPDefaults struct {
	ResponseHeaderTimeout time.Duration `yaml:"response_header_timeout" env-default:"30s"`
	WriteTimeout          time.Duration `yaml:"write_timeout" env-default:"0s"`
	ReadHeaderTimeout     time.Duration `yaml:"read_header_timeout" env-default:"5s"` // beyond task
}

type UDPDefaults struct {
	SessionIdle time.Duration `yaml:"session_idle" env-default:"30s"`
	BackendRead time.Duration `yaml:"backend_read" env-default:"5s"`
	Dial        time.Duration `yaml:"dial" env-default:"2s"`
}

type Backoff struct {
	Base time.Duration `yaml:"base" env-default:"100ms"`
	Max  time.Duration `yaml:"max" env-default:"5s"`
}

type Route struct {
	Name          string      `yaml:"name"`
	Protocol      string      `yaml:"protocol"`
	Listen        string      `yaml:"listen"`
	Balancer      string      `yaml:"balancer"`
	ProxyProtocol string      `yaml:"proxy_protocol"`
	Timeouts      Timeouts    `yaml:"timeouts"`
	Backends      []Backend   `yaml:"backends"`
	HTTP          *HTTPConfig `yaml:"http,omitempty"`
}

type Timeouts struct {
	ConnectTimeout       time.Duration `yaml:"connect_timeout"`
	IdleTimeout          time.Duration `yaml:"idle_timeout"`
	KeepAlive            time.Duration `yaml:"keepalive"`
	ProxyProtoHdrTimeout time.Duration `yaml:"proxyproto_header_timeout"`
	UDP                  UDPTimeouts   `yaml:"udp"`
}

type UDPTimeouts struct {
	SessionIdle time.Duration `yaml:"session_idle"`
	BackendRead time.Duration `yaml:"backend_read"`
	Dial        time.Duration `yaml:"dial"`
}

type Backend struct {
	Addr   string `yaml:"addr"`
	Weight int    `yaml:"weight"`
}

type Settings struct {
	ConnectTimeout       time.Duration
	IdleTimeout          time.Duration
	KeepAlive            time.Duration
	MaxConns             int
	BufSize              int
	ProxyProtoHdrTimeout time.Duration
	Backoff              Backoff
	UDP                  UDPSettings
}

type UDPSettings struct {
	SessionIdle time.Duration
	BackendRead time.Duration
	Dial        time.Duration
}

func (r Route) Effective(d Defaults) Settings {
	return Settings{
		ConnectTimeout:       firstNonZeroDur(r.Timeouts.ConnectTimeout, d.ConnectTimeout),
		IdleTimeout:          firstNonZeroDur(r.Timeouts.IdleTimeout, d.IdleTimeout),
		KeepAlive:            firstNonZeroDur(r.Timeouts.KeepAlive, d.KeepAlive),
		MaxConns:             d.MaxConns,
		BufSize:              d.BufSize,
		ProxyProtoHdrTimeout: firstNonZeroDur(r.Timeouts.ProxyProtoHdrTimeout, d.ProxyProtoHdrTimeout),
		Backoff:              d.Backoff,
		UDP: UDPSettings{
			SessionIdle: firstNonZeroDur(r.Timeouts.UDP.SessionIdle, d.UDP.SessionIdle),
			BackendRead: firstNonZeroDur(r.Timeouts.UDP.BackendRead, d.UDP.BackendRead),
			Dial:        firstNonZeroDur(r.Timeouts.UDP.Dial, d.UDP.Dial),
		},
	}
}

func Load(envFile string) (*Config, error) {
	if err := loadEnv(envFile); err != nil {
		return nil, fmt.Errorf("config: load env: %w", err)
	}

	cfgPath := os.Getenv("CONFIG_PATH")
	if cfgPath == "" {
		return nil, errors.New("config: CONFIG_PATH is not set")
	}

	var cfg Config
	if err := cleanenv.ReadConfig(cfgPath, &cfg); err != nil {
		return nil, fmt.Errorf("config: read %s: %w", cfgPath, err)
	}

	return &cfg, nil
}

func MustLoad(envFile string) *Config {
	cfg, err := Load(envFile)
	if err != nil {
		panic(err)
	}
	return cfg
}

func firstNonZeroDur(a, b time.Duration) time.Duration {
	if a > 0 {
		return a
	}
	return b
}

func loadEnv(envFile string) error {
	if err := godotenv.Load(envFile); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("godotenv load: %w", err)
	}
	return nil
}
