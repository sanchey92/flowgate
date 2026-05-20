package proxyhttp

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/sanchey92/flowgate/internal/proxy/http/reqctx"
)

const (
	defaultMaxIdleConnsPerHost = 64
	defaultMaxConnsPerHost     = 256
)

type TransportSettings struct {
	ConnectTimeout        time.Duration
	ResponseHeaderTimeout time.Duration
	IdleConnTimeout       time.Duration
	KeepAlivePeriod       time.Duration
	MaxIdleConnsPerHost   int
	MaxConnsPerHost       int
}

func BuildTransport(s TransportSettings) *http.Transport {
	dialer := &net.Dialer{
		Timeout:   s.ConnectTimeout,
		KeepAlive: s.KeepAlivePeriod,
	}

	maxIdle := s.MaxIdleConnsPerHost
	if maxIdle <= 0 {
		maxIdle = defaultMaxIdleConnsPerHost
	}

	maxConns := s.MaxConnsPerHost
	if maxConns <= 0 {
		maxConns = defaultMaxConnsPerHost
	}

	return &http.Transport{
		Proxy: nil,

		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			c, err := dialer.DialContext(ctx, network, addr)
			if err != nil {
				return nil, fmt.Errorf("http transport dial %s/%s: %w", network, addr, err)
			}
			return c, nil
		},

		ResponseHeaderTimeout: s.ResponseHeaderTimeout,

		IdleConnTimeout: s.IdleConnTimeout,

		MaxIdleConnsPerHost: maxIdle,

		MaxConnsPerHost: maxConns,

		DisableCompression: true,

		ForceAttemptHTTP2: false,
	}
}

type SelectingTransport struct {
	inner http.RoundTripper
}

func NewSelectingTransport(inner http.RoundTripper) *SelectingTransport {
	return &SelectingTransport{inner: inner}
}

func (t *SelectingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if s := reqctx.SlotFrom(r.Context()); s != nil && s.PickErr != nil {
		return nil, fmt.Errorf("http proxy pick: %w", s.PickErr)
	}
	resp, err := t.inner.RoundTrip(r)
	if err != nil {
		return nil, fmt.Errorf("http proxy: round trip: %w", err)
	}
	return resp, nil
}
