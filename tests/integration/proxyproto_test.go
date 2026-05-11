//go:build integration

package integration

import (
	"context"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sanchey92/flowgate/internal/config"
)

// TestE2E_ProxyProtocol_PropagatesSourceAddr verifies that:
//  1. when a PROXY (v1/v2) header is sent ahead of the payload, the proxy
//     parses it and logs the originating client address via "real_client_addr"
//  2. the header is stripped before forwarding — the backend sees only the
//     real payload, not the PROXY preamble.
func TestE2E_ProxyProtocol_PropagatesSourceAddr(t *testing.T) {
	cases := []struct {
		name    string
		mode    string
		header  func() []byte
		wantSrc string
	}{
		{
			name: "v1",
			mode: "v1",
			header: func() []byte {
				return buildV1Header("10.20.30.40", "127.0.0.1", 12345, 8080)
			},
			wantSrc: "10.20.30.40:12345",
		},
		{
			name: "v2",
			mode: "v2",
			header: func() []byte {
				return buildV2Header(
					netip.MustParseAddr("10.20.30.40"),
					netip.MustParseAddr("127.0.0.1"),
					12345, 8080,
				)
			},
			wantSrc: "10.20.30.40:12345",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)

			be := startTCPEcho(t, 0)

			logger, cap := newCaptureLogger()
			r := config.Route{
				Name:          "pp-" + tc.name,
				Protocol:      "tcp",
				Listen:        "127.0.0.1:0",
				Balancer:      "round_robin",
				ProxyProtocol: tc.mode,
				Backends: []config.Backend{
					{Addr: be.addr, Weight: 1},
				},
			}
			p := startProxy(t, ctx, r, logger)

			c, err := net.DialTimeout("tcp", p.Addr().String(), 1*time.Second)
			require.NoError(t, err)
			defer func() { _ = c.Close() }()

			payload := []byte("hello-after-pp")
			pkt := append(tc.header(), payload...)
			require.NoError(t, c.SetDeadline(time.Now().Add(3*time.Second)))
			_, err = c.Write(pkt)
			require.NoError(t, err)

			if cw, ok := c.(interface{ CloseWrite() error }); ok {
				_ = cw.CloseWrite()
			}

			got, err := readUpTo(c, 4096, 3*time.Second)
			require.NoError(t, err)
			// Backend must only echo back the payload — never the PROXY header.
			require.Equal(t, payload, got,
				"backend must receive (and echo) only post-header payload")

			// The proxy must have recorded the real client addr in its log context.
			deadline := time.Now().Add(2 * time.Second)
			var foundAddr string
			for time.Now().Before(deadline) {
				for _, rec := range cap.Snapshot() {
					if v, ok := rec["real_client_addr"].(string); ok && v != "" {
						foundAddr = v
						break
					}
				}
				if foundAddr != "" {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			assert.Equal(t, tc.wantSrc, foundAddr,
				"proxyproto %s: real_client_addr must come from parsed header", tc.name)
		})
	}
}
