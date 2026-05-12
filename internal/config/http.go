package config

import (
	"time"
)

type HTTPConfig struct {
	BackendGroups   []BackendGroup        `yaml:"backend_groups"`
	RoutingRules    []RoutingRule         `yaml:"routing_rules"`
	HeaderRules     HeaderRules           `yaml:"headers"`
	StandardHeaders StandardHeadersConfig `yaml:"standard_headers"`
	Timeouts        HTTPTimeouts          `yaml:"timeouts"`
	WebSocket       WebSocketConfig       `yaml:"websocket"`
}

type BackendGroup struct {
	Name     string    `yaml:"name"`
	Balancer string    `yaml:"balancer"`
	Backends []Backend `yaml:"backends"`
}

func (g BackendGroup) EffectiveBalancer(routeBalancer string) string {
	if g.Balancer != "" {
		return g.Balancer
	}
	return routeBalancer
}

type RoutingRule struct {
	Match        MatchCondition `yaml:"match"`
	BackendGroup string         `yaml:"backend_group"`
}

type MatchCondition struct {
	Host        string            `yaml:"host"`
	PathExact   string            `yaml:"path_exact"`
	PathPrefix  string            `yaml:"path_prefix"`
	PathRegex   string            `yaml:"path_regex"`
	Headers     map[string]string `yaml:"headers"`
	QueryParams map[string]string `yaml:"query_params"`
}

func (m *MatchCondition) IsDefault() bool {
	return m.PathPrefix == "/" &&
		m.Host == "" &&
		m.PathExact == "" &&
		m.PathRegex == "" &&
		len(m.Headers) == 0 &&
		len(m.QueryParams) == 0
}

type HeaderRules struct {
	Request  HeaderOp `yaml:"request"`
	Response HeaderOp `yaml:"response"`
}

type HeaderOp struct {
	Add     map[string]string `yaml:"add"`
	Remove  []string          `yaml:"remove"`
	Replace map[string]string `yaml:"replace"`
}

type StandardHeadersConfig struct {
	Enabled        bool `yaml:"enabled" env-default:"true"`
	ForwardedFor   bool `yaml:"forwarded_for"`
	RealIP         bool `yaml:"real_ip"`
	ForwardedProto bool `yaml:"forwarded_proto"`
	ForwardedHost  bool `yaml:"forwarded_host"`
	RequestID      bool `yaml:"request_id"`
}

func (s StandardHeadersConfig) EffectiveSet() StandardHeadersConfig {
	if !s.Enabled {
		return s
	}
	if s.ForwardedFor || s.RealIP || s.ForwardedProto || s.ForwardedHost || s.RequestID {
		return s
	}
	return StandardHeadersConfig{
		Enabled:        true,
		ForwardedFor:   true,
		RealIP:         true,
		ForwardedProto: true,
		ForwardedHost:  true,
		RequestID:      true,
	}
}

type HTTPTimeouts struct {
	ResponseHeaderTimeout time.Duration `yaml:"response_header_timeout"`
	WriteTimeout          time.Duration `yaml:"write_timeout"`
	ReadHeaderTimeout     time.Duration `yaml:"read_header_timeout"`
}

type HTTPSettings struct {
	ResponseHeaderTimeout time.Duration
	WriteTimeout          time.Duration
	ReadHeaderTimeout     time.Duration
}

func (h HTTPConfig) EffectiveTimeouts(d HTTPDefaults) HTTPSettings {
	return HTTPSettings{
		ResponseHeaderTimeout: firstNonZeroDur(h.Timeouts.ResponseHeaderTimeout, d.ResponseHeaderTimeout),
		WriteTimeout:          firstNonZeroDur(h.Timeouts.WriteTimeout, d.WriteTimeout),
		ReadHeaderTimeout:     firstNonZeroDur(h.Timeouts.ReadHeaderTimeout, d.ReadHeaderTimeout),
	}
}

type WebSocketConfig struct {
	Enabled bool `yaml:"enabled" env-default:"true"` // ⚠ см. Приложение Г
}
