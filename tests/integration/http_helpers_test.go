//go:build integration

package integration

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"

	"github.com/sanchey92/flowgate/internal/config"
)

// httpBackend — лёгкий апстрим для интеграционных HTTP-тестов. Помимо счётчика
// хитов отдаёт label в теле ответа и заголовке, чтобы тест мог за один запрос
// убедиться, в какую группу попал трафик.
type httpBackend struct {
	label  string
	addr   string
	hits   atomic.Int64
	srv    *httptest.Server
	slow   time.Duration
	wsEcho bool
}

// startHTTPBackend поднимает httptest.Server с маршрутами:
//
//	GET /        — отдаёт label
//	GET /slow    — ждёт b.slow перед ответом
//	GET /ws      — WebSocket echo (если wsEcho=true)
func startHTTPBackend(t *testing.T, label string) *httpBackend {
	t.Helper()
	return startHTTPBackendWith(t, label, 0, false)
}

func startHTTPBackendSlow(t *testing.T, label string, delay time.Duration) *httpBackend {
	t.Helper()
	return startHTTPBackendWith(t, label, delay, false)
}

func startHTTPBackendWS(t *testing.T, label string) *httpBackend {
	t.Helper()
	return startHTTPBackendWith(t, label, 0, true)
}

func startHTTPBackendWith(t *testing.T, label string, slow time.Duration, ws bool) *httpBackend {
	t.Helper()

	b := &httpBackend{label: label, slow: slow, wsEcho: ws}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		b.hits.Add(1)
		w.Header().Set("X-Backend-Label", b.label)
		_, _ = io.WriteString(w, b.label+":"+r.URL.Path)
	})
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		b.hits.Add(1)
		select {
		case <-time.After(b.slow):
		case <-r.Context().Done():
			return
		}
		w.Header().Set("X-Backend-Label", b.label)
		_, _ = io.WriteString(w, b.label+":slow:done")
	})
	if ws {
		mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
			b.hits.Add(1)
			c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
				InsecureSkipVerify: true,
			})
			if err != nil {
				return
			}
			defer c.CloseNow()

			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			defer cancel()

			for {
				typ, data, err := c.Read(ctx)
				if err != nil {
					return
				}
				if err := c.Write(ctx, typ, data); err != nil {
					return
				}
			}
		})
	}

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	b.srv = srv
	b.addr = strings.TrimPrefix(srv.URL, "http://")
	return b
}

func (b *httpBackend) Addr() string  { return b.addr }
func (b *httpBackend) Hits() int64   { return b.hits.Load() }
func (b *httpBackend) Label() string { return b.label }

// httpGroupSpec — лаконичный спек группы для конструктора маршрута.
type httpGroupSpec struct {
	Name     string
	Balancer string
	Addrs    []string
}

func mkGroups(specs ...httpGroupSpec) []config.BackendGroup {
	out := make([]config.BackendGroup, 0, len(specs))
	for _, s := range specs {
		group := config.BackendGroup{Name: s.Name, Balancer: s.Balancer}
		for _, addr := range s.Addrs {
			group.Backends = append(group.Backends, config.Backend{Addr: addr, Weight: 1})
		}
		out = append(out, group)
	}
	return out
}

// httpRoute собирает config.Route для HTTP-прокси. Listen всегда :0,
// чтобы тесты не конфликтовали за порты — настоящий адрес узнаём через p.Addr().
func httpRoute(name string, groups []config.BackendGroup, rules []config.RoutingRule, ws config.WebSocketConfig) config.Route {
	return config.Route{
		Name:     name,
		Protocol: "http",
		Listen:   "127.0.0.1:0",
		Balancer: "round_robin",
		HTTP: &config.HTTPConfig{
			BackendGroups:   groups,
			RoutingRules:    rules,
			StandardHeaders: config.StandardHeadersConfig{Enabled: true},
			WebSocket:       ws,
		},
	}
}

// httpGET выполняет GET через указанный прокси. proxy — TCP-адрес слушателя,
// host — значение Host-заголовка (наш роутер маршрутизирует по нему).
func httpGET(t *testing.T, proxyAddr, host, path string, headers http.Header) (*http.Response, string) {
	t.Helper()

	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://%s%s", proxyAddr, path), nil)
	require.NoError(t, err)
	if host != "" {
		req.Host = host
	}
	for k, vs := range headers {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	_ = resp.Body.Close()
	return resp, string(body)
}

// waitListening дожидается, пока на addr можно открыть TCP-соединение.
// Используется в shutdown-тестах, чтобы убедиться, что прокси встал.
func waitListening(t *testing.T, addr string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("addr %s did not become listening within %s", addr, timeout)
}
