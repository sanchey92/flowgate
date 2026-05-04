package udp

import (
	"log/slog"
	"net/netip"
	"sync"
	"time"
)

type Table struct {
	ttl     time.Duration
	log     *slog.Logger
	onEvict func(*Session)

	sessions sync.Map

	closeOnce sync.Once
	done      chan struct{}
	wg        sync.WaitGroup
}

func NewTable(ttl, gcInterval time.Duration, log *slog.Logger, onEvict func(*Session)) *Table {
	t := &Table{
		ttl:     ttl,
		log:     log,
		onEvict: onEvict,
		done:    make(chan struct{}),
	}
	if ttl > 0 && gcInterval > 0 {
		t.wg.Add(1)
		go t.gc(gcInterval)
	}
	return t
}

func (t *Table) Get(addr netip.AddrPort) (*Session, bool) {
	v, ok := t.sessions.Load(addr)
	if !ok {
		return nil, false
	}
	return v.(*Session), true
}

func (t *Table) LoadOrStore(addr netip.AddrPort, s *Session) (*Session, bool) {
	actual, loaded := t.sessions.LoadOrStore(addr, s)
	return actual.(*Session), loaded
}

func (t *Table) Delete(addr netip.AddrPort) {
	v, loaded := t.sessions.LoadAndDelete(addr)
	if !loaded {
		return
	}
	s := v.(*Session)
	t.onEvict(s)
	if err := s.Close(); err != nil {
		t.log.Warn("udp: session close",
			slog.String("client", addr.String()),
			slog.Any("error", err),
		)
	}
}

func (t *Table) Close() {
	t.closeOnce.Do(func() {
		close(t.done)
	})
	t.wg.Wait()

	t.sessions.Range(func(key, val any) bool {
		addr := key.(netip.AddrPort)
		s := val.(*Session)

		if _, ok := t.sessions.LoadAndDelete(addr); !ok {
			return true
		}
		t.onEvict(s)
		if err := s.Close(); err != nil {
			t.log.Warn("udp: session close on shutdown",
				slog.String("client", addr.String()),
				slog.Any("error", err),
			)
		}
		return true
	})
}

func (t *Table) gc(interval time.Duration) {
	defer t.wg.Done()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-t.done:
			return
		case now := <-ticker.C:
			t.evictExpired(now)
		}
	}
}

func (t *Table) evictExpired(now time.Time) {
	cutoff := now.Add(-t.ttl).UnixNano()

	var stale []netip.AddrPort
	t.sessions.Range(func(key, val any) bool {
		s := val.(*Session)
		if s.IdleSince() <= cutoff {
			stale = append(stale, key.(netip.AddrPort))
		}
		return true
	})

	for _, addr := range stale {
		t.Delete(addr)
	}
}
