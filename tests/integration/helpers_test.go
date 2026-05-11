//go:build integration

package integration

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sanchey92/flowgate/internal/balancer"
	"github.com/sanchey92/flowgate/internal/config"
	"github.com/sanchey92/flowgate/internal/domain/model"
	"github.com/sanchey92/flowgate/internal/proxy"
	"github.com/sanchey92/flowgate/internal/registry"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// captureLogger collects slog records into a slice for inspection.
type captureLogger struct {
	mu      sync.Mutex
	records []map[string]any
}

func newCaptureLogger() (*slog.Logger, *captureLogger) {
	cap := &captureLogger{}
	return slog.New(cap), cap
}

func (c *captureLogger) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (c *captureLogger) Handle(_ context.Context, r slog.Record) error {
	m := map[string]any{"msg": r.Message, "level": r.Level.String()}
	r.Attrs(func(a slog.Attr) bool {
		m[a.Key] = a.Value.Any()
		return true
	})
	c.mu.Lock()
	c.records = append(c.records, m)
	c.mu.Unlock()
	return nil
}

func (c *captureLogger) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &captureWithAttrs{parent: c, attrs: attrs}
}

func (c *captureLogger) WithGroup(_ string) slog.Handler { return c }

type captureWithAttrs struct {
	parent *captureLogger
	attrs  []slog.Attr
}

func (c *captureWithAttrs) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (c *captureWithAttrs) Handle(_ context.Context, r slog.Record) error {
	m := map[string]any{"msg": r.Message, "level": r.Level.String()}
	for _, a := range c.attrs {
		m[a.Key] = a.Value.Any()
	}
	r.Attrs(func(a slog.Attr) bool {
		m[a.Key] = a.Value.Any()
		return true
	})
	c.parent.mu.Lock()
	c.parent.records = append(c.parent.records, m)
	c.parent.mu.Unlock()
	return nil
}

func (c *captureWithAttrs) WithAttrs(attrs []slog.Attr) slog.Handler {
	merged := make([]slog.Attr, 0, len(c.attrs)+len(attrs))
	merged = append(merged, c.attrs...)
	merged = append(merged, attrs...)
	return &captureWithAttrs{parent: c.parent, attrs: merged}
}

func (c *captureWithAttrs) WithGroup(_ string) slog.Handler { return c }

func (c *captureLogger) Snapshot() []map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]map[string]any, len(c.records))
	copy(out, c.records)
	return out
}

// testDefaults returns a config.Defaults suited for fast E2E tests.
func testDefaults() config.Defaults {
	return config.Defaults{
		ConnectTimeout:       1 * time.Second,
		IdleTimeout:          3 * time.Second,
		KeepAlive:            30 * time.Second,
		MaxConns:             1024,
		BufSize:              32768,
		ProxyProtoHdrTimeout: 1 * time.Second,
		Backoff:              config.Backoff{Base: 20 * time.Millisecond, Max: 200 * time.Millisecond},
		UDP: config.UDPDefaults{
			SessionIdle: 3 * time.Second,
			BackendRead: 1 * time.Second,
			Dial:        500 * time.Millisecond,
		},
	}
}

// startProxy spins up a proxy.Runner with the given route and matching balancer.
// Returns the runner and its bound address.
func startProxy(t *testing.T, ctx context.Context, r config.Route, log *slog.Logger) proxy.Runner {
	t.Helper()

	backends := make([]*model.Backend, 0, len(r.Backends))
	for i, b := range r.Backends {
		backends = append(backends, model.NewBackend(b.Addr, b.Weight, i))
	}
	reg := registry.NewInMemory(backends)

	bal, err := balancer.New(r.Balancer, reg)
	require.NoError(t, err)

	settings := r.Effective(testDefaults())
	p, err := proxy.New(r, settings, bal, log)
	require.NoError(t, err)

	require.NoError(t, p.Start(ctx))
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = p.Shutdown(shutdownCtx)
	})
	return p
}

// tcpEcho is a tiny TCP server. It records the count of accepted connections,
// the byte counts of payloads it received, and supports an optional per-conn delay.
type tcpEcho struct {
	addr  string
	hits  *atomic.Int64
	bytes *atomic.Int64
	delay time.Duration

	ln net.Listener
	wg sync.WaitGroup
}

func startTCPEcho(t *testing.T, delay time.Duration) *tcpEcho {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	e := &tcpEcho{
		addr:  ln.Addr().String(),
		hits:  new(atomic.Int64),
		bytes: new(atomic.Int64),
		delay: delay,
		ln:    ln,
	}
	e.wg.Add(1)
	go e.serve()

	t.Cleanup(func() {
		_ = e.ln.Close()
		e.wg.Wait()
	})
	return e
}

func (e *tcpEcho) Hits() int64 { return e.hits.Load() }

func (e *tcpEcho) serve() {
	defer e.wg.Done()
	for {
		c, err := e.ln.Accept()
		if err != nil {
			return
		}
		e.hits.Add(1)
		e.wg.Add(1)
		go e.handle(c)
	}
}

func (e *tcpEcho) handle(c net.Conn) {
	defer e.wg.Done()
	defer func() { _ = c.Close() }()

	buf := make([]byte, 4096)
	for {
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		n, err := c.Read(buf)
		if n > 0 {
			e.bytes.Add(int64(n))
			if e.delay > 0 {
				select {
				case <-time.After(e.delay):
				}
			}
			if _, werr := c.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// udpEcho is a tiny UDP echo server with per-client tracking.
type udpEcho struct {
	addr        string
	conn        *net.UDPConn
	hits        *atomic.Int64
	seen        sync.Map // netip.AddrPort -> struct{}
	label       string
	closing     chan struct{}
	wg          sync.WaitGroup
	transformFn func(b []byte) []byte
}

func startUDPEcho(t *testing.T, label string) *udpEcho {
	t.Helper()

	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	require.NoError(t, err)

	e := &udpEcho{
		addr:    conn.LocalAddr().String(),
		conn:    conn,
		hits:    new(atomic.Int64),
		label:   label,
		closing: make(chan struct{}),
		transformFn: func(in []byte) []byte {
			out := make([]byte, 0, len(in)+len(label)+2)
			out = append(out, '[')
			out = append(out, []byte(label)...)
			out = append(out, ']')
			out = append(out, in...)
			return out
		},
	}
	e.wg.Add(1)
	go e.serve()

	t.Cleanup(func() {
		close(e.closing)
		_ = e.conn.Close()
		e.wg.Wait()
	})
	return e
}

func (e *udpEcho) Hits() int64 { return e.hits.Load() }

func (e *udpEcho) ClientsSeen() int {
	n := 0
	e.seen.Range(func(_, _ any) bool { n++; return true })
	return n
}

func (e *udpEcho) serve() {
	defer e.wg.Done()
	buf := make([]byte, 4096)
	for {
		select {
		case <-e.closing:
			return
		default:
		}
		_ = e.conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		n, src, err := e.conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				continue
			}
			return
		}
		e.hits.Add(1)
		e.seen.Store(src, struct{}{})

		reply := e.transformFn(buf[:n])
		if _, err := e.conn.WriteToUDPAddrPort(reply, src); err != nil {
			return
		}
	}
}

// buildV1Header builds a PROXY-protocol v1 header line.
func buildV1Header(srcIP, dstIP string, srcPort, dstPort uint16) []byte {
	return []byte(fmt.Sprintf("PROXY TCP4 %s %s %d %d\r\n", srcIP, dstIP, srcPort, dstPort))
}

// buildV2Header builds a PROXY-protocol v2 binary header (IPv4 STREAM).
func buildV2Header(srcIP, dstIP netip.Addr, srcPort, dstPort uint16) []byte {
	sig := []byte{
		0x0D, 0x0A, 0x0D, 0x0A, 0x00,
		0x0D, 0x0A, 0x51, 0x55, 0x49, 0x54, 0x0A,
	}
	verCmd := byte(0x21)   // version 2, command PROXY
	famTrans := byte(0x11) // AF_INET + STREAM

	body := make([]byte, 0, 12)
	s4 := srcIP.As4()
	d4 := dstIP.As4()
	body = append(body, s4[:]...)
	body = append(body, d4[:]...)
	body = binary.BigEndian.AppendUint16(body, srcPort)
	body = binary.BigEndian.AppendUint16(body, dstPort)

	out := make([]byte, 0, 16+len(body))
	out = append(out, sig...)
	out = append(out, verCmd, famTrans)
	out = binary.BigEndian.AppendUint16(out, uint16(len(body)))
	out = append(out, body...)
	return out
}

// readAll reads from c until EOF or the byte budget is exhausted, with a deadline.
func readUpTo(c net.Conn, max int, deadline time.Duration) ([]byte, error) {
	_ = c.SetReadDeadline(time.Now().Add(deadline))
	out := make([]byte, 0, max)
	br := bufio.NewReader(c)
	tmp := make([]byte, 1024)
	for len(out) < max {
		n, err := br.Read(tmp)
		if n > 0 {
			out = append(out, tmp[:n]...)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return out, nil
			}
			return out, err
		}
	}
	return out, nil
}
