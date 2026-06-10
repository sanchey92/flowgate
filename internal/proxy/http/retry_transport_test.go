package proxyhttp

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	domainErr "github.com/sanchey92/flowgate/internal/domain/errors"
	"github.com/sanchey92/flowgate/internal/domain/model"
	"github.com/sanchey92/flowgate/internal/proxy/http/mocks"
	"github.com/sanchey92/flowgate/internal/proxy/http/reqctx"
	"github.com/sanchey92/flowgate/internal/proxy/http/retry"
)

// newTransportRequest собирает запрос со слотом в контексте — так, как его
// видит retryTransport после rewrite: группа уже определена маршрутизатором.
func newTransportRequest(t *testing.T, group string) (*http.Request, *reqctx.RequestSlot) {
	t.Helper()
	slot := &reqctx.RequestSlot{Group: group}
	r := httptest.NewRequest(http.MethodGet, "http://example.com/foo", nil)
	return r.WithContext(reqctx.WithSlot(r.Context(), slot)), slot
}

func newTestRetryTransport(groups map[string]Balancer) *retryTransport {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return newRetryTransport(BuildTransport(TransportSettings{}), groups, &retry.Policy{}, log)
}

func TestRetryTransport_PickError(t *testing.T) {
	t.Parallel()
	bal := mocks.NewBalancer(t)
	bal.EXPECT().Pick().Return(nil, domainErr.ErrNoBackends)

	rt := newTestRetryTransport(map[string]Balancer{"g1": bal})
	r, slot := newTransportRequest(t, "g1")

	resp, err := rt.RoundTrip(r)

	require.Nil(t, resp)
	assert.ErrorIs(t, err, domainErr.ErrNoBackends)
	assert.Nil(t, slot.Backend)
}

// Pick нарушает контракт: err == nil, но backend пуст. Транспорт не должен
// доходить до directTo с nil-бэкендом — это закончилось бы panic.
func TestRetryTransport_PickReturnsNilBackend(t *testing.T) {
	t.Parallel()
	bal := mocks.NewBalancer(t)
	bal.EXPECT().Pick().Return(nil, nil)

	rt := newTestRetryTransport(map[string]Balancer{"g1": bal})
	r, slot := newTransportRequest(t, "g1")

	resp, err := rt.RoundTrip(r)

	require.Nil(t, resp)
	assert.ErrorIs(t, err, domainErr.ErrNilBackend)
	assert.Nil(t, slot.Backend)
}

// Pick нарушает контракт: возвращает backend вместе с ошибкой.
// Транспорт не должен записывать backend в слот и вызывать Release —
// бэкенд фактически не был выдан. Мок упал бы на unexpected call to Release.
func TestRetryTransport_PickReturnsBackendWithError(t *testing.T) {
	t.Parallel()
	backend := model.NewBackend("10.0.0.2:9090", 1, 0)

	bal := mocks.NewBalancer(t)
	bal.EXPECT().Pick().Return(backend, domainErr.ErrNoBackends)

	rt := newTestRetryTransport(map[string]Balancer{"g1": bal})
	r, slot := newTransportRequest(t, "g1")

	resp, err := rt.RoundTrip(r)

	require.Nil(t, resp)
	assert.ErrorIs(t, err, domainErr.ErrNoBackends)
	assert.Nil(t, slot.Backend)
}
