package active

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/sanchey92/flowgate/internal/domain/model"
)

type Checker interface {
	Check(ctx context.Context, b *model.Backend) error
}

func NewChecker(protocol string, cfg *Config) Checker {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "http":
		return newHTTPChecker(cfg)
	default:
		return newTCPChecker()
	}
}

type tcpChecker struct {
	dialer net.Dialer
}

func newTCPChecker() Checker {
	return &tcpChecker{}
}

func (c *tcpChecker) Check(ctx context.Context, b *model.Backend) error {
	conn, err := c.dialer.DialContext(ctx, "tcp", b.Addr)
	if err != nil {
		return fmt.Errorf("active: tcp dial %s: %w", b.Addr, err)
	}
	if err := conn.Close(); err != nil {
		return fmt.Errorf("active: tcp close %s: %w", b.Addr, err)
	}
	return nil
}

type httpChecker struct {
	client         *http.Client
	path           string
	expectedStatus int
}

func newHTTPChecker(cfg *Config) Checker {
	return &httpChecker{
		client: &http.Client{
			Transport: &http.Transport{
				DisableKeepAlives: true,
			},
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		path:           cfg.Path,
		expectedStatus: cfg.ExpectedStatus,
	}
}

func (c *httpChecker) Check(ctx context.Context, b *model.Backend) error {
	url := "http://" + b.Addr + c.path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("active: build request %s: %w", url, err)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("active: http get %s: %w", url, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != c.expectedStatus {
		return fmt.Errorf("active: %s: status %d (want %d)",
			url, resp.StatusCode, c.expectedStatus)
	}
	return nil
}
