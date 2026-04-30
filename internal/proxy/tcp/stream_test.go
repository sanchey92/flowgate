package tcp_test

import (
	"context"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/sanchey92/flowgate/internal/proxy/tcp"
	"github.com/sanchey92/flowgate/internal/proxy/tcp/mocks"
)

const testBufSize = 32 * 1024

func newPool(t *testing.T) *mocks.BufferPool {
	t.Helper()
	p := mocks.NewBufferPool(t)
	p.EXPECT().Get().RunAndReturn(func() *[]byte {
		b := make([]byte, testBufSize)
		return &b
	})
	p.EXPECT().Put(mock.Anything).Return()
	return p
}

func tcpPair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()

	type accepted struct {
		c   net.Conn
		err error
	}
	ch := make(chan accepted, 1)
	go func() {
		c, err := ln.Accept()
		ch <- accepted{c, err}
	}()

	client, err := net.Dial("tcp", ln.Addr().String())
	require.NoError(t, err)

	srv := <-ch
	require.NoError(t, srv.err)

	t.Cleanup(func() {
		_ = client.Close()
		_ = srv.c.Close()
	})
	return client, srv.c
}

func TestPipePair_BidirectionalSucceeds(t *testing.T) {
	t.Parallel()

	clientA, serverA := tcpPair(t)
	clientB, serverB := tcpPair(t)

	dataAB := []byte("hello from peer A")
	dataBA := []byte("greetings from peer B, slightly longer payload")

	go func() {
		_, _ = clientA.Write(dataAB)
		_ = tcp.CloseWrite(clientA)
	}()
	go func() {
		_, _ = clientB.Write(dataBA)
		_ = tcp.CloseWrite(clientB)
	}()

	var (
		readOnB, readOnA []byte
		readWG           sync.WaitGroup
	)
	readWG.Add(2)
	go func() {
		defer readWG.Done()
		readOnB, _ = io.ReadAll(clientB)
	}()
	go func() {
		defer readWG.Done()
		readOnA, _ = io.ReadAll(clientA)
	}()

	bytesAB, bytesBA, err := tcp.PipePair(context.Background(), serverA, serverB, newPool(t), 0)
	require.NoError(t, err)

	readWG.Wait()
	assert.Equal(t, int64(len(dataAB)), bytesAB)
	assert.Equal(t, int64(len(dataBA)), bytesBA)
	assert.Equal(t, dataAB, readOnB)
	assert.Equal(t, dataBA, readOnA)
}

func TestPipePair_ContextCancelClosesConnections(t *testing.T) {
	t.Parallel()

	clientA, serverA := tcpPair(t)
	clientB, serverB := tcpPair(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		_, _, _ = tcp.PipePair(ctx, serverA, serverB, newPool(t), 0)
		close(done)
	}()

	_, err := clientA.Write([]byte("ping"))
	require.NoError(t, err)
	buf := make([]byte, 4)
	_, err = io.ReadFull(clientB, buf)
	require.NoError(t, err)
	assert.Equal(t, []byte("ping"), buf)

	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("PipePair did not return after ctx cancel")
	}

	require.NoError(t, clientA.SetReadDeadline(time.Now().Add(time.Second)))
	require.NoError(t, clientB.SetReadDeadline(time.Now().Add(time.Second)))
	_, errA := clientA.Read(make([]byte, 1))
	_, errB := clientB.Read(make([]byte, 1))
	assert.Error(t, errA)
	assert.Error(t, errB)
}

func TestPipePair_AlreadyCancelledContextReturnsImmediately(t *testing.T) {
	t.Parallel()

	_, serverA := tcpPair(t)
	_, serverB := tcpPair(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() {
		_, _, _ = tcp.PipePair(ctx, serverA, serverB, newPool(t), 0)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("PipePair must return promptly when ctx is already cancelled")
	}
}

func TestPipePair_BothSidesEOFReturnsZeroNoError(t *testing.T) {
	t.Parallel()

	clientA, serverA := tcpPair(t)
	clientB, serverB := tcpPair(t)

	require.NoError(t, clientA.Close())
	require.NoError(t, clientB.Close())

	bytesAB, bytesBA, err := tcp.PipePair(context.Background(), serverA, serverB, newPool(t), 0)
	require.NoError(t, err)
	assert.Equal(t, int64(0), bytesAB)
	assert.Equal(t, int64(0), bytesBA)
}

func TestPipePair_BufferPoolGetPutBalanced(t *testing.T) {
	t.Parallel()

	clientA, serverA := tcpPair(t)
	clientB, serverB := tcpPair(t)

	require.NoError(t, clientA.Close())
	require.NoError(t, clientB.Close())

	var gets, puts atomic.Int64
	p := mocks.NewBufferPool(t)
	p.EXPECT().Get().RunAndReturn(func() *[]byte {
		gets.Add(1)
		b := make([]byte, testBufSize)
		return &b
	})
	p.EXPECT().Put(mock.Anything).Run(func(*[]byte) {
		puts.Add(1)
	}).Return()

	_, _, err := tcp.PipePair(context.Background(), serverA, serverB, p, 0)
	require.NoError(t, err)

	assert.Equal(t, int64(2), gets.Load(), "Get must be called once per direction")
	assert.Equal(t, int64(2), puts.Load(), "Put must match Get count")
}
