package retry_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sanchey92/flowgate/internal/proxy/http/retry"
)

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return false }

func newPolicy(t *testing.T, conditions []string, idempotentOnly, retryOnPost bool) *retry.Policy {
	t.Helper()
	p, err := retry.CompileRetryPolicy(
		true, 2, conditions,
		100*time.Millisecond, 2*time.Second,
		idempotentOnly, retryOnPost,
	)
	require.NoError(t, err)
	return p
}

func TestRetryableOutcome_ErrorConditionsMasked(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		conditions []string
		err        error
		want       bool
	}{
		{"refused not in conditions", []string{"503"}, syscall.ECONNREFUSED, false},
		{"refused in conditions", []string{"connection_error"}, syscall.ECONNREFUSED, true},
		{"reset not in conditions", []string{"connection_error"}, syscall.ECONNRESET, false},
		{"reset in conditions", []string{"reset"}, syscall.ECONNRESET, true},
		{"timeout not in conditions", []string{"connection_error"}, timeoutErr{}, false},
		{"timeout in conditions", []string{"timeout"}, timeoutErr{}, true},
		{"unknown error as connection_error", []string{"connection_error"}, errors.New("boom"), true},
		{"unknown error masked out", []string{"timeout"}, errors.New("boom"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := newPolicy(t, tt.conditions, true, false)
			wrapped := fmt.Errorf("round trip: %w", tt.err)
			assert.Equal(t, tt.want, p.RetryableOutcome(nil, wrapped))
		})
	}
}

func TestRetryableOutcome_ContextErrors(t *testing.T) {
	t.Parallel()
	p := newPolicy(t, []string{"connection_error", "reset", "timeout"}, true, false)

	assert.False(t, p.RetryableOutcome(nil, context.Canceled))
	assert.False(t, p.RetryableOutcome(nil, context.DeadlineExceeded))
}

func TestRetryableOutcome_Statuses(t *testing.T) {
	t.Parallel()
	p := newPolicy(t, []string{"502", "504"}, true, false)

	resp := func(code int) *http.Response { return &http.Response{StatusCode: code} }

	assert.True(t, p.RetryableOutcome(resp(http.StatusBadGateway), nil))
	assert.False(t, p.RetryableOutcome(resp(http.StatusServiceUnavailable), nil))
	assert.True(t, p.RetryableOutcome(resp(http.StatusGatewayTimeout), nil))
	assert.False(t, p.RetryableOutcome(resp(http.StatusInternalServerError), nil))
}

func TestMethodAllowsRetry(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		method         string
		idempotencyKey string
		idempotentOnly bool
		retryOnPost    bool
		want           bool
	}{
		{name: "GET with idempotent_only", method: http.MethodGet, idempotentOnly: true, want: true},
		{name: "POST with idempotent_only", method: http.MethodPost, idempotentOnly: true, want: false},
		{name: "POST with retry_on_post", method: http.MethodPost, idempotentOnly: true, retryOnPost: true, want: true},
		{name: "PATCH with idempotent_only", method: http.MethodPatch, idempotentOnly: true, want: false},
		{name: "PATCH with Idempotency-Key", method: http.MethodPatch, idempotencyKey: "k1", idempotentOnly: true, want: true},
		{name: "POST without idempotent_only", method: http.MethodPost, idempotentOnly: false, want: true},
		{name: "PATCH without idempotent_only", method: http.MethodPatch, idempotentOnly: false, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := newPolicy(t, nil, tt.idempotentOnly, tt.retryOnPost)
			r := httptest.NewRequest(tt.method, "http://example.com/", nil)
			if tt.idempotencyKey != "" {
				r.Header.Set("Idempotency-Key", tt.idempotencyKey)
			}
			assert.Equal(t, tt.want, p.MethodAllowsRetry(r))
		})
	}
}

func TestBackoffDelay_ZeroBase(t *testing.T) {
	t.Parallel()
	p, err := retry.CompileRetryPolicy(true, 2, nil, 0, 2*time.Second, true, false)
	require.NoError(t, err)

	assert.Equal(t, time.Duration(0), p.BackoffDelay(0))
}

func TestBackoffDelay_CappedByMax(t *testing.T) {
	t.Parallel()
	base, maxDelay := 100*time.Millisecond, 300*time.Millisecond
	p, err := retry.CompileRetryPolicy(true, 10, nil, base, maxDelay, true, false)
	require.NoError(t, err)

	for attempt := 0; attempt < 10; attempt++ {
		d := p.BackoffDelay(attempt)
		assert.GreaterOrEqual(t, d, time.Duration(0))
		assert.LessOrEqual(t, d, maxDelay)
	}
}
