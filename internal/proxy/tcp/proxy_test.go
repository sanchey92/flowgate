package tcp_test

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	domainErr "github.com/sanchey92/flowgate/internal/domain/errors"
	"github.com/sanchey92/flowgate/internal/proxy/tcp"
	"github.com/sanchey92/flowgate/internal/proxy/tcp/mocks"
)

type proxyDeps struct {
	handler *mocks.Stream
	limiter *mocks.Limiter
	backoff *mocks.Backoff
}

func newProxyDeps(t *testing.T) proxyDeps {
	t.Helper()
	return proxyDeps{
		handler: mocks.NewStream(t),
		limiter: mocks.NewLimiter(t),
		backoff: mocks.NewBackoff(t),
	}
}

func newTestProxy(t *testing.T, deps proxyDeps) *tcp.Proxy {
	t.Helper()
	return tcp.New(
		"test",
		"127.0.0.1:0",
		deps.handler,
		deps.limiter,
		deps.backoff,
		discardLogger(),
	)
}

func TestProxy_New_AddrNilBeforeStart(t *testing.T) {
	t.Parallel()

	p := newTestProxy(t, newProxyDeps(t))
	require.NotNil(t, p)
	assert.Nil(t, p.Addr())
}

func TestProxy_StateTransitions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		run  func(t *testing.T, p *tcp.Proxy)
	}{
		{
			name: "start succeeds and exposes addr",
			run: func(t *testing.T, p *tcp.Proxy) {
				require.NoError(t, p.Start(context.Background()))
				assert.NotNil(t, p.Addr(), "Addr must be available after Start")

				require.NoError(t, p.Shutdown(context.Background()))
				assert.Nil(t, p.Addr(), "Addr must be cleared after Shutdown")
			},
		},
		{
			name: "double start returns ErrProxyStarted",
			run: func(t *testing.T, p *tcp.Proxy) {
				require.NoError(t, p.Start(context.Background()))
				err := p.Start(context.Background())
				assert.ErrorIs(t, err, domainErr.ErrProxyStarted)
				require.NoError(t, p.Shutdown(context.Background()))
			},
		},
		{
			name: "start after shutdown returns ErrProxyStopped",
			run: func(t *testing.T, p *tcp.Proxy) {
				require.NoError(t, p.Start(context.Background()))
				require.NoError(t, p.Shutdown(context.Background()))

				err := p.Start(context.Background())
				assert.ErrorIs(t, err, domainErr.ErrProxyStopped)
			},
		},
		{
			name: "shutdown without start is noop",
			run: func(t *testing.T, p *tcp.Proxy) {
				require.NoError(t, p.Shutdown(context.Background()))
			},
		},
		{
			name: "shutdown is idempotent",
			run: func(t *testing.T, p *tcp.Proxy) {
				require.NoError(t, p.Start(context.Background()))
				require.NoError(t, p.Shutdown(context.Background()))
				require.NoError(t, p.Shutdown(context.Background()))
			},
		},
		{
			name: "shutdown accepts nil ctx",
			run: func(t *testing.T, p *tcp.Proxy) {
				require.NoError(t, p.Start(context.Background()))
				require.NoError(t, p.Shutdown(nil)) //nolint:staticcheck // nil ctx is intentionally allowed by Shutdown
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := newTestProxy(t, newProxyDeps(t))
			tt.run(t, p)
		})
	}
}

func TestProxy_Start_ListenError(t *testing.T) {
	t.Parallel()

	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = occupied.Close() })

	deps := newProxyDeps(t)
	p := tcp.New(
		"test",
		occupied.Addr().String(),
		deps.handler,
		deps.limiter,
		deps.backoff,
		discardLogger(),
	)

	err = p.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "listen")
	assert.Nil(t, p.Addr())
}

func TestProxy_AcceptLoop(t *testing.T) {
	t.Parallel()

	type setupResult struct {
		handlerInvoked <-chan struct{}
	}

	tests := []struct {
		name  string
		setup func(deps proxyDeps) setupResult
	}{
		{
			name: "happy path: backoff reset, limiter cycle, handler invoked",
			setup: func(deps proxyDeps) setupResult {
				handled := make(chan struct{})
				deps.backoff.EXPECT().Reset().Once()
				deps.limiter.EXPECT().Acquire(mock.Anything).Return(nil).Once()
				deps.limiter.EXPECT().Release().Once()
				deps.handler.EXPECT().Handle(mock.Anything, mock.Anything).
					Run(func(_ context.Context, c net.Conn) {
						defer close(handled)
						_ = c.Close()
					}).Once()
				return setupResult{handlerInvoked: handled}
			},
		},
		{
			name: "limiter rejection: conn dropped, handler skipped",
			setup: func(deps proxyDeps) setupResult {
				deps.backoff.EXPECT().Reset().Once()
				deps.limiter.EXPECT().Acquire(mock.Anything).
					Return(errors.New("limit reached")).Once()
				return setupResult{handlerInvoked: nil}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			deps := newProxyDeps(t)
			res := tt.setup(deps)

			p := newTestProxy(t, deps)
			require.NoError(t, p.Start(context.Background()))
			t.Cleanup(func() { _ = p.Shutdown(context.Background()) })

			dial, err := net.Dial("tcp", p.Addr().String())
			require.NoError(t, err)
			t.Cleanup(func() { _ = dial.Close() })

			if res.handlerInvoked != nil {
				select {
				case <-res.handlerInvoked:
				case <-time.After(2 * time.Second):
					t.Fatal("handler was not invoked")
				}
				return
			}

			require.NoError(t, dial.SetReadDeadline(time.Now().Add(2*time.Second)))
			_, err = dial.Read(make([]byte, 1))
			assert.Error(t, err, "conn must be closed by proxy after limiter rejection")
		})
	}
}

func TestProxy_Shutdown_TimeoutWaitingForHandler(t *testing.T) {
	t.Parallel()

	deps := newProxyDeps(t)

	handlerStarted := make(chan struct{})
	handlerRelease := make(chan struct{})

	deps.backoff.EXPECT().Reset().Once()
	deps.limiter.EXPECT().Acquire(mock.Anything).Return(nil).Once()
	deps.limiter.EXPECT().Release().Once()
	deps.handler.EXPECT().Handle(mock.Anything, mock.Anything).
		Run(func(_ context.Context, _ net.Conn) {
			close(handlerStarted)
			<-handlerRelease
		}).Once()

	p := newTestProxy(t, deps)
	require.NoError(t, p.Start(context.Background()))

	dial, err := net.Dial("tcp", p.Addr().String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = dial.Close() })

	select {
	case <-handlerStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not start")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err = p.Shutdown(shutdownCtx)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)

	close(handlerRelease)

	finalCtx, finalCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer finalCancel()
	require.NoError(t, p.Shutdown(finalCtx))
}

func TestProxy_ParentCtxCancel_StopsProxy(t *testing.T) {
	t.Parallel()

	deps := newProxyDeps(t)
	p := newTestProxy(t, deps)

	ctx, cancel := context.WithCancel(context.Background())
	require.NoError(t, p.Start(ctx))
	cancel()

	require.NoError(t, p.Shutdown(context.Background()))
	assert.Nil(t, p.Addr())
}
