//go:build integration

package integration

import (
	"context"
	"io"
	"math"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sanchey92/flowgate/internal/config"
)

// TestE2E_TCP_RoundRobin_EvenDistribution: 1 route → 2 backends, RR, 100 requests.
// Each backend must take roughly half (±5% of total).
func TestE2E_TCP_RoundRobin_EvenDistribution(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	be1 := startTCPEcho(t, 0)
	be2 := startTCPEcho(t, 0)

	r := config.Route{
		Name:     "rr",
		Protocol: "tcp",
		Listen:   "127.0.0.1:0",
		Balancer: "round_robin",
		Backends: []config.Backend{
			{Addr: be1.addr, Weight: 1},
			{Addr: be2.addr, Weight: 1},
		},
	}
	p := startProxy(t, ctx, r, discardLogger())

	const total = 100
	payload := []byte("ping")

	for i := 0; i < total; i++ {
		c, err := net.DialTimeout("tcp", p.Addr().String(), 1*time.Second)
		require.NoError(t, err, "dial %d", i)

		require.NoError(t, c.SetDeadline(time.Now().Add(2*time.Second)))
		_, err = c.Write(payload)
		require.NoError(t, err)

		// half-close so the proxy's pipe sees EOF and unwinds cleanly
		if cw, ok := c.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}

		got, err := io.ReadAll(c)
		require.NoError(t, err, "read response %d", i)
		require.Equal(t, payload, got)
		_ = c.Close()
	}

	h1 := be1.Hits()
	h2 := be2.Hits()
	t.Logf("backend1 hits=%d, backend2 hits=%d", h1, h2)

	require.EqualValues(t, total, h1+h2, "total hits must equal request count")

	diff := math.Abs(float64(h1-h2)) / float64(total)
	assert.LessOrEqual(t, diff, 0.05,
		"RR distribution must be even within ±5%%: %d vs %d", h1, h2)
}
