package tcp_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/sanchey92/flowgate/internal/domain/model"
	"github.com/sanchey92/flowgate/internal/proxy/tcp"
	"github.com/sanchey92/flowgate/internal/proxy/tcp/mocks"
)

func anyPool(t *testing.T) *mocks.BufferPool {
	t.Helper()
	p := mocks.NewBufferPool(t)
	p.EXPECT().Get().RunAndReturn(func() *[]byte {
		b := make([]byte, testBufSize)
		return &b
	}).Maybe()
	p.EXPECT().Put(mock.Anything).Return().Maybe()
	return p
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func defaultTimeouts() *tcp.Timeouts {
	return &tcp.Timeouts{
		Connect:         time.Second,
		Idle:            0,
		KeepAlivePeriod: 0,
	}
}

func dialerReturning(c net.Conn) tcp.Dialer {
	return func(_ context.Context, _, _ string) (net.Conn, error) {
		return c, nil
	}
}

func waitDone(t *testing.T, done <-chan struct{}, timeout time.Duration, msg string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(timeout):
		t.Fatal(msg)
	}
}

func TestNewHandler_NilDialer_UsesNetDialer(t *testing.T) {
	t.Parallel()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err == nil {
			accepted <- c
		}
	}()

	be := &model.Backend{ID: "be-1", Addr: ln.Addr().String()}
	balancer := mocks.NewBalancer(t)
	balancer.EXPECT().Pick().Return(be, nil)
	balancer.EXPECT().Release(be).Return()

	h := tcp.NewHandler(balancer, anyPool(t), defaultTimeouts(), tcp.ProxyProtoOff, discardLogger(), nil)

	proxyClient, externalClient := tcpPair(t)
	require.NoError(t, externalClient.Close()) // EOF на стороне клиента

	handleDone := make(chan struct{})
	go func() {
		h.Handle(context.Background(), proxyClient)
		close(handleDone)
	}()

	select {
	case c := <-accepted:
		_ = c.Close() // отпускаем pipe со стороны бекенда
	case <-time.After(2 * time.Second):
		t.Fatal("default dialer did not connect to test listener")
	}

	waitDone(t, handleDone, 2*time.Second, "Handle did not return")
}

func TestNewHandler_ProxyProtoOff_NoStartupWarn(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	balancer := mocks.NewBalancer(t)
	_ = tcp.NewHandler(balancer, anyPool(t), defaultTimeouts(), tcp.ProxyProtoOff, log, nil)

	assert.NotContains(t, buf.String(), "proxy protocol",
		"при ProxyProtoOff не должно быть warn про proxy protocol")
}

func TestHandle_HappyPath_BidirectionalForwarding(t *testing.T) {
	t.Parallel()

	proxyClient, externalClient := tcpPair(t)
	backendProxy, backendSide := tcpPair(t)

	be := &model.Backend{ID: "be-1", Addr: "ignored:0"}
	balancer := mocks.NewBalancer(t)
	balancer.EXPECT().Pick().Return(be, nil)
	balancer.EXPECT().Release(be).Return()

	h := tcp.NewHandler(balancer, newPool(t), defaultTimeouts(),
		tcp.ProxyProtoOff, discardLogger(), dialerReturning(backendProxy))

	handleDone := make(chan struct{})
	go func() {
		h.Handle(context.Background(), proxyClient)
		close(handleDone)
	}()

	_, err := externalClient.Write([]byte("ping"))
	require.NoError(t, err)
	require.NoError(t, tcp.CloseWrite(externalClient))

	backendIn, err := io.ReadAll(backendSide)
	require.NoError(t, err)
	assert.Equal(t, "ping", string(backendIn))

	_, err = backendSide.Write([]byte("pong"))
	require.NoError(t, err)
	require.NoError(t, tcp.CloseWrite(backendSide))

	externalIn, err := io.ReadAll(externalClient)
	require.NoError(t, err)
	assert.Equal(t, "pong", string(externalIn))

	waitDone(t, handleDone, 2*time.Second, "Handle did not return after both halves closed")
}

func TestHandle_PickError_DropsClientNoRelease(t *testing.T) {
	t.Parallel()

	balancer := mocks.NewBalancer(t)
	balancer.EXPECT().Pick().Return(nil, errors.New("no backends"))

	h := tcp.NewHandler(balancer, anyPool(t), defaultTimeouts(),
		tcp.ProxyProtoOff, discardLogger(), nil)

	proxyClient, externalClient := tcpPair(t)

	done := make(chan struct{})
	go func() {
		h.Handle(context.Background(), proxyClient)
		close(done)
	}()

	waitDone(t, done, time.Second, "Handle did not return after pick error")

	require.NoError(t, externalClient.SetReadDeadline(time.Now().Add(time.Second)))
	_, err := externalClient.Read(make([]byte, 1))
	assert.Error(t, err, "клиентское соединение должно быть закрыто после Pick error")
}

func TestHandle_DialError_ReleasesBackend(t *testing.T) {
	t.Parallel()

	be := &model.Backend{ID: "be-1", Addr: "1.2.3.4:9"}
	balancer := mocks.NewBalancer(t)
	balancer.EXPECT().Pick().Return(be, nil)
	balancer.EXPECT().Release(be).Return()

	dial := func(_ context.Context, _, _ string) (net.Conn, error) {
		return nil, errors.New("connection refused")
	}

	h := tcp.NewHandler(balancer, anyPool(t), defaultTimeouts(),
		tcp.ProxyProtoOff, discardLogger(), dial)

	proxyClient, _ := tcpPair(t)

	done := make(chan struct{})
	go func() {
		h.Handle(context.Background(), proxyClient)
		close(done)
	}()

	waitDone(t, done, time.Second, "Handle did not return after dial error")
}

func TestHandle_DialContextTimeout(t *testing.T) {
	t.Parallel()

	be := &model.Backend{ID: "be-1", Addr: "1.2.3.4:9"}
	balancer := mocks.NewBalancer(t)
	balancer.EXPECT().Pick().Return(be, nil)
	balancer.EXPECT().Release(be).Return()

	dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}

	timeouts := &tcp.Timeouts{Connect: 50 * time.Millisecond}
	h := tcp.NewHandler(balancer, anyPool(t), timeouts,
		tcp.ProxyProtoOff, discardLogger(), dial)

	proxyClient, _ := tcpPair(t)

	start := time.Now()
	h.Handle(context.Background(), proxyClient)
	elapsed := time.Since(start)

	assert.GreaterOrEqual(t, elapsed, 50*time.Millisecond,
		"Handle обязан соблюдать connect timeout")
	assert.Less(t, elapsed, 500*time.Millisecond,
		"Handle не должен висеть после connect timeout")
}

func TestHandle_ContextCancel_TerminatesPipe(t *testing.T) {
	t.Parallel()

	proxyClient, _ := tcpPair(t)
	backendProxy, _ := tcpPair(t)

	be := &model.Backend{ID: "be-1", Addr: "ignored:0"}
	balancer := mocks.NewBalancer(t)
	balancer.EXPECT().Pick().Return(be, nil)
	balancer.EXPECT().Release(be).Return()

	h := tcp.NewHandler(balancer, newPool(t), defaultTimeouts(),
		tcp.ProxyProtoOff, discardLogger(), dialerReturning(backendProxy))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		h.Handle(ctx, proxyClient)
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	waitDone(t, done, 2*time.Second, "Handle did not return after ctx cancel")
}

func TestHandle_ProxyProtoEnabled_StartupWarnButNotPerCall(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	proxyClient, externalClient := tcpPair(t)
	backendProxy, backendSide := tcpPair(t)

	be := &model.Backend{ID: "be-1", Addr: "ignored:0"}
	balancer := mocks.NewBalancer(t)
	balancer.EXPECT().Pick().Return(be, nil)
	balancer.EXPECT().Release(be).Return()

	h := tcp.NewHandler(balancer, newPool(t), defaultTimeouts(),
		tcp.ProxyProtoV2, log, dialerReturning(backendProxy))

	require.Equal(t, 1,
		strings.Count(buf.String(), "proxy protocol mode is not implemented"),
		"warn про proxy protocol должен появиться ровно один раз — при создании Handler")

	buf.Reset()

	handleDone := make(chan struct{})
	go func() {
		h.Handle(context.Background(), proxyClient)
		close(handleDone)
	}()

	// быстрый разрыв с обеих сторон, чтобы Handle вышел
	require.NoError(t, externalClient.Close())
	require.NoError(t, backendSide.Close())

	waitDone(t, handleDone, 2*time.Second, "Handle did not return")

	assert.NotContains(t, buf.String(), "proxy protocol",
		"Handle не должен логировать proxy proto warn на каждое соединение")
}
