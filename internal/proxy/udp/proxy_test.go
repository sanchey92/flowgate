package udp_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	domainErr "github.com/sanchey92/flowgate/internal/domain/errors"
	"github.com/sanchey92/flowgate/internal/domain/model"
	"github.com/sanchey92/flowgate/internal/proxy/udp"
	"github.com/sanchey92/flowgate/internal/proxy/udp/mocks"
)

const (
	testSessionIdle = time.Second
	testBackendRead = 200 * time.Millisecond
	testDialTimeout = 200 * time.Millisecond
	syncTimeout     = 2 * time.Second
)

func newTestTimeouts() udp.Timeouts {
	return udp.Timeouts{
		SessionIdle: testSessionIdle,
		BackendRead: testBackendRead,
		Dial:        testDialTimeout,
	}
}

func newTestProxy(t *testing.T, b udp.Balancer) *udp.Proxy {
	t.Helper()
	return udp.New("test", "127.0.0.1:0", b, newTestTimeouts(), discardLogger(), nil)
}

func startEchoBackend(t *testing.T) *net.UDPConn {
	t.Helper()
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	go func() {
		buf := make([]byte, 4096)
		for {
			n, from, err := c.ReadFromUDPAddrPort(buf)
			if err != nil {
				return
			}
			_, _ = c.WriteToUDPAddrPort(buf[:n], from)
		}
	}()
	return c
}

func TestProxy_New_AddrNilBeforeStart(t *testing.T) {
	t.Parallel()

	p := newTestProxy(t, mocks.NewBalancer(t))
	require.NotNil(t, p)
	assert.Nil(t, p.Addr())
}

func TestProxy_StateTransitions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		run  func(t *testing.T, p *udp.Proxy)
	}{
		{
			name: "start succeeds and exposes addr",
			run: func(t *testing.T, p *udp.Proxy) {
				require.NoError(t, p.Start(context.Background()))
				assert.NotNil(t, p.Addr(), "Addr должен быть доступен после Start")

				require.NoError(t, p.Shutdown(context.Background()))
				assert.Nil(t, p.Addr(), "Addr должен очищаться после Shutdown")
			},
		},
		{
			name: "double start returns ErrProxyStarted",
			run: func(t *testing.T, p *udp.Proxy) {
				require.NoError(t, p.Start(context.Background()))
				err := p.Start(context.Background())
				assert.ErrorIs(t, err, domainErr.ErrProxyStarted)
				require.NoError(t, p.Shutdown(context.Background()))
			},
		},
		{
			name: "start after shutdown returns ErrProxyStopped",
			run: func(t *testing.T, p *udp.Proxy) {
				require.NoError(t, p.Start(context.Background()))
				require.NoError(t, p.Shutdown(context.Background()))

				err := p.Start(context.Background())
				assert.ErrorIs(t, err, domainErr.ErrProxyStopped)
			},
		},
		{
			name: "shutdown without start is noop",
			run: func(t *testing.T, p *udp.Proxy) {
				require.NoError(t, p.Shutdown(context.Background()))
			},
		},
		{
			name: "shutdown is idempotent",
			run: func(t *testing.T, p *udp.Proxy) {
				require.NoError(t, p.Start(context.Background()))
				require.NoError(t, p.Shutdown(context.Background()))
				require.NoError(t, p.Shutdown(context.Background()))
			},
		},
		{
			name: "shutdown accepts nil ctx",
			run: func(t *testing.T, p *udp.Proxy) {
				require.NoError(t, p.Start(context.Background()))
				require.NoError(t, p.Shutdown(nil)) //nolint:staticcheck // nil ctx разрешён контрактом Shutdown
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := newTestProxy(t, mocks.NewBalancer(t))
			tt.run(t, p)
		})
	}
}

func TestProxy_Start_InvalidSessionIdle(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		idle time.Duration
	}{
		{"zero", 0},
		{"negative", -time.Second},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			p := udp.New("test", "127.0.0.1:0", mocks.NewBalancer(t),
				udp.Timeouts{SessionIdle: tc.idle, BackendRead: testBackendRead, Dial: testDialTimeout},
				discardLogger(), nil)

			err := p.Start(context.Background())
			require.Error(t, err)
			assert.Contains(t, err.Error(), "SessionIdle")
			assert.Nil(t, p.Addr(), "при ошибке валидации Addr должен оставаться nil")
		})
	}
}

func TestProxy_Start_ListenError(t *testing.T) {
	t.Parallel()

	occupied, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	require.NoError(t, err)
	t.Cleanup(func() { _ = occupied.Close() })

	p := udp.New("test", occupied.LocalAddr().String(), mocks.NewBalancer(t),
		newTestTimeouts(), discardLogger(), nil)

	err = p.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "listen")
	assert.Nil(t, p.Addr())
}

func TestProxy_Forwarding_HappyPath(t *testing.T) {
	t.Parallel()

	backend := startEchoBackend(t)
	be := &model.Backend{ID: "be-1", Addr: backend.LocalAddr().String()}

	bal := mocks.NewBalancer(t)
	bal.EXPECT().Pick().Return(be, nil).Once()
	bal.EXPECT().Release(be).Once() // вызов через Table.Close → onEvict при Shutdown

	p := newTestProxy(t, bal)
	require.NoError(t, p.Start(context.Background()))
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })

	client, err := net.Dial("udp", p.Addr().String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	_, err = client.Write([]byte("ping"))
	require.NoError(t, err)

	require.NoError(t, client.SetReadDeadline(time.Now().Add(syncTimeout)))
	buf := make([]byte, 1024)
	n, err := client.Read(buf)
	require.NoError(t, err)
	assert.Equal(t, "ping", string(buf[:n]))
}

func TestProxy_Forwarding_ReusesSessionForSameClient(t *testing.T) {
	t.Parallel()

	backend := startEchoBackend(t)
	be := &model.Backend{ID: "be-1", Addr: backend.LocalAddr().String()}

	// инвариант: один Pick на client addr, многократные пакеты идут в ту же сессию
	bal := mocks.NewBalancer(t)
	bal.EXPECT().Pick().Return(be, nil).Once()
	bal.EXPECT().Release(be).Once()

	p := newTestProxy(t, bal)
	require.NoError(t, p.Start(context.Background()))
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })

	client, err := net.Dial("udp", p.Addr().String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	require.NoError(t, client.SetReadDeadline(time.Now().Add(syncTimeout)))
	for i := 0; i < 3; i++ {
		msg := []byte(fmt.Sprintf("msg-%d", i))
		_, err := client.Write(msg)
		require.NoError(t, err)

		buf := make([]byte, 1024)
		n, err := client.Read(buf)
		require.NoError(t, err)
		assert.Equal(t, msg, buf[:n])
	}
}

func TestProxy_Forwarding_MultipleClients(t *testing.T) {
	t.Parallel()

	backend := startEchoBackend(t)
	be := &model.Backend{ID: "be-1", Addr: backend.LocalAddr().String()}

	bal := mocks.NewBalancer(t)
	bal.EXPECT().Pick().Return(be, nil).Twice()
	bal.EXPECT().Release(be).Twice()

	p := newTestProxy(t, bal)
	require.NoError(t, p.Start(context.Background()))
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })

	const clients = 2
	var wg sync.WaitGroup
	wg.Add(clients)
	errs := make(chan error, clients)

	for i := 0; i < clients; i++ {
		i := i
		go func() {
			defer wg.Done()

			c, err := net.Dial("udp", p.Addr().String())
			if err != nil {
				errs <- fmt.Errorf("dial: %w", err)
				return
			}
			defer func() { _ = c.Close() }()

			msg := []byte(fmt.Sprintf("client-%d", i))
			if _, err := c.Write(msg); err != nil {
				errs <- fmt.Errorf("write: %w", err)
				return
			}

			_ = c.SetReadDeadline(time.Now().Add(syncTimeout))
			buf := make([]byte, 1024)
			n, err := c.Read(buf)
			if err != nil {
				errs <- fmt.Errorf("read: %w", err)
				return
			}
			if string(buf[:n]) != string(msg) {
				errs <- fmt.Errorf("client %d: got %q, want %q", i, buf[:n], msg)
			}
		}()
	}

	wg.Wait()
	close(errs)
	for e := range errs {
		require.NoError(t, e)
	}
}

func TestProxy_PickError_DoesNotCreateSession(t *testing.T) {
	t.Parallel()

	picked := make(chan struct{})
	bal := mocks.NewBalancer(t)
	bal.EXPECT().Pick().
		Run(func() { close(picked) }).
		Return(nil, errors.New("no backends")).Once()
	// Release НЕ должен вызываться: balancer не выдал backend

	p := newTestProxy(t, bal)
	require.NoError(t, p.Start(context.Background()))
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })

	client, err := net.Dial("udp", p.Addr().String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	_, err = client.Write([]byte("ping"))
	require.NoError(t, err)

	select {
	case <-picked:
	case <-time.After(syncTimeout):
		t.Fatal("Pick не был вызван")
	}
}

func TestProxy_DialError_ReleasesBackend(t *testing.T) {
	t.Parallel()

	be := &model.Backend{ID: "be-x", Addr: "127.0.0.1:0"}

	released := make(chan struct{})
	bal := mocks.NewBalancer(t)
	bal.EXPECT().Pick().Return(be, nil).Once()
	bal.EXPECT().Release(be).
		Run(func(*model.Backend) { close(released) }).
		Once()

	dialer := func(_ context.Context, _, _ string) (net.Conn, error) {
		return nil, errors.New("dial fails")
	}
	p := udp.New("test", "127.0.0.1:0", bal, newTestTimeouts(), discardLogger(), dialer)
	require.NoError(t, p.Start(context.Background()))
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })

	client, err := net.Dial("udp", p.Addr().String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	_, err = client.Write([]byte("ping"))
	require.NoError(t, err)

	select {
	case <-released:
	case <-time.After(syncTimeout):
		t.Fatal("Release не был вызван после ошибки dial")
	}
}

func TestProxy_DialReturnsNonUDPConn_ReleasesBackend(t *testing.T) {
	t.Parallel()

	be := &model.Backend{ID: "be-x", Addr: "127.0.0.1:0"}

	released := make(chan struct{})
	bal := mocks.NewBalancer(t)
	bal.EXPECT().Pick().Return(be, nil).Once()
	bal.EXPECT().Release(be).
		Run(func(*model.Backend) { close(released) }).
		Once()

	pipeC, pipeS := net.Pipe()
	t.Cleanup(func() {
		_ = pipeC.Close()
		_ = pipeS.Close()
	})

	dialer := func(_ context.Context, _, _ string) (net.Conn, error) {
		return pipeC, nil
	}
	p := udp.New("test", "127.0.0.1:0", bal, newTestTimeouts(), discardLogger(), dialer)
	require.NoError(t, p.Start(context.Background()))
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })

	client, err := net.Dial("udp", p.Addr().String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	_, err = client.Write([]byte("ping"))
	require.NoError(t, err)

	select {
	case <-released:
	case <-time.After(syncTimeout):
		t.Fatal("Release не был вызван при не-UDP соединении от dialer")
	}
}

func TestProxy_ParentCtxCancel_StopsProxy(t *testing.T) {
	t.Parallel()

	p := newTestProxy(t, mocks.NewBalancer(t))

	ctx, cancel := context.WithCancel(context.Background())
	require.NoError(t, p.Start(ctx))
	cancel()

	require.NoError(t, p.Shutdown(context.Background()))
	assert.Nil(t, p.Addr())
}
