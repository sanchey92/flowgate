//go:build integration

package integration

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sanchey92/flowgate/internal/config"
)

// TestE2E_HTTP_UnreachableBackend_Returns502 — Stage G2 §3 (вариант «нулевого
// health»): когда единственный backend в группе недоступен по TCP, прокси
// возвращает 502 Bad Gateway.
//
// Замечание: «backend group с нулём backends» отрезается валидатором конфига
// ещё до запуска, поэтому именно unreachable — это самая близкая практическая
// эмуляция «нулевого health» без реализации health-check'ов (Phase 3).
func TestE2E_HTTP_UnreachableBackend_Returns502(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// 127.0.0.1:1 — заведомо никто не слушает; connect_timeout=1s в testDefaults.
	groups := mkGroups(httpGroupSpec{Name: "g-dead", Addrs: []string{"127.0.0.1:1"}})
	rules := []config.RoutingRule{
		{Match: config.MatchCondition{PathPrefix: "/"}, BackendGroup: "g-dead"},
	}
	route := httpRoute("dead", groups, rules, config.WebSocketConfig{Enabled: false})
	p := startProxy(t, ctx, route, discardLogger())
	addr := p.Addr().String()
	waitListening(t, addr, 2*time.Second)

	resp, _ := httpGET(t, addr, "any.example.com", "/x", nil)
	assert.Equal(t, http.StatusBadGateway, resp.StatusCode,
		"недоступный backend должен дать 502")
}

// TestE2E_HTTP_NoRoute_Returns404 — запрос приходит, но ни одно правило не
// матчится (нет fallback). Прокси возвращает 404, балансировщик не вызывается.
func TestE2E_HTTP_NoRoute_Returns404(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	be := startHTTPBackend(t, "isolated")
	groups := mkGroups(httpGroupSpec{Name: "g-host", Addrs: []string{be.Addr()}})
	rules := []config.RoutingRule{
		// Только host-specific правило: всё остальное обязано получить 404.
		{Match: config.MatchCondition{Host: "tenant.example.com"}, BackendGroup: "g-host"},
	}
	route := httpRoute("no-fallback", groups, rules, config.WebSocketConfig{Enabled: false})
	p := startProxy(t, ctx, route, discardLogger())
	addr := p.Addr().String()
	waitListening(t, addr, 2*time.Second)

	resp, _ := httpGET(t, addr, "other.example.com", "/anywhere", nil)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.EqualValues(t, 0, be.Hits(), "backend не должен видеть запрос")
}

// TestE2E_HTTP_HappyPath_Returns200 — sanity-check для предыдущих негативных
// кейсов: с теми же defaults валидный запрос проходит. Это страховка, чтобы
// 502/404 выше не оказались артефактом сломанной конфигурации.
func TestE2E_HTTP_HappyPath_Returns200(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	be := startHTTPBackend(t, "live")
	groups := mkGroups(httpGroupSpec{Name: "g-live", Addrs: []string{be.Addr()}})
	rules := []config.RoutingRule{
		{Match: config.MatchCondition{PathPrefix: "/"}, BackendGroup: "g-live"},
	}
	route := httpRoute("live", groups, rules, config.WebSocketConfig{Enabled: false})
	p := startProxy(t, ctx, route, discardLogger())
	addr := p.Addr().String()
	waitListening(t, addr, 2*time.Second)

	resp, body := httpGET(t, addr, "any.example.com", "/hello", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "live:/hello", body)
}
