package proxyhttp

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	domainErr "github.com/sanchey92/flowgate/internal/domain/errors"
	"github.com/sanchey92/flowgate/internal/proxy/http/reqctx"
)

// roundTripperFunc позволяет подменить inner http.RoundTripper в тестах.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

// --- BuildTransport --------------------------------------------------------

func TestBuildTransport_AppliesDefaults(t *testing.T) {
	t.Parallel()
	tr := BuildTransport(TransportSettings{})

	assert.Equal(t, defaultMaxIdleConnsPerHost, tr.MaxIdleConnsPerHost)
	assert.Equal(t, defaultMaxConnsPerHost, tr.MaxConnsPerHost)
	assert.True(t, tr.DisableCompression)
	assert.False(t, tr.ForceAttemptHTTP2)
	assert.Nil(t, tr.Proxy)
	assert.NotNil(t, tr.DialContext)
}

func TestBuildTransport_NegativeLimitsFallBackToDefaults(t *testing.T) {
	t.Parallel()
	tr := BuildTransport(TransportSettings{
		MaxIdleConnsPerHost: -1,
		MaxConnsPerHost:     -10,
	})

	assert.Equal(t, defaultMaxIdleConnsPerHost, tr.MaxIdleConnsPerHost)
	assert.Equal(t, defaultMaxConnsPerHost, tr.MaxConnsPerHost)
}

func TestBuildTransport_CustomSettingsApplied(t *testing.T) {
	t.Parallel()
	s := TransportSettings{
		ResponseHeaderTimeout: 7 * time.Second,
		IdleConnTimeout:       11 * time.Second,
		MaxIdleConnsPerHost:   3,
		MaxConnsPerHost:       5,
	}
	tr := BuildTransport(s)

	assert.Equal(t, 7*time.Second, tr.ResponseHeaderTimeout)
	assert.Equal(t, 11*time.Second, tr.IdleConnTimeout)
	assert.Equal(t, 3, tr.MaxIdleConnsPerHost)
	assert.Equal(t, 5, tr.MaxConnsPerHost)
}

func TestBuildTransport_DialContextConnects(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()

	tr := BuildTransport(TransportSettings{ConnectTimeout: time.Second})

	conn, err := tr.DialContext(context.Background(), "tcp", ln.Addr().String())
	require.NoError(t, err)
	require.NotNil(t, conn)
	_ = conn.Close()
}

func TestBuildTransport_DialContextWrapsError(t *testing.T) {
	t.Parallel()
	tr := BuildTransport(TransportSettings{ConnectTimeout: 200 * time.Millisecond})

	// 127.0.0.1:1 — порт, на котором никто не слушает.
	conn, err := tr.DialContext(context.Background(), "tcp", "127.0.0.1:1")

	require.Error(t, err)
	assert.Nil(t, conn)
	assert.Contains(t, err.Error(), "http transport dial tcp/127.0.0.1:1:")
}

func TestBuildTransport_DialContextHonoursCancelledContext(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()

	tr := BuildTransport(TransportSettings{ConnectTimeout: time.Second})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	conn, err := tr.DialContext(ctx, "tcp", ln.Addr().String())

	require.Error(t, err)
	assert.Nil(t, conn)
	assert.Contains(t, err.Error(), "http transport dial")
}

// --- selectingTransport.RoundTrip ------------------------------------------

func TestSelectingTransport_PickErrorShortCircuits(t *testing.T) {
	t.Parallel()
	called := false
	tr := NewSelectingTransport(roundTripperFunc(func(*http.Request) (*http.Response, error) {
		called = true
		return nil, nil
	}))

	slot := &reqctx.RequestSlot{PickErr: domainErr.ErrNoBackends}
	r := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	r = r.WithContext(reqctx.WithSlot(r.Context(), slot))

	resp, err := tr.RoundTrip(r)

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.ErrorIs(t, err, domainErr.ErrNoBackends)
	assert.Contains(t, err.Error(), "http proxy pick:")
	assert.False(t, called, "inner RoundTripper must not be called when pick failed")
}

func TestSelectingTransport_NoSlotCallsInner(t *testing.T) {
	t.Parallel()
	want := &http.Response{StatusCode: http.StatusOK}
	tr := NewSelectingTransport(roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return want, nil
	}))

	r := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)

	resp, err := tr.RoundTrip(r)

	require.NoError(t, err)
	assert.Same(t, want, resp)
}

func TestSelectingTransport_SlotWithoutErrorCallsInner(t *testing.T) {
	t.Parallel()
	want := &http.Response{StatusCode: http.StatusTeapot}
	tr := NewSelectingTransport(roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return want, nil
	}))

	r := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	r = r.WithContext(reqctx.WithSlot(r.Context(), &reqctx.RequestSlot{}))

	resp, err := tr.RoundTrip(r)

	require.NoError(t, err)
	assert.Same(t, want, resp)
}

func TestSelectingTransport_WrapsInnerError(t *testing.T) {
	t.Parallel()
	innerErr := errors.New("dial boom")
	tr := NewSelectingTransport(roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, innerErr
	}))

	r := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)

	resp, err := tr.RoundTrip(r)

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.ErrorIs(t, err, innerErr)
	assert.Contains(t, err.Error(), "http proxy: round trip:")
}
