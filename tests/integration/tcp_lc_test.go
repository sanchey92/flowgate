//go:build integration

package integration

import (
	"context"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sanchey92/flowgate/internal/config"
)

// TestE2E_TCP_LeastConn_DistributionSkew opens N client connections, staggered
// in time, against two backends: one slow (every conn holds it for ~1s), one
// fast (echoes immediately). LC must keep the slow backend's active count
// elevated and route the bulk of new picks to the fast backend.
func TestE2E_TCP_LeastConn_DistributionSkew(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	slow := startTCPEcho(t, 1*time.Second)
	fast := startTCPEcho(t, 0)

	r := config.Route{
		Name:     "lc",
		Protocol: "tcp",
		Listen:   "127.0.0.1:0",
		Balancer: "least_conn",
		Backends: []config.Backend{
			{Addr: slow.addr, Weight: 1},
			{Addr: fast.addr, Weight: 1},
		},
	}
	p := startProxy(t, ctx, r, discardLogger())

	const (
		total       = 40
		launchEvery = 10 * time.Millisecond
	)
	payload := []byte("hi")

	var (
		wg   sync.WaitGroup
		errs atomic.Int64
	)

	run := func() {
		defer wg.Done()
		c, err := net.DialTimeout("tcp", p.Addr().String(), 1*time.Second)
		if err != nil {
			errs.Add(1)
			return
		}
		defer func() { _ = c.Close() }()

		_ = c.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := c.Write(payload); err != nil {
			errs.Add(1)
			return
		}
		if cw, ok := c.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
		got, err := io.ReadAll(c)
		if err != nil || string(got) != string(payload) {
			errs.Add(1)
		}
	}

	for i := 0; i < total; i++ {
		wg.Add(1)
		go run()
		if i < total-1 {
			time.Sleep(launchEvery)
		}
	}
	wg.Wait()

	require.Zero(t, errs.Load(), "no client-side errors expected")

	slowHits := slow.Hits()
	fastHits := fast.Hits()
	t.Logf("slow=%d, fast=%d", slowHits, fastHits)

	require.EqualValues(t, total, slowHits+fastHits)
	assert.Greater(t, fastHits, slowHits*2,
		"least-conn must steer the majority of traffic to the fast backend: fast=%d slow=%d",
		fastHits, slowHits)
}
