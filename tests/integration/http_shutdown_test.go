//go:build integration

package integration

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sanchey92/flowgate/internal/config"
)

// TestE2E_HTTP_GracefulShutdown_MidRequest — Stage G2 §4:
// «активный запрос дозавершается, новые получают отказ».
//
// Идея: backend искусственно отвечает с задержкой 500ms. Старт запроса —
// клиент1 в горутине. Через 100ms вызываем proxy.Shutdown(ctx, 3s). За это
// окно второй dial на тот же listener должен зафейлиться (порт закрыт), а
// первый запрос — дожить до 200 OK.
func TestE2E_HTTP_GracefulShutdown_MidRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	be := startHTTPBackendSlow(t, "slow", 500*time.Millisecond)

	groups := mkGroups(httpGroupSpec{Name: "g-slow", Addrs: []string{be.Addr()}})
	rules := []config.RoutingRule{
		{Match: config.MatchCondition{PathPrefix: "/"}, BackendGroup: "g-slow"},
	}
	route := httpRoute("slow", groups, rules, config.WebSocketConfig{Enabled: false})

	// startProxy уже регистрирует Shutdown в t.Cleanup. Runner.Shutdown
	// идемпотентен (см. internal/proxy/http/runner.go: stopped→nil + <-done),
	// поэтому повторный вызов из теста безопасен.
	p := startProxy(t, ctx, route, discardLogger())
	addr := p.Addr().String()
	waitListening(t, addr, 2*time.Second)

	// 1. Старт активного запроса.
	type result struct {
		status int
		body   string
		err    error
	}
	done := make(chan result, 1)
	go func() {
		req, err := http.NewRequest(http.MethodGet, "http://"+addr+"/slow", nil)
		if err != nil {
			done <- result{err: err}
			return
		}
		req.Host = "any.example.com"
		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			done <- result{err: err}
			return
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		done <- result{status: resp.StatusCode, body: string(b)}
	}()

	// Дать прокси установить TCP-соединение с backend'ом и начать ждать.
	time.Sleep(100 * time.Millisecond)

	// 2. Параллельный shutdown.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 3*time.Second)
	shutdownDone := make(chan error, 1)
	go func() {
		shutdownDone <- p.Shutdown(shutdownCtx)
	}()
	defer shutdownCancel()

	// 3. Пока активный запрос ещё в полёте, новые dial'ы должны отвергаться —
	//    listener закрыт сразу же при Shutdown.
	require.Eventually(t, func() bool {
		c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err != nil {
			return true
		}
		_ = c.Close()
		return false
	}, 1*time.Second, 25*time.Millisecond, "listener должен закрыться при graceful shutdown")

	// 4. Активный запрос дожил.
	select {
	case r := <-done:
		require.NoError(t, r.err, "активный запрос должен завершиться без ошибки")
		assert.Equal(t, http.StatusOK, r.status)
		assert.Equal(t, "slow:slow:done", r.body)
	case <-time.After(3 * time.Second):
		t.Fatal("активный запрос не вернулся за 3 секунды после shutdown")
	}

	// 5. Shutdown сам должен завершиться.
	select {
	case err := <-shutdownDone:
		require.NoError(t, err, "shutdown должен завершиться без ошибки")
	case <-time.After(1 * time.Second):
		t.Fatal("Shutdown не вернулся вовремя")
	}
}
