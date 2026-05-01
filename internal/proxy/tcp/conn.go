package tcp

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"log/slog"
	"net"
	"time"

	"github.com/sanchey92/flowgate/internal/domain/model"
)

type Timeouts struct {
	Connect         time.Duration
	Idle            time.Duration
	KeepAlivePeriod time.Duration
}

type ProxyProtoMode int

const (
	ProxyProtoOff ProxyProtoMode = iota
	ProxyProtoV1
	ProxyProtoV2
	ProxyProtoAuto
)

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
	balancer Balancer
	pool     BufferPool
	timeouts *Timeouts
	pp       ProxyProtoMode
	log      *slog.Logger
	dial     Dialer
}

func NewHandler(
	b Balancer,
	pool BufferPool,
	t *Timeouts,
	pp ProxyProtoMode,
	log *slog.Logger,
	dial Dialer,
) *Handler {
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}

	if pp != ProxyProtoOff {
		log.Warn("proxy protocol mode is not implemented in stage C, ignoring",
			slog.Int("mode", int(pp)))
	}

	return &Handler{
		balancer: b,
		pool:     pool,
		timeouts: t,
		pp:       pp,
		log:      log,
		dial:     dial,
	}
}

func (h *Handler) Handle(ctx context.Context, client net.Conn) {
	reqID := newRequestID()
	startedAt := time.Now()

	log := h.log.With(
		slog.String("request_id", reqID),
		slog.String("client_addr", client.RemoteAddr().String()),
	)

	defer func() {
		if err := client.Close(); err != nil && !IsBenignClose(err) {
			log.Debug("client close error", slog.Any("error", err))
		}
	}()

	backend, err := h.balancer.Pick()
	if err != nil {
		log.Warn("no backends available, dropping connection", slog.Any("error", err))
		return
	}
	defer h.balancer.Release(backend)

	log = log.With(slog.String("backend_id", backend.ID),
		slog.String("backend_addr", backend.Addr))

	dialCtx, cancelDial := context.WithTimeout(ctx, h.timeouts.Connect)
	backendConn, err := h.dial(dialCtx, "tcp", backend.Addr)
	cancelDial()
	if err != nil {
		log.Warn("backend dial failed", slog.Any("error", err))
		return
	}

	defer func() {
		if err := backendConn.Close(); err != nil && !IsBenignClose(err) {
			log.Debug("backend close error", slog.Any("error", err))
		}
	}()

	if err := SetKeepAlive(client, h.timeouts.KeepAlivePeriod); err != nil {
		log.Debug("client set keepalive failed", slog.Any("error", err))
	}
	if err := SetKeepAlive(backendConn, h.timeouts.KeepAlivePeriod); err != nil {
		log.Debug("backend set keepalive failed", slog.Any("error", err))
	}

	bytesIn, bytesOut, pipeErr := PipePair(ctx, client, backendConn, h.pool, h.timeouts.Idle)

	logFields := []any{
		slog.Int64("bytes_in", bytesIn),
		slog.Int64("bytes_out", bytesOut),
		slog.Duration("duration", time.Since(startedAt)),
	}
	if pipeErr != nil {
		log.Warn("connection closed with error",
			append(logFields, slog.Any("error", pipeErr))...)
		return
	}
	log.Info("connection closed", logFields...)
}

func newRequestID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("err-%d", time.Now().UnixNano())
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:])
}
