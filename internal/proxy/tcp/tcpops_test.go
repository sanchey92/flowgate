package tcp_test

import (
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sanchey92/flowgate/internal/proxy/tcp"
)

func TestSetKeepAlive_NonTCPIsNoop(t *testing.T) {
	t.Parallel()

	a, b := net.Pipe()
	t.Cleanup(func() {
		_ = a.Close()
		_ = b.Close()
	})

	err := tcp.SetKeepAlive(a, time.Second)
	assert.NoError(t, err, "non-TCP conn must not error")
}

func TestSetKeepAlive_TCPSucceeds(t *testing.T) {
	t.Parallel()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	var serverConn net.Conn
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		serverConn, _ = ln.Accept()
	}()

	clientConn, err := net.Dial("tcp", ln.Addr().String())
	require.NoError(t, err)
	wg.Wait()
	t.Cleanup(func() {
		_ = clientConn.Close()
		if serverConn != nil {
			_ = serverConn.Close()
		}
	})

	require.NoError(t, tcp.SetKeepAlive(clientConn, 30*time.Second))
}

func TestCloseWrite_TCPHalfClose(t *testing.T) {
	t.Parallel()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	srvDone := make(chan struct{})
	go func() {
		defer close(srvDone)
		s, err := ln.Accept()
		if err != nil {
			return
		}
		defer s.Close()

		_, _ = io.Copy(io.Discard, s)
		_, _ = s.Write([]byte("bye"))
	}()

	c, err := net.Dial("tcp", ln.Addr().String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	_, _ = c.Write([]byte("hi"))
	require.NoError(t, tcp.CloseWrite(c)) // FIN

	// Read должен вернуть ответ от сервера, не EOF — read-half ещё открыт.
	buf := make([]byte, 16)
	n, err := c.Read(buf)
	require.NoError(t, err)
	assert.Equal(t, "bye", string(buf[:n]))

	<-srvDone
}

func TestCloseWrite_NonTCPFullClose(t *testing.T) {
	t.Parallel()

	a, b := net.Pipe()
	t.Cleanup(func() { _ = b.Close() })

	require.NoError(t, tcp.CloseWrite(a)) // должен сделать полный Close на pipe

	_, err := a.Write([]byte("x"))
	assert.Error(t, err, "после closeWrite на pipe запись должна падать")
}

func TestIsBenignClose(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		err    error
		benign bool
	}{
		{"nil", nil, true},
		{"io.EOF", io.EOF, true},
		{"io.ErrClosedPipe", io.ErrClosedPipe, true},
		{"net.ErrClosed", net.ErrClosed, true},
		{"wrapped EOF", errors.Join(errors.New("ctx"), io.EOF), true},
		{"random error", errors.New("boom"), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.benign, tcp.IsBenignClose(tc.err))
		})
	}
}
