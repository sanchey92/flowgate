package tcp

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"log/slog"
	"net"
	"time"

	"github.com/sanchey92/flowgate/internal/domain/model"
	"github.com/sanchey92/flowgate/internal/proxy/proxyproto"
)

type Timeouts struct {
	Connect         time.Duration
	Idle            time.Duration
	KeepAlivePeriod time.Duration
}

type Dialer func(ctx context.Context, network, addr string) (net.Conn, error)

type Balancer interface {
	Pick() (*model.Backend, error)
	Release(backend *model.Backend)
}

type BufferPool interface {
	Get() *[]byte
	Put(*[]byte)
}

type Handler struct {
	balancer     Balancer
	pool         BufferPool
	timeouts     Timeouts
	pp           proxyproto.Mode
	ppHdrTimeout time.Duration
	log          *slog.Logger
	dial         Dialer
}

func NewHandler(
	b Balancer,
	pool BufferPool,
	t Timeouts,
	pp proxyproto.Mode,
	ppHdrTimeout time.Duration,
	log *slog.Logger,
	dial Dialer,
) *Handler {
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	if pp != proxyproto.ModeOff && ppHdrTimeout <= 0 {
		ppHdrTimeout = 3 * time.Second
	}

	return &Handler{
		balancer:     b,
		pool:         pool,
		timeouts:     t,
		pp:           pp,
		ppHdrTimeout: ppHdrTimeout,
		log:          log,
		dial:         dial,
	}
}

const proxyProtoBufSize = 1024

func (h *Handler) Handle(ctx context.Context, client net.Conn) {
	startedAt := time.Now()
	log := h.log.With(
		slog.String("request_id", newRequestID()),
		slog.String("client_addr", client.RemoteAddr().String()),
	)
	defer func() { closeWithLog(client, log, "client") }()

	clientForPipe, log, err := h.applyProxyProto(client, log)
	if err != nil {
		log.Warn("proxyproto failed, dropping connection", slog.Any("error", err))
		return
	}

	backend, err := h.balancer.Pick()
	if err != nil {
		log.Warn("no backends available, dropping connection", slog.Any("error", err))
		return
	}
	defer h.balancer.Release(backend)
	log = log.With(
		slog.String("backend_id", backend.ID),
		slog.String("backend_addr", backend.Addr),
	)

	backendConn, err := h.dialBackend(ctx, backend.Addr)
	if err != nil {
		log.Warn("backend dial failed", slog.Any("error", err))
		return
	}
	defer func() { closeWithLog(backendConn, log, "backend") }()

	h.configureKeepAlive(client, backendConn, log)

	bytesIn, bytesOut, pipeErr := PipePair(ctx, clientForPipe, backendConn, h.pool, h.timeouts.Idle)
	logPipeResult(log, time.Since(startedAt), bytesIn, bytesOut, pipeErr)
}

func (h *Handler) applyProxyProto(client net.Conn, log *slog.Logger) (net.Conn, *slog.Logger, error) {
	if h.pp == proxyproto.ModeOff {
		return client, log, nil
	}

	if err := client.SetReadDeadline(time.Now().Add(h.ppHdrTimeout)); err != nil {
		return nil, log, fmt.Errorf("proxyproto: set read deadline: %w", err)
	}

	r := bufio.NewReaderSize(client, proxyProtoBufSize)
	hdr, err := proxyproto.DetectAndStrip(r, h.pp)
	if err != nil {
		return nil, log, fmt.Errorf("proxyproto: parse: %w", err)
	}

	if err := client.SetReadDeadline(time.Time{}); err != nil {
		log.Warn("proxyproto: clear read deadline", slog.Any("error", err))
	}

	if hdr != nil && !hdr.IsLocal() {
		log = log.With(
			slog.String("real_client_addr", hdr.Src.String()),
			slog.Int("proxyproto_version", hdr.Version),
		)
	}
	return proxyproto.WrapConn(client, r, hdr), log, nil
}

func (h *Handler) dialBackend(ctx context.Context, addr string) (net.Conn, error) {
	dialCtx, cancel := context.WithTimeout(ctx, h.timeouts.Connect)
	defer cancel()
	conn, err := h.dial(dialCtx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("tcp: dial backend: %w", err)
	}
	return conn, nil
}

func (h *Handler) configureKeepAlive(client, backend net.Conn, log *slog.Logger) {
	if err := SetKeepAlive(client, h.timeouts.KeepAlivePeriod); err != nil {
		log.Debug("client set keepalive failed", slog.Any("error", err))
	}
	if err := SetKeepAlive(backend, h.timeouts.KeepAlivePeriod); err != nil {
		log.Debug("backend set keepalive failed", slog.Any("error", err))
	}
}

func logPipeResult(log *slog.Logger, duration time.Duration, bytesIn, bytesOut int64, err error) {
	fields := []any{
		slog.Int64("bytes_in", bytesIn),
		slog.Int64("bytes_out", bytesOut),
		slog.Duration("duration", duration),
	}
	if err != nil {
		log.Warn("connection closed with error", append(fields, slog.Any("error", err))...)
		return
	}
	log.Info("connection closed", fields...)
}

func closeWithLog(c net.Conn, log *slog.Logger, label string) {
	if err := c.Close(); err != nil && !IsBenignClose(err) {
		log.Debug(label+" close error", slog.Any("error", err))
	}
}

func newRequestID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("err-%d", time.Now().UnixNano())
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:])
}
