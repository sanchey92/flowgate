//go:build integration

package integration

import (
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sanchey92/flowgate/internal/config"
)

// TestE2E_UDP_SessionsIsolated: 2 clients × 2 backends. Each client must get
// a stable answer that comes from exactly one backend; clients do not share
// a session and do not see traffic intended for the other.
func TestE2E_UDP_SessionsIsolated(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	be1 := startUDPEcho(t, "A")
	be2 := startUDPEcho(t, "B")

	r := config.Route{
		Name:     "udp-rr",
		Protocol: "udp",
		Listen:   "127.0.0.1:0",
		Balancer: "round_robin",
		Backends: []config.Backend{
			{Addr: be1.addr, Weight: 1},
			{Addr: be2.addr, Weight: 1},
		},
	}
	p := startProxy(t, ctx, r, discardLogger())
	proxyAddr := p.Addr().(*net.UDPAddr)

	// Each client dials the proxy from a distinct local port. That gives
	// the proxy two distinct AddrPort keys, so it must allocate two sessions
	// and (with RR) talk to a different backend per client.
	type result struct {
		who   string
		reply string
		err   error
	}
	clientCount := 2
	results := make(chan result, clientCount*3)
	var wg sync.WaitGroup

	dialAndPing := func(who string) {
		defer wg.Done()
		c, err := net.DialUDP("udp", nil, proxyAddr)
		if err != nil {
			results <- result{who: who, err: err}
			return
		}
		defer func() { _ = c.Close() }()

		for i := 0; i < 3; i++ {
			_ = c.SetDeadline(time.Now().Add(2 * time.Second))
			if _, err := c.Write([]byte(who)); err != nil {
				results <- result{who: who, err: err}
				return
			}
			buf := make([]byte, 1024)
			n, err := c.Read(buf)
			if err != nil {
				results <- result{who: who, err: err}
				return
			}
			results <- result{who: who, reply: string(buf[:n])}
		}
	}

	wg.Add(clientCount)
	go dialAndPing("client-1")
	go dialAndPing("client-2")
	wg.Wait()
	close(results)

	clientReplies := map[string][]string{}
	for r := range results {
		require.NoError(t, r.err, "client %s", r.who)
		clientReplies[r.who] = append(clientReplies[r.who], r.reply)
	}
	require.Len(t, clientReplies, clientCount)

	// Each client must always hit the same backend for the duration
	// of its session (sticky 5-tuple via RR's first pick).
	for who, replies := range clientReplies {
		require.NotEmpty(t, replies, "client %s had no replies", who)
		first := replies[0]
		for _, r := range replies {
			assert.Equal(t, first, r,
				"client %s session must be sticky: got %q after %q", who, r, first)
			assert.True(t, strings.HasSuffix(r, who),
				"reply payload must echo the client's request: %q", r)
		}
	}

	// All backend traffic must come from a single client per session.
	// Total replies seen at backends equals total requests.
	totalHits := be1.Hits() + be2.Hits()
	assert.EqualValues(t, clientCount*3, totalHits,
		"unexpected number of backend hits: be1=%d be2=%d", be1.Hits(), be2.Hits())

	// Each backend that did receive traffic must have seen at most 1 distinct client.
	if be1.Hits() > 0 {
		assert.Equal(t, 1, be1.ClientsSeen(),
			"backend A must see at most one (proxy-side) source, got %d", be1.ClientsSeen())
	}
	if be2.Hits() > 0 {
		assert.Equal(t, 1, be2.ClientsSeen(),
			"backend B must see at most one (proxy-side) source, got %d", be2.ClientsSeen())
	}
}
