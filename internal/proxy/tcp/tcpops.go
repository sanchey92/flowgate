package tcp

import (
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

func SetKeepAlive(c net.Conn, period time.Duration) error {
	tc, ok := c.(*net.TCPConn)
	if !ok {
		return nil
	}
	if err := tc.SetKeepAlive(true); err != nil {
		return fmt.Errorf("tcp: set keepalive: %w", err)
	}
	if period > 0 {
		if err := tc.SetKeepAlivePeriod(period); err != nil {
			return fmt.Errorf("tcp: set keepalive period: %w", err)
		}
	}
	return nil
}

func CloseWrite(c net.Conn) error {
	if tc, ok := c.(*net.TCPConn); ok {
		if err := tc.CloseWrite(); err != nil {
			return fmt.Errorf("tcp: close write: %w", err)
		}
		return nil
	}
	if err := c.Close(); err != nil {
		return fmt.Errorf("tcp: close (no half-close): %w", err)
	}
	return nil
}

func SetDeadlines(c net.Conn, idle time.Duration) error {
	if idle <= 0 {
		return nil
	}
	if err := c.SetReadDeadline(time.Now().Add(idle)); err != nil {
		return fmt.Errorf("tcp; set deadline: %w", err)
	}
	return nil
}

func IsBenignClose(err error) bool {
	if err == nil {
		return true
	}
	if errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrClosedPipe) ||
		errors.Is(err, net.ErrClosed) {
		return true
	}
	return false
}
