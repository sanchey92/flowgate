package proxyhttp

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
	return newRetryTransport(BuildTransport(TransportSettings{}), groups, &retry.Policy{}, retry.NewBudget(20), log)
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

// --- retry budget ------------------------------------------------------------

// newEnabledPolicy собирает включённую политику с ретраями по 503 и
// микроскопическим бэкоффом, чтобы тесты с реальными попытками не спали.
func newEnabledPolicy(t *testing.T) *retry.Policy {
	t.Helper()
	p, err := retry.CompileRetryPolicy(
		true, 2, []string{"503"},
		time.Millisecond, 2*time.Millisecond,
		true, false,
	)
	require.NoError(t, err)
	return p
}

// unavailableRT всегда отвечает 503 и считает попытки — стаб «штормящего»
// апстрима для проверки взаимодействия транспорта с бюджетом.
func unavailableRT(calls *int) roundTripperFunc {
	return func(*http.Request) (*http.Response, error) {
		*calls++
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Body:       io.NopCloser(strings.NewReader("upstream unavailable")),
		}, nil
	}
}

// Отказ бюджета — это «вернуть как есть»: клиент получает 503 последней
// (единственной) попытки, а не новую ошибку; slot.Retries не растёт, как и
// при исчерпании max_retries. Pick строго один — повторный Pick или Release
// уронили бы мок.
func TestRetryTransport_BudgetDeniesRetry(t *testing.T) {
	t.Parallel()
	backend := model.NewBackend("10.0.0.2:9090", 1, 0)
	bal := mocks.NewBalancer(t)
	bal.EXPECT().Pick().Return(backend, nil).Once()

	// Прогреваем окно до budgetMinRequest (10 в пакете retry): percent=0
	// запрещает ретраи только после порога стабильности.
	budget := retry.NewBudget(0)
	for i := 0; i < 10; i++ {
		budget.RecordRequest()
	}

	calls := 0
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	rt := newRetryTransport(unavailableRT(&calls), map[string]Balancer{"g1": bal}, newEnabledPolicy(t), budget, log)
	r, slot := newTransportRequest(t, "g1")

	resp, err := rt.RoundTrip(r)

	require.NoError(t, err)
	require.NotNil(t, resp)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	assert.Equal(t, 1, calls, "при отказе бюджета попытка должна быть ровно одна")
	assert.Equal(t, 0, slot.Retries)
}

// Контроль к предыдущему тесту: на холодном бюджете тот же транспорт делает
// все попытки по политике — значит, отказ выше вызван именно бюджетом.
func TestRetryTransport_RetriesWhenBudgetAllows(t *testing.T) {
	t.Parallel()
	backend := model.NewBackend("10.0.0.2:9090", 1, 0)
	bal := mocks.NewBalancer(t)
	bal.EXPECT().Pick().Return(backend, nil).Times(3)
	bal.EXPECT().Release(backend).Return().Times(2)

	calls := 0
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	rt := newRetryTransport(unavailableRT(&calls), map[string]Balancer{"g1": bal}, newEnabledPolicy(t), retry.NewBudget(20), log)
	r, slot := newTransportRequest(t, "g1")

	resp, err := rt.RoundTrip(r)

	require.NoError(t, err)
	require.NotNil(t, resp)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	assert.Equal(t, 3, calls, "первая попытка плюс max_retries ретраев")
	assert.Equal(t, 2, slot.Retries)
}
