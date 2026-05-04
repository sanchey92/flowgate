package udp_test

import (
	"fmt"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sanchey92/flowgate/internal/domain/model"
	"github.com/sanchey92/flowgate/internal/proxy/udp"
)

func newSession(t *testing.T, addr string) (*udp.Session, netip.AddrPort) {
	t.Helper()
	conn := newUDPConn(t)
	ap := mustAddrPort(t, addr)
	s := udp.NewSession(ap, conn, &model.Backend{ID: addr, Addr: "127.0.0.1:9000"})
	return s, ap
}

func TestTable_LoadOrStore_New(t *testing.T) {
	t.Parallel()

	tbl := udp.NewTable(0, 0, discardLogger(), func(*udp.Session) {})
	t.Cleanup(tbl.Close)

	s, ap := newSession(t, "127.0.0.1:1111")

	actual, loaded := tbl.LoadOrStore(ap, s)
	assert.Same(t, s, actual)
	assert.False(t, loaded, "первая запись по ключу — loaded=false")
}

func TestTable_LoadOrStore_Existing(t *testing.T) {
	t.Parallel()

	tbl := udp.NewTable(0, 0, discardLogger(), func(*udp.Session) {})
	t.Cleanup(tbl.Close)

	s1, ap := newSession(t, "127.0.0.1:1111")
	s2, _ := newSession(t, "127.0.0.1:1111")
	t.Cleanup(func() { _ = s2.BackendConn.Close() }) // s2 проиграл гонку — закрывать должен вызывающий

	_, loaded1 := tbl.LoadOrStore(ap, s1)
	require.False(t, loaded1)

	actual, loaded2 := tbl.LoadOrStore(ap, s2)
	assert.True(t, loaded2)
	assert.Same(t, s1, actual, "должен возвращаться победитель гонки")
}

func TestTable_Get(t *testing.T) {
	t.Parallel()

	tbl := udp.NewTable(0, 0, discardLogger(), func(*udp.Session) {})
	t.Cleanup(tbl.Close)

	ap := mustAddrPort(t, "127.0.0.1:2222")
	_, ok := tbl.Get(ap)
	assert.False(t, ok)

	s, _ := newSession(t, "127.0.0.1:2222")
	tbl.LoadOrStore(ap, s)

	got, ok := tbl.Get(ap)
	require.True(t, ok)
	assert.Same(t, s, got)
}

func TestTable_Delete_CallsOnEvictAndClosesSession(t *testing.T) {
	t.Parallel()

	var (
		mu      sync.Mutex
		evicted []*udp.Session
	)
	tbl := udp.NewTable(0, 0, discardLogger(), func(s *udp.Session) {
		mu.Lock()
		defer mu.Unlock()
		evicted = append(evicted, s)
	})
	t.Cleanup(tbl.Close)

	s, ap := newSession(t, "127.0.0.1:3333")
	tbl.LoadOrStore(ap, s)

	tbl.Delete(ap)

	_, ok := tbl.Get(ap)
	assert.False(t, ok, "ключ должен исчезнуть из таблицы")

	mu.Lock()
	require.Len(t, evicted, 1)
	assert.Same(t, s, evicted[0])
	mu.Unlock()

	_, err := s.BackendConn.Write([]byte("x"))
	assert.ErrorIs(t, err, net.ErrClosed, "BackendConn должен быть закрыт после Delete")
}

func TestTable_Delete_Missing_NoCalls(t *testing.T) {
	t.Parallel()

	var calls int32
	tbl := udp.NewTable(0, 0, discardLogger(),
		func(*udp.Session) { atomic.AddInt32(&calls, 1) })
	t.Cleanup(tbl.Close)

	tbl.Delete(mustAddrPort(t, "127.0.0.1:9"))

	assert.Equal(t, int32(0), atomic.LoadInt32(&calls),
		"Delete отсутствующего ключа не должен дёргать onEvict")
}

func TestTable_Delete_OnlyOnceUnderConcurrency(t *testing.T) {
	t.Parallel()

	var calls int32
	tbl := udp.NewTable(0, 0, discardLogger(),
		func(*udp.Session) { atomic.AddInt32(&calls, 1) })
	t.Cleanup(tbl.Close)

	s, ap := newSession(t, "127.0.0.1:4444")
	tbl.LoadOrStore(ap, s)

	const N = 16
	var wg sync.WaitGroup
	wg.Add(N)
	for range N {
		go func() {
			defer wg.Done()
			tbl.Delete(ap)
		}()
	}
	wg.Wait()

	assert.Equal(t, int32(1), atomic.LoadInt32(&calls),
		"onEvict должен быть вызван ровно один раз даже при гонке Delete")
}

func TestTable_GC_EvictsExpiredSessions(t *testing.T) {
	t.Parallel()

	evicted := make(chan *udp.Session, 4)
	tbl := udp.NewTable(20*time.Millisecond, 5*time.Millisecond, discardLogger(),
		func(s *udp.Session) { evicted <- s })
	t.Cleanup(tbl.Close)

	s, ap := newSession(t, "127.0.0.1:5555")
	tbl.LoadOrStore(ap, s)

	select {
	case got := <-evicted:
		assert.Same(t, s, got)
	case <-time.After(time.Second):
		t.Fatal("протухшая сессия не была выселена GC")
	}

	_, ok := tbl.Get(ap)
	assert.False(t, ok, "после eviction ключ должен отсутствовать")
}

func TestTable_GC_KeepsTouchedSessions(t *testing.T) {
	t.Parallel()

	var evicted int32
	tbl := udp.NewTable(100*time.Millisecond, 10*time.Millisecond, discardLogger(),
		func(*udp.Session) { atomic.AddInt32(&evicted, 1) })
	t.Cleanup(tbl.Close)

	s, ap := newSession(t, "127.0.0.1:6666")
	tbl.LoadOrStore(ap, s)

	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		s.Touch()
		time.Sleep(5 * time.Millisecond)
	}

	assert.Equal(t, int32(0), atomic.LoadInt32(&evicted),
		"живая (регулярно touched) сессия не должна выселяться")
	_, ok := tbl.Get(ap)
	assert.True(t, ok)
}

func TestTable_NoGC_WhenTTLZero(t *testing.T) {
	t.Parallel()

	var evicted int32
	tbl := udp.NewTable(0, 5*time.Millisecond, discardLogger(),
		func(*udp.Session) { atomic.AddInt32(&evicted, 1) })

	s, ap := newSession(t, "127.0.0.1:7777")
	tbl.LoadOrStore(ap, s)

	time.Sleep(50 * time.Millisecond)

	assert.Equal(t, int32(0), atomic.LoadInt32(&evicted),
		"при ttl=0 GC не должен запускаться")
	_, ok := tbl.Get(ap)
	assert.True(t, ok)

	tbl.Close()
	assert.Equal(t, int32(1), atomic.LoadInt32(&evicted),
		"Close обязан выселить остаток даже если GC был выключен")
}

func TestTable_NoGC_WhenIntervalZero(t *testing.T) {
	t.Parallel()

	var evicted int32
	tbl := udp.NewTable(time.Millisecond, 0, discardLogger(),
		func(*udp.Session) { atomic.AddInt32(&evicted, 1) })

	s, ap := newSession(t, "127.0.0.1:7778")
	tbl.LoadOrStore(ap, s)

	time.Sleep(30 * time.Millisecond)

	assert.Equal(t, int32(0), atomic.LoadInt32(&evicted),
		"при gcInterval=0 GC не должен запускаться")

	tbl.Close()
	assert.Equal(t, int32(1), atomic.LoadInt32(&evicted))
}

func TestTable_Close_EvictsAllSessions(t *testing.T) {
	t.Parallel()

	var (
		mu      sync.Mutex
		evicted []*udp.Session
	)
	tbl := udp.NewTable(time.Hour, time.Hour, discardLogger(), func(s *udp.Session) {
		mu.Lock()
		defer mu.Unlock()
		evicted = append(evicted, s)
	})

	const N = 5
	sessions := make([]*udp.Session, 0, N)
	for i := range N {
		s, ap := newSession(t, fmt.Sprintf("127.0.0.1:%d", 20000+i))
		tbl.LoadOrStore(ap, s)
		sessions = append(sessions, s)
	}

	tbl.Close()

	mu.Lock()
	assert.Len(t, evicted, N, "Close должен выселить все живые сессии")
	mu.Unlock()

	for _, s := range sessions {
		_, err := s.BackendConn.Write([]byte("x"))
		assert.ErrorIs(t, err, net.ErrClosed)
	}
}

func TestTable_Close_StopsGCGoroutine(t *testing.T) {
	t.Parallel()

	tbl := udp.NewTable(time.Millisecond, time.Millisecond, discardLogger(),
		func(*udp.Session) {})

	done := make(chan struct{})
	go func() {
		tbl.Close()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Close не дождался выхода gc-горутины")
	}
}

func TestTable_Close_Idempotent(t *testing.T) {
	t.Parallel()

	tbl := udp.NewTable(time.Millisecond, time.Millisecond, discardLogger(),
		func(*udp.Session) {})

	done := make(chan struct{})
	go func() {
		tbl.Close()
		tbl.Close()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("повторный Close не должен зависать")
	}
}
