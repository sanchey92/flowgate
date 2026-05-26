//go:build integration

package integration

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sanchey92/flowgate/internal/config"
)

// TestE2E_HTTP_WebSocket_EchoRoundTrip — Stage G2 §2: WebSocket end-to-end.
// Полный handshake → несколько эхо-сообщений → close-frame. Проверяем, что
// каждый кадр доходит без искажений и обе стороны корректно завершают сессию.
func TestE2E_HTTP_WebSocket_EchoRoundTrip(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	be := startHTTPBackendWS(t, "ws-echo")

	groups := mkGroups(httpGroupSpec{Name: "g-ws", Addrs: []string{be.Addr()}})
	rules := []config.RoutingRule{
		{Match: config.MatchCondition{PathPrefix: "/"}, BackendGroup: "g-ws"},
	}
	route := httpRoute("ws", groups, rules, config.WebSocketConfig{Enabled: true})
	p := startProxy(t, ctx, route, discardLogger())
	addr := p.Addr().String()
	waitListening(t, addr, 2*time.Second)

	dialCtx, dialCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer dialCancel()

	c, resp, err := websocket.Dial(dialCtx, fmt.Sprintf("ws://%s/ws", addr), nil)
	require.NoError(t, err, "ws dial through proxy must succeed")
	require.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode, "handshake must return 101")
	defer c.CloseNow()

	frames := []string{"hello", "frame-2", "Привет, WS", ""}
	for i, payload := range frames {
		writeCtx, wcancel := context.WithTimeout(context.Background(), 1*time.Second)
		require.NoError(t, c.Write(writeCtx, websocket.MessageText, []byte(payload)),
			"write frame %d", i)
		wcancel()

		readCtx, rcancel := context.WithTimeout(context.Background(), 1*time.Second)
		typ, data, err := c.Read(readCtx)
		rcancel()
		require.NoError(t, err, "read frame %d", i)
		assert.Equal(t, websocket.MessageText, typ, "frame %d type", i)
		assert.Equal(t, payload, string(data), "frame %d payload echo", i)
	}

	// Clean shutdown: клиент шлёт close, обе стороны видят NormalClosure.
	require.NoError(t, c.Close(websocket.StatusNormalClosure, "bye"))

	assert.EqualValues(t, 1, be.Hits(), "одно ws-соединение — один хит upgrade-handler'а")
}

// TestE2E_HTTP_WebSocket_Disabled — клиент шлёт корректный upgrade, но
// websocket.enabled=false. Прокси должен короткозамкнуть запрос 501 ещё до
// похода в балансировщик. Backend hits должен остаться нулём.
func TestE2E_HTTP_WebSocket_Disabled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	be := startHTTPBackendWS(t, "ws-disabled")

	groups := mkGroups(httpGroupSpec{Name: "g-ws", Addrs: []string{be.Addr()}})
	rules := []config.RoutingRule{
		{Match: config.MatchCondition{PathPrefix: "/"}, BackendGroup: "g-ws"},
	}
	route := httpRoute("ws-off", groups, rules, config.WebSocketConfig{Enabled: false})
	p := startProxy(t, ctx, route, discardLogger())
	addr := p.Addr().String()
	waitListening(t, addr, 2*time.Second)

	req, err := http.NewRequest(http.MethodGet, "http://"+addr+"/ws", nil)
	require.NoError(t, err)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	req.Header.Set("Sec-WebSocket-Version", "13")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNotImplemented, resp.StatusCode)
	assert.EqualValues(t, 0, be.Hits(), "при отключённом ws backend не должен видеть запрос")
}
