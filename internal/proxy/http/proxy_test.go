package proxyhttp

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sanchey92/flowgate/internal/config"
	domainErr "github.com/sanchey92/flowgate/internal/domain/errors"
	"github.com/sanchey92/flowgate/internal/domain/model"
	"github.com/sanchey92/flowgate/internal/pool"
	"github.com/sanchey92/flowgate/internal/proxy/http/mocks"
	"github.com/sanchey92/flowgate/internal/proxy/http/reqctx"
	"github.com/sanchey92/flowgate/internal/proxy/http/router"
)

// --- helpers ---------------------------------------------------------------

func newTestLogger() (*slog.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	h := slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	return slog.New(h), buf
}

func buildRouter(t *testing.T, rules ...config.RoutingRule) *router.Router {
	t.Helper()
	r, err := router.Build(rules, nil)
	require.NoError(t, err)
	return r
}

// fallbackRouter маршрутизирует любой запрос в group.
func fallbackRouter(t *testing.T, group string) *router.Router {
	t.Helper()
	return buildRouter(t, config.RoutingRule{
		Match:        config.MatchCondition{PathPrefix: "/"},
		BackendGroup: group,
	})
}

func newProxy(t *testing.T, r *router.Router, groups map[string]Balancer) (*Proxy, *bytes.Buffer) {
	t.Helper()
	log, buf := newTestLogger()
	tr := BuildTransport(TransportSettings{})
	p := New(r, groups, tr, pool.NewBufferPool(4096), config.HeaderRules{}, config.StandardHeadersConfig{}, log)
	return p, buf
}

// newProxyRequest собирает httputil.ProxyRequest со слотом в контексте Out,
// как это делает httputil.ReverseProxy перед вызовом rewrite.
func newProxyRequest(t *testing.T, target string, slot *reqctx.RequestSlot) *httputil.ProxyRequest {
	t.Helper()
	in := httptest.NewRequest(http.MethodGet, target, nil)
	out := in.Clone(in.Context())
	if slot != nil {
		out = out.WithContext(reqctx.WithSlot(out.Context(), slot))
	}
	return &httputil.ProxyRequest{In: in, Out: out}
}

// --- New -------------------------------------------------------------------

func TestNew_WiresReverseProxy(t *testing.T) {
	t.Parallel()
	r := fallbackRouter(t, "g1")
	tr := BuildTransport(TransportSettings{})
	log, _ := newTestLogger()

	p := New(r, map[string]Balancer{}, tr, pool.NewBufferPool(1024), config.HeaderRules{}, config.StandardHeadersConfig{}, log)

	require.NotNil(t, p)
	assert.Same(t, tr, p.transport)
	require.NotNil(t, p.rp)
	assert.NotNil(t, p.rp.Rewrite)
	assert.NotNil(t, p.rp.Transport)
	assert.NotNil(t, p.rp.BufferPool)
	assert.NotNil(t, p.rp.ErrorHandler)
	assert.NotNil(t, p.rp.ModifyResponse)
}

// --- ensureRequestID -------------------------------------------------------

func TestEnsureRequestID_GeneratesWithoutMutatingHeader(t *testing.T) {
	t.Parallel()
	r := httptest.NewRequest(http.MethodGet, "/", nil)

	id := ensureRequestID(r)

	assert.NotEmpty(t, id)
	// ensureRequestID не должен модифицировать incoming Header: иначе при
	// отключённом StandardHeaders.RequestID наш ID всё равно утечёт апстриму.
	assert.Empty(t, r.Header.Get(requestIDHeader))
}

func TestEnsureRequestID_KeepsExisting(t *testing.T) {
	t.Parallel()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set(requestIDHeader, "preset-id")

	id := ensureRequestID(r)

	assert.Equal(t, "preset-id", id)
	assert.Equal(t, "preset-id", r.Header.Get(requestIDHeader))
}

func TestEnsureRequestID_DifferentRequestsGetDifferentIDs(t *testing.T) {
	t.Parallel()
	a := ensureRequestID(httptest.NewRequest(http.MethodGet, "/", nil))
	b := ensureRequestID(httptest.NewRequest(http.MethodGet, "/", nil))

	assert.NotEqual(t, a, b)
}

// --- rewrite ---------------------------------------------------------------

func TestRewrite_NoSlotIsNoop(t *testing.T) {
	t.Parallel()
	p, _ := newProxy(t, fallbackRouter(t, "g1"), map[string]Balancer{})
	pr := newProxyRequest(t, "http://example.com/foo", nil)

	assert.NotPanics(t, func() { p.rewrite(pr) })
}

func TestRewrite_Success(t *testing.T) {
	t.Parallel()
	backend := model.NewBackend("10.0.0.1:8080", 1, 0)

	bal := mocks.NewBalancer(t)
	bal.EXPECT().Pick().Return(backend, nil)

	p, _ := newProxy(t, fallbackRouter(t, "g1"), map[string]Balancer{"g1": bal})

	slot := &reqctx.RequestSlot{}
	pr := newProxyRequest(t, "http://example.com/foo", slot)
	p.rewrite(pr)

	assert.NoError(t, slot.PickErr)
	assert.Equal(t, "g1", slot.Group)
	assert.Same(t, backend, slot.Backend)
	assert.Equal(t, "http", pr.Out.URL.Scheme)
	assert.Equal(t, "10.0.0.1:8080", pr.Out.URL.Host)
}

func TestRewrite_NoRoute(t *testing.T) {
	t.Parallel()
	// Маршрутизатор без fallback: запрос на чужой хост никуда не попадёт.
	r := buildRouter(t, config.RoutingRule{
		Match:        config.MatchCondition{Host: "specific.com"},
		BackendGroup: "g1",
	})
	p, _ := newProxy(t, r, map[string]Balancer{})

	slot := &reqctx.RequestSlot{}
	pr := newProxyRequest(t, "http://other.com/foo", slot)
	p.rewrite(pr)

	assert.ErrorIs(t, slot.PickErr, domainErr.ErrNoRoute)
	assert.Nil(t, slot.Backend)
}

func TestRewrite_UnknownGroup(t *testing.T) {
	t.Parallel()
	p, _ := newProxy(t, fallbackRouter(t, "ghost"), map[string]Balancer{})

	slot := &reqctx.RequestSlot{}
	pr := newProxyRequest(t, "http://example.com/foo", slot)
	p.rewrite(pr)

	assert.ErrorIs(t, slot.PickErr, domainErr.ErrUnknownGroup)
	assert.Contains(t, slot.PickErr.Error(), "ghost")
	assert.Equal(t, "ghost", slot.Group)
}

func TestRewrite_PickError(t *testing.T) {
	t.Parallel()
	bal := mocks.NewBalancer(t)
	bal.EXPECT().Pick().Return(nil, domainErr.ErrNoBackends)

	p, _ := newProxy(t, fallbackRouter(t, "g1"), map[string]Balancer{"g1": bal})

	slot := &reqctx.RequestSlot{}
	pr := newProxyRequest(t, "http://example.com/foo", slot)
	p.rewrite(pr)

	assert.ErrorIs(t, slot.PickErr, domainErr.ErrNoBackends)
	assert.Nil(t, slot.Backend)
}

// Pick нарушает контракт: возвращает backend вместе с ошибкой.
// rewrite не должен записывать backend в слот — иначе defer вызовет Release
// на бэкенде, который фактически не был выдан.
func TestRewrite_PickReturnsBackendWithError(t *testing.T) {
	t.Parallel()
	backend := model.NewBackend("10.0.0.2:9090", 1, 0)

	bal := mocks.NewBalancer(t)
	bal.EXPECT().Pick().Return(backend, domainErr.ErrNoBackends)

	p, _ := newProxy(t, fallbackRouter(t, "g1"), map[string]Balancer{"g1": bal})

	slot := &reqctx.RequestSlot{}
	pr := newProxyRequest(t, "http://example.com/foo", slot)
	p.rewrite(pr)

	assert.ErrorIs(t, slot.PickErr, domainErr.ErrNoBackends)
	assert.Nil(t, slot.Backend)
}

// Pick нарушает контракт: err == nil, но backend пуст.
func TestRewrite_PickReturnsNilBackend(t *testing.T) {
	t.Parallel()
	bal := mocks.NewBalancer(t)
	bal.EXPECT().Pick().Return(nil, nil)

	p, _ := newProxy(t, fallbackRouter(t, "g1"), map[string]Balancer{"g1": bal})

	slot := &reqctx.RequestSlot{}
	pr := newProxyRequest(t, "http://example.com/foo", slot)
	p.rewrite(pr)

	assert.ErrorIs(t, slot.PickErr, domainErr.ErrNilBackend)
	assert.Nil(t, slot.Backend)
}

// --- handleError -----------------------------------------------------------

func TestHandleError_MapsStatuses(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"no route", domainErr.ErrNoRoute, http.StatusNotFound},
		{"no backends", domainErr.ErrNoBackends, http.StatusServiceUnavailable},
		{"unknown group", domainErr.ErrUnknownGroup, http.StatusBadGateway},
		{"nil backend", domainErr.ErrNilBackend, http.StatusBadGateway},
		{"generic", errors.New("boom"), http.StatusBadGateway},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p, _ := newProxy(t, fallbackRouter(t, "g1"), map[string]Balancer{})
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodGet, "http://example.com/p", nil)

			p.handleError(w, r, tc.err)

			assert.Equal(t, tc.want, w.Code)
		})
	}
}

func TestHandleError_LogsBackendFromSlot(t *testing.T) {
	t.Parallel()
	p, buf := newProxy(t, fallbackRouter(t, "g1"), map[string]Balancer{})

	slot := &reqctx.RequestSlot{Backend: model.NewBackend("10.1.1.1:80", 1, 0)}
	r := httptest.NewRequest(http.MethodGet, "http://example.com/p", nil)
	r = r.WithContext(reqctx.WithSlot(r.Context(), slot))
	w := httptest.NewRecorder()

	p.handleError(w, r, domainErr.ErrNoBackends)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	logged := buf.String()
	assert.Contains(t, logged, "upstream error")
	assert.Contains(t, logged, "10.1.1.1:80")
}

// --- modifyResponse --------------------------------------------------------

func TestModifyResponse_ReturnsNil(t *testing.T) {
	t.Parallel()
	p, _ := newProxy(t, fallbackRouter(t, "g1"), map[string]Balancer{})
	assert.NoError(t, p.modifyResponse(&http.Response{StatusCode: http.StatusOK}))
}

// --- accessLog -------------------------------------------------------------

func TestAccessLog_InfoForSuccess(t *testing.T) {
	t.Parallel()
	p, buf := newProxy(t, fallbackRouter(t, "g1"), map[string]Balancer{})

	slot := &reqctx.RequestSlot{RequestID: "rid-1", Group: "g1"}
	rec := &recorder{ResponseWriter: httptest.NewRecorder(), status: http.StatusOK, bytesOut: 42}
	r := httptest.NewRequest(http.MethodGet, "http://example.com/p", nil)

	p.accessLog(r, slot, rec, 5*time.Millisecond)

	logged := buf.String()
	assert.Contains(t, logged, "level=INFO")
	assert.Contains(t, logged, "http access")
	assert.Contains(t, logged, "rid-1")
	assert.Contains(t, logged, "bytes_out=42")
}

func TestAccessLog_WarnForServerError(t *testing.T) {
	t.Parallel()
	p, buf := newProxy(t, fallbackRouter(t, "g1"), map[string]Balancer{})

	slot := &reqctx.RequestSlot{RequestID: "rid-2", Group: "g1", PickErr: domainErr.ErrNoBackends}
	rec := &recorder{ResponseWriter: httptest.NewRecorder(), status: http.StatusBadGateway}
	r := httptest.NewRequest(http.MethodGet, "http://example.com/p", nil)

	p.accessLog(r, slot, rec, time.Millisecond)

	logged := buf.String()
	assert.Contains(t, logged, "level=WARN")
	assert.Contains(t, logged, "pick_error")
}

func TestAccessLog_SkipsBytesInForChunked(t *testing.T) {
	t.Parallel()
	p, buf := newProxy(t, fallbackRouter(t, "g1"), map[string]Balancer{})

	slot := &reqctx.RequestSlot{RequestID: "rid-3"}
	rec := &recorder{ResponseWriter: httptest.NewRecorder(), status: http.StatusOK}
	r := httptest.NewRequest(http.MethodGet, "http://example.com/p", nil)
	r.ContentLength = -1 // неизвестная длина (chunked)

	p.accessLog(r, slot, rec, time.Millisecond)

	assert.NotContains(t, buf.String(), "bytes_in")
}

// --- ServeHTTP (интеграция) ------------------------------------------------

func TestServeHTTP_ProxiesToBackend(t *testing.T) {
	t.Parallel()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "hello from backend")
	}))
	defer upstream.Close()

	addr := strings.TrimPrefix(upstream.URL, "http://")
	backend := model.NewBackend(addr, 1, 0)

	bal := mocks.NewBalancer(t)
	bal.EXPECT().Pick().Return(backend, nil)
	bal.EXPECT().Release(backend).Return()

	p, buf := newProxy(t, fallbackRouter(t, "g1"), map[string]Balancer{"g1": bal})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "http://example.com/foo", nil)
	r.Header.Set(requestIDHeader, "trace-123")

	p.ServeHTTP(w, r)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "hello from backend", w.Body.String())
	assert.Contains(t, buf.String(), "trace-123")
}

func TestServeHTTP_NoRouteReturns404(t *testing.T) {
	t.Parallel()
	r := buildRouter(t, config.RoutingRule{
		Match:        config.MatchCondition{Host: "specific.com"},
		BackendGroup: "g1",
	})
	p, _ := newProxy(t, r, map[string]Balancer{})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://other.com/foo", nil)

	p.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestServeHTTP_UnknownGroupReturns502(t *testing.T) {
	t.Parallel()
	p, _ := newProxy(t, fallbackRouter(t, "ghost"), map[string]Balancer{})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://example.com/foo", nil)

	p.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadGateway, w.Code)
}

func TestServeHTTP_PickErrorReturns503(t *testing.T) {
	t.Parallel()
	bal := mocks.NewBalancer(t)
	bal.EXPECT().Pick().Return(nil, domainErr.ErrNoBackends)

	p, _ := newProxy(t, fallbackRouter(t, "g1"), map[string]Balancer{"g1": bal})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://example.com/foo", nil)

	p.ServeHTTP(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestServeHTTP_NilBackendReturns502(t *testing.T) {
	t.Parallel()
	bal := mocks.NewBalancer(t)
	bal.EXPECT().Pick().Return(nil, nil)

	p, _ := newProxy(t, fallbackRouter(t, "g1"), map[string]Balancer{"g1": bal})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://example.com/foo", nil)

	p.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadGateway, w.Code)
}

// Pick вернул backend вместе с ошибкой — backend считается невыданным,
// defer НЕ должен вызывать Release. Если бы вызывал — мок упал бы
// на "unexpected call to Release".
func TestServeHTTP_DoesNotReleaseWhenPickReturnsBackendWithError(t *testing.T) {
	t.Parallel()
	backend := model.NewBackend("10.0.0.9:1234", 1, 0)

	bal := mocks.NewBalancer(t)
	bal.EXPECT().Pick().Return(backend, domainErr.ErrNoBackends)
	// Release намеренно не ожидается.

	p, _ := newProxy(t, fallbackRouter(t, "g1"), map[string]Balancer{"g1": bal})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://example.com/foo", nil)

	p.ServeHTTP(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestServeHTTP_UnreachableBackendReturns502(t *testing.T) {
	t.Parallel()
	// 127.0.0.1:1 — порт, на котором никто не слушает.
	backend := model.NewBackend("127.0.0.1:1", 1, 0)

	bal := mocks.NewBalancer(t)
	bal.EXPECT().Pick().Return(backend, nil)
	bal.EXPECT().Release(backend).Return()

	p, _ := newProxy(t, fallbackRouter(t, "g1"), map[string]Balancer{"g1": bal})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://example.com/foo", nil)

	p.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadGateway, w.Code)
}
