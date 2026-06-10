package retry

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"syscall"
	"time"
)

type condKind uint8

const maxRetryBodyBytes = 64 << 10

const (
	condConnError condKind = 1 << iota
	condTimeout
	condReset
	cond502
	cond503
	cond504
)

var idempotentMethods = map[string]struct{}{
	http.MethodGet:     {},
	http.MethodHead:    {},
	http.MethodPut:     {},
	http.MethodDelete:  {},
	http.MethodOptions: {},
	http.MethodTrace:   {},
}

type Policy struct {
	enabled        bool
	maxRetries     int
	conditions     condKind
	backoffBase    time.Duration
	backoffMax     time.Duration
	idempotentOnly bool
	retryOnPost    bool
}

func CompileRetryPolicy(
	enabled bool,
	maxRetries int,
	conditions []string,
	backoffBase time.Duration,
	backoffMax time.Duration,
	idempotentOnly bool,
	retryOnPost bool,
) (*Policy, error) {
	conds, err := parseConditions(conditions)
	if err != nil {
		return &Policy{}, fmt.Errorf("compile retry policy: %w", err)
	}
	return &Policy{
		enabled:        enabled,
		maxRetries:     maxRetries,
		conditions:     conds,
		backoffBase:    backoffBase,
		backoffMax:     backoffMax,
		idempotentOnly: idempotentOnly,
		retryOnPost:    retryOnPost,
	}, nil
}

func parseConditions(values []string) (condKind, error) {
	var c condKind
	for _, v := range values {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "connection_error":
			c |= condConnError
		case "timeout":
			c |= condTimeout
		case "reset":
			c |= condReset
		case "502":
			c |= cond502
		case "503":
			c |= cond503
		case "504":
			c |= cond504
		default:
			return 0, fmt.Errorf("unknown retry condition %q", v)
		}
	}
	return c, nil
}

func (p *Policy) MethodAllowsRetry(r *http.Request) bool {
	if !p.idempotentOnly {
		return true
	}

	if _, ok := idempotentMethods[r.Method]; ok {
		return true
	}

	if r.Header.Get("Idempotency-Key") != "" {
		return true
	}

	if r.Method == http.MethodPost {
		return p.retryOnPost
	}

	return false
}

func (p *Policy) RetryableOutcome(resp *http.Response, err error) bool {
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return false
		}
		return p.conditions&p.retryableErrKind(err) != 0
	}

	if resp == nil {
		return false
	}

	switch resp.StatusCode {
	case http.StatusBadGateway:
		return p.conditions&cond502 != 0
	case http.StatusServiceUnavailable:
		return p.conditions&cond503 != 0
	case http.StatusGatewayTimeout:
		return p.conditions&cond504 != 0
	default:
		return false
	}
}

func (p *Policy) retryableErrKind(err error) condKind {
	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		return condConnError
	case errors.Is(err, syscall.ECONNRESET):
		return condReset
	}
	if netErr, ok := errors.AsType[net.Error](err); ok && netErr.Timeout() {
		return condTimeout
	}

	return condConnError
}

func EnsureReplayableBody(r *http.Request) (bool, error) {
	if r.Body == nil || r.Body == http.NoBody {
		return true, nil
	}
	if r.GetBody != nil {
		return true, nil
	}
	if r.ContentLength < 0 || r.ContentLength > maxRetryBodyBytes {
		return false, nil
	}

	buf, err := io.ReadAll(r.Body)
	if err != nil {
		return false, fmt.Errorf("read request body: %w", err)
	}
	_ = r.Body.Close()

	r.Body = io.NopCloser(bytes.NewReader(buf))
	r.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(buf)), nil
	}

	return true, nil
}

func (p *Policy) Enabled() bool {
	return p.enabled
}

func (p *Policy) MaxRetries() int {
	return p.maxRetries
}
