package udp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"time"

	domainErr "github.com/sanchey92/flowgate/internal/domain/errors"
	"github.com/sanchey92/flowgate/internal/domain/model"
)

const (
	readBufSize  = 64 * 1024
	replyBufSize = 8 * 1024

	gcDivisor         = 2
	defaultGCInterval = 10 * time.Second
)

type Timeouts struct {
	SessionIdle time.Duration
	BackendRead time.Duration
	Dial        time.Duration
}

type Balancer interface {
	Pick() (*model.Backend, error)
	Release(backend *model.Backend)
}

type Dialer func(ctx context.Context, network, addr string) (net.Conn, error)

type Proxy struct {
	name   string
	listen string
	log    *slog.Logger

	balancer Balancer
	timeouts Timeouts
	dial     Dialer

	mu         sync.Mutex
	listenConn *net.UDPConn
	table      *Table
	cancel     context.CancelFunc
	done       chan struct{}
	started    bool
	stopped    bool

	wg sync.WaitGroup
}

func New(
	name, listen string,
	b Balancer,
	t Timeouts,
	log *slog.Logger,
	dial Dialer,
) *Proxy {
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	return &Proxy{
		name:     name,
		listen:   listen,
		log:      log,
		balancer: b,
		timeouts: t,
		dial:     dial,
	}
}

func (p *Proxy) Addr() net.Addr {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.listenConn == nil {
		return nil
	}
	return p.listenConn.LocalAddr()
}

func (p *Proxy) Start(ctx context.Context) error {
	if p.timeouts.SessionIdle <= 0 {
		return fmt.Errorf("udp proxy %q: invalid SessionIdle %s: must be > 0",
			p.name, p.timeouts.SessionIdle)
	}

	p.mu.Lock()

	if p.stopped {
		p.mu.Unlock()
		return domainErr.ErrProxyStopped
	}
	if p.started {
		p.mu.Unlock()
		return domainErr.ErrProxyStarted
	}

	addr, err := net.ResolveUDPAddr("udp", p.listen)
	if err != nil {
		p.mu.Unlock()
		return fmt.Errorf("udp proxy %q: resolve %s: %w", p.name, p.listen, err)
	}

	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		p.mu.Unlock()
		return fmt.Errorf("udp proxy %q: listen %s: %w", p.name, p.listen, err)
	}

	loopCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	gcInterval := p.timeouts.SessionIdle / gcDivisor
	if gcInterval <= 0 {
		gcInterval = defaultGCInterval
	}

	p.listenConn = conn
	p.cancel = cancel
	p.done = done
	p.table = NewTable(p.timeouts.SessionIdle, gcInterval, p.log,
		func(s *Session) { p.balancer.Release(s.Backend) },
	)
	p.started = true
	p.wg.Add(1)
	p.mu.Unlock()

	//nolint:contextcheck // loop lifetime is managed by p.cancel, intentionally detached from parent ctx
	go p.readLoop(loopCtx, conn)
	go p.complete(done)
	go p.watchParentCtx(ctx, done)

	p.log.Info("udp proxy started", slog.String("addr", conn.LocalAddr().String()))
	return nil
}

//nolint:contextcheck // ctx may be nil; falling back to Background is intentional for graceful shutdown
func (p *Proxy) Shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}

	done := p.stop()
	if done == nil {
		return nil
	}

	select {
	case <-done:
		p.log.Info("udp proxy shutdown complete")
		return nil
	case <-ctx.Done():
		p.log.Warn("udp proxy shutdown timed out, sessions still alive",
			slog.Any("ctx_err", ctx.Err()))
		return fmt.Errorf("udp proxy %q: shutdown: %w", p.name, ctx.Err())
	}
}

func (p *Proxy) readLoop(ctx context.Context, conn *net.UDPConn) {
	defer p.wg.Done()

	var buf [readBufSize]byte

	for {
		if ctx.Err() != nil {
			return
		}

		n, clientAddr, err := conn.ReadFromUDPAddrPort(buf[:])
		if err != nil {
			if errors.Is(err, net.ErrClosed) || ctx.Err() != nil {
				p.log.Info("udp listener closed, read loop exiting")
				return
			}
			p.log.Debug("udp read error", slog.Any("error", err))
			continue
		}

		// Передаём buf[:n] синхронно. Не делаем go p.forward(...) — иначе
		// другая итерация перезапишет buf до того, как worker отправит
		if err := p.forward(ctx, conn, clientAddr, buf[:n]); err != nil {
			p.log.Warn("udp forward",
				slog.String("client", clientAddr.String()),
				slog.Any("error", err),
			)
		}
	}
}

func (p *Proxy) forward(ctx context.Context, conn *net.UDPConn, client netip.AddrPort, data []byte) error {
	sess, created, err := p.getOrCreateSession(ctx, client)
	if err != nil {
		return err
	}

	if created {
		p.wg.Add(1)

		go p.replyLoop(ctx, conn, sess)
	}

	sess.Touch()

	if _, err := sess.BackendConn.Write(data); err != nil {
		if errors.Is(err, net.ErrClosed) {
			return nil
		}
		p.table.Delete(client)
		return fmt.Errorf("udp: write backend: %w", err)
	}
	return nil
}

func (p *Proxy) getOrCreateSession(ctx context.Context, client netip.AddrPort) (*Session, bool, error) {
	if s, ok := p.table.Get(client); ok {
		return s, false, nil
	}

	backend, err := p.balancer.Pick()
	if err != nil {
		return nil, false, fmt.Errorf("udp: pick: %w", err)
	}

	dialCtx, cancel := context.WithTimeout(ctx, p.timeouts.Dial)
	netConn, err := p.dial(dialCtx, "udp", backend.Addr)
	cancel()
	if err != nil {
		p.balancer.Release(backend)
		return nil, false, fmt.Errorf("udp: dial backend %s: %w", backend.Addr, err)
	}

	udpConn, ok := netConn.(*net.UDPConn)
	if !ok {
		_ = netConn.Close()
		p.balancer.Release(backend)
		return nil, false, fmt.Errorf("udp: dial returned %T, want *net.UDPConn", netConn)
	}

	candidate := NewSession(client, udpConn, backend)
	actual, loaded := p.table.LoadOrStore(client, candidate)
	if loaded {
		p.balancer.Release(backend)
		_ = udpConn.Close()
		return actual, false, nil
	}
	return candidate, true, nil
}

func (p *Proxy) replyLoop(ctx context.Context, conn *net.UDPConn, sess *Session) {
	defer p.wg.Done()
	defer p.table.Delete(sess.ClientAddr)

	var buf [replyBufSize]byte

	for {
		if ctx.Err() != nil {
			return
		}
		if p.timeouts.BackendRead > 0 {
			_ = sess.BackendConn.SetReadDeadline(time.Now().Add(p.timeouts.BackendRead))
		}

		n, err := sess.BackendConn.Read(buf[:])
		if err != nil {
			if errors.Is(err, net.ErrClosed) || ctx.Err() != nil {
				return
			}
			p.log.Debug("udp backend read",
				slog.String("backend", sess.Backend.Addr),
				slog.String("client", sess.ClientAddr.String()),
				slog.Any("error", err),
			)
			return
		}

		sess.Touch()

		if _, err := conn.WriteToUDPAddrPort(buf[:n], sess.ClientAddr); err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			p.log.Debug("udp listener write",
				slog.String("client", sess.ClientAddr.String()),
				slog.Any("error", err),
			)
		}
	}
}

func (p *Proxy) stop() <-chan struct{} {
	var (
		cancel context.CancelFunc
		listen *net.UDPConn
		table  *Table
		done   chan struct{}
	)

	p.mu.Lock()
	if !p.started {
		p.mu.Unlock()
		return nil
	}
	done = p.done
	if !p.stopped {
		p.stopped = true
		cancel = p.cancel
		listen = p.listenConn
		table = p.table
		p.listenConn = nil
	}

	p.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if listen != nil {
		if err := listen.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			p.log.Warn("udp listener close error", slog.Any("error", err))
		}
	}

	if table != nil {
		table.Close()
	}
	return done
}

func (p *Proxy) complete(done chan struct{}) {
	p.wg.Wait()
	p.mu.Lock()
	if p.done == done {
		p.started = false
		p.listenConn = nil
		p.table = nil
		p.cancel = nil
	}
	p.mu.Unlock()
	close(done)
}

func (p *Proxy) watchParentCtx(ctx context.Context, done <-chan struct{}) {
	select {
	case <-ctx.Done():
		p.stop()
	case <-done:
	}
}
