package udp_test

import (
	"errors"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sanchey92/flowgate/internal/domain/model"
	"github.com/sanchey92/flowgate/internal/proxy/udp"
)

func newUDPConn(t *testing.T) *net.UDPConn {
	t.Helper()
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	require.NoError(t, err)
	return c
}

func mustAddrPort(t *testing.T, s string) netip.AddrPort {
	t.Helper()
	ap, err := netip.ParseAddrPort(s)
	require.NoError(t, err)
	return ap
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestNewSession_SetsFieldsAndTouches(t *testing.T) {
	t.Parallel()

	conn := newUDPConn(t)
	t.Cleanup(func() { _ = conn.Close() })

	client := mustAddrPort(t, "127.0.0.1:1234")
	be := &model.Backend{ID: "be-1", Addr: "127.0.0.1:9000"}

	before := time.Now().UnixNano()
	s := udp.NewSession(client, conn, be)
	after := time.Now().UnixNano()

	assert.Equal(t, client, s.ClientAddr)
	assert.Same(t, conn, s.BackendConn)
	assert.Same(t, be, s.Backend)

	last := s.IdleSince()
	assert.GreaterOrEqual(t, last, before, "IdleSince должно быть выставлено в NewSession")
	assert.LessOrEqual(t, last, after)
}

func TestSession_Touch_AdvancesIdleSince(t *testing.T) {
	t.Parallel()

	conn := newUDPConn(t)
	t.Cleanup(func() { _ = conn.Close() })

	s := udp.NewSession(mustAddrPort(t, "127.0.0.1:1"), conn, &model.Backend{})
	first := s.IdleSince()

	time.Sleep(2 * time.Millisecond)
	s.Touch()
	second := s.IdleSince()

	assert.Greater(t, second, first, "Touch обязан продвигать LastActive")
}

func TestSession_Close_ClosesBackendConn(t *testing.T) {
	t.Parallel()

	conn := newUDPConn(t)
	s := udp.NewSession(mustAddrPort(t, "127.0.0.1:1"), conn, &model.Backend{})

	require.NoError(t, s.Close())

	_, err := conn.Write([]byte("x"))
	require.Error(t, err)
	assert.True(t, errors.Is(err, net.ErrClosed),
		"BackendConn должен быть закрыт после Session.Close")
}

func TestSession_Close_Idempotent(t *testing.T) {
	t.Parallel()

	conn := newUDPConn(t)
	s := udp.NewSession(mustAddrPort(t, "127.0.0.1:1"), conn, &model.Backend{})

	require.NoError(t, s.Close())
	require.NoError(t, s.Close(), "повторный Close — no-op без ошибки")
	require.NoError(t, s.Close())
}

func TestSession_Close_SuppressesNetErrClosed(t *testing.T) {
	t.Parallel()

	conn := newUDPConn(t)
	// закрываем conn до Session.Close, чтобы внутренний BackendConn.Close
	// вернул net.ErrClosed — Session.Close обязан его проглотить.
	require.NoError(t, conn.Close())

	s := udp.NewSession(mustAddrPort(t, "127.0.0.1:1"), conn, &model.Backend{})
	assert.NoError(t, s.Close(),
		"net.ErrClosed от уже закрытого backend conn должен подавляться")
}

func TestSession_Close_Concurrent(t *testing.T) {
	t.Parallel()

	conn := newUDPConn(t)
	s := udp.NewSession(mustAddrPort(t, "127.0.0.1:1"), conn, &model.Backend{})

	const N = 32
	var wg sync.WaitGroup
	wg.Add(N)
	errs := make(chan error, N)
	for range N {
		go func() {
			defer wg.Done()
			errs <- s.Close()
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		assert.NoError(t, err)
	}
}
