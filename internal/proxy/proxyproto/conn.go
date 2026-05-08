package proxyproto

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
)

type taggedConn struct {
	net.Conn
	r          *bufio.Reader
	realRemote net.Addr
	realLocal  net.Addr
}

func WrapConn(base net.Conn, r *bufio.Reader, header *Header) net.Conn {
	var realRemote, realLocal net.Addr
	if header != nil && !header.IsLocal() {
		realRemote = net.TCPAddrFromAddrPort(header.Src)
		realLocal = net.TCPAddrFromAddrPort(header.Dst)
	}
	return &taggedConn{
		Conn:       base,
		r:          r,
		realRemote: realRemote,
		realLocal:  realLocal,
	}
}

func (c *taggedConn) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if err != nil {
		if errors.Is(err, io.EOF) {
			return n, io.EOF
		}
		return n, fmt.Errorf("proxyproto tagged conn: %w", err)
	}
	return n, nil
}

func (c *taggedConn) RemoteAddr() net.Addr {
	if c.realRemote != nil {
		return c.realRemote
	}
	return c.Conn.RemoteAddr()
}

func (c *taggedConn) LocalAddr() net.Addr {
	if c.realLocal != nil {
		return c.realLocal
	}
	return c.Conn.LocalAddr()
}
