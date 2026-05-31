package proxyhttp

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sanchey92/flowgate/internal/config"
	"github.com/sanchey92/flowgate/internal/domain/model"
	"github.com/sanchey92/flowgate/internal/pool"
	"github.com/sanchey92/flowgate/internal/proxy/http/router"
)

// pickedOnceBalancer — балансировщик-минимал для бенча: возвращает заранее
// зафиксированный backend, считает Pick/Release атомарно. Реальный RR/LC
// здесь не нужны и только зашумят профиль аллокациями карты.
type pickedOnceBalancer struct {
	backend *model.Backend
	picks   atomic.Int64
}

func (b *pickedOnceBalancer) Pick() (*model.Backend, error) { b.picks.Add(1); return b.backend, nil }
func (b *pickedOnceBalancer) Release(*model.Backend)        {}

// BenchmarkHTTPProxy_Throughput — Stage G3 §2: baseline RPS «клиент → прокси →
// backend» без сети наружу. Поднимаем httptest.Server для backend, ещё один —
// для прокси, гоняем GET в RunParallel. SetBytes даёт удобную метрику MB/s.
//
// Цель — фиксировать baseline для последующих регрессий. Цифры зависят от
// железа; запись текущей baseline — в README, секция «Performance baseline».
func BenchmarkHTTPProxy_Throughput(b *testing.B) {
	const responseBody = "hello, world!"

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, responseBody)
	}))
	defer upstream.Close()

	backend := model.NewBackend(strings.TrimPrefix(upstream.URL, "http://"), 1, 0)
	bal := &pickedOnceBalancer{backend: backend}

	rt, err := router.Build([]config.RoutingRule{
		{Match: config.MatchCondition{PathPrefix: "/"}, BackendGroup: "g"},
	}, nil)
	if err != nil {
		b.Fatalf("router.Build: %v", err)
	}

	tr := BuildTransport(TransportSettings{
		MaxIdleConnsPerHost: 256,
		MaxConnsPerHost:     256,
	})
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	p := New(rt, map[string]Balancer{"g": bal}, tr, pool.NewBufferPool(32*1024),
		config.HeaderRules{},
		config.StandardHeadersConfig{Enabled: true},
		config.WebSocketConfig{Enabled: true},
		0,
		log)

	frontend := httptest.NewServer(p)
	defer frontend.Close()

	client := &http.Client{Transport: &http.Transport{
		MaxIdleConnsPerHost: 256,
		MaxConnsPerHost:     256,
		DisableCompression:  true,
	}}

	b.ReportAllocs()
	b.SetBytes(int64(len(responseBody)))
	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			resp, err := client.Get(frontend.URL + "/x")
			if err != nil {
				b.Fatalf("client.Get: %v", err)
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	})
}
