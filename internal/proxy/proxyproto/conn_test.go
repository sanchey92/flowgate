package proxyproto

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"
)

type fakeConn struct {
	closed     atomic.Bool
	closeErr   error
	localAddr  net.Addr
	remoteAddr net.Addr
}

func newFakeConn() *fakeConn {
	return &fakeConn{
		localAddr:  &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1111},
		remoteAddr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 2222},
	}
}

func (f *fakeConn) Read(_ []byte) (int, error)       { return 0, io.EOF }
func (f *fakeConn) Write(p []byte) (int, error)      { return len(p), nil }
func (f *fakeConn) LocalAddr() net.Addr              { return f.localAddr }
func (f *fakeConn) RemoteAddr() net.Addr             { return f.remoteAddr }
func (f *fakeConn) SetDeadline(time.Time) error      { return nil }
func (f *fakeConn) SetReadDeadline(time.Time) error  { return nil }
func (f *fakeConn) SetWriteDeadline(time.Time) error { return nil }
func (f *fakeConn) Close() error {
	f.closed.Store(true)
	return f.closeErr
}

type halfCloseFakeConn struct {
	fakeConn
	closeWriteCalled atomic.Bool
	closeWriteErr    error
}

func (f *halfCloseFakeConn) CloseWrite() error {
	f.closeWriteCalled.Store(true)
	return f.closeWriteErr
}

func newReader() *bufio.Reader {
	return bufio.NewReaderSize(bytes.NewReader(nil), minBuffSize)
}

func TestTaggedConn_CloseWrite_DelegatesToUnderlying(t *testing.T) {
	base := &halfCloseFakeConn{fakeConn: *newFakeConn()}
	c := WrapConn(base, newReader(), nil)

	if err := c.(interface{ CloseWrite() error }).CloseWrite(); err != nil {
		t.Fatalf("CloseWrite: unexpected error: %v", err)
	}
	if !base.closeWriteCalled.Load() {
		t.Fatal("expected underlying CloseWrite to be called")
	}
	if base.closed.Load() {
		t.Fatal("Close must not be called when underlying supports half-close")
	}
}

func TestTaggedConn_CloseWrite_FallsBackToCloseWhenUnsupported(t *testing.T) {
	base := newFakeConn()
	c := WrapConn(base, newReader(), nil)

	if err := c.(interface{ CloseWrite() error }).CloseWrite(); err != nil {
		t.Fatalf("CloseWrite: unexpected error: %v", err)
	}
	if !base.closed.Load() {
		t.Fatal("expected fallback to Close when underlying lacks CloseWrite")
	}
}

func TestTaggedConn_CloseWrite_WrapsUnderlyingError(t *testing.T) {
	wantErr := errors.New("boom")
	base := &halfCloseFakeConn{fakeConn: *newFakeConn(), closeWriteErr: wantErr}
	c := WrapConn(base, newReader(), nil)

	err := c.(interface{ CloseWrite() error }).CloseWrite()
	if err == nil {
		t.Fatal("expected error to propagate")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("error chain must wrap underlying err: got %v", err)
	}
}

func TestTaggedConn_AddrsOverridenForProxyHeader(t *testing.T) {
	base := newFakeConn()
	src := netip.MustParseAddrPort("1.2.3.4:1234")
	dst := netip.MustParseAddrPort("5.6.7.8:5678")
	hdr := &Header{Version: 2, Command: CommandProxy, Src: src, Dst: dst}

	c := WrapConn(base, newReader(), hdr)

	if got := c.RemoteAddr().String(); got != src.String() {
		t.Fatalf("RemoteAddr: got %q, want %q", got, src.String())
	}
	if got := c.LocalAddr().String(); got != dst.String() {
		t.Fatalf("LocalAddr: got %q, want %q", got, dst.String())
	}
}

func TestTaggedConn_AddrsPassThroughForLocalCommand(t *testing.T) {
	base := newFakeConn()
	hdr := &Header{Version: 2, Command: CommandLocal}

	c := WrapConn(base, newReader(), hdr)

	if c.RemoteAddr() != base.RemoteAddr() {
		t.Fatal("LOCAL command must not override RemoteAddr")
	}
	if c.LocalAddr() != base.LocalAddr() {
		t.Fatal("LOCAL command must not override LocalAddr")
	}
}

func TestTaggedConn_AddrsPassThroughForNilHeader(t *testing.T) {
	base := newFakeConn()

	c := WrapConn(base, newReader(), nil)

	if c.RemoteAddr() != base.RemoteAddr() {
		t.Fatal("nil header must not override RemoteAddr")
	}
	if c.LocalAddr() != base.LocalAddr() {
		t.Fatal("nil header must not override LocalAddr")
	}
}
