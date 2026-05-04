package udp

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync/atomic"
	"time"

	"github.com/sanchey92/flowgate/internal/domain/model"
)

type Session struct {
	ClientAddr  netip.AddrPort
	BackendConn *net.UDPConn
	Backend     *model.Backend

	LastActive atomic.Int64
	closed     atomic.Bool
}

func NewSession(client netip.AddrPort, conn *net.UDPConn, b *model.Backend) *Session {
	s := &Session{
		ClientAddr:  client,
		BackendConn: conn,
		Backend:     b,
	}
	s.Touch()
	return s
}

func (s *Session) Touch() {
	s.LastActive.Store(time.Now().UnixNano())
}

func (s *Session) IdleSince() int64 {
	return s.LastActive.Load()
}

func (s *Session) Close() error {
	if !s.closed.CompareAndSwap(false, true) {
		return nil
	}
	if err := s.BackendConn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		return fmt.Errorf("udp: session close backend: %w", err)
	}
	return nil
}
