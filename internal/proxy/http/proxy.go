package proxyhttp

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"time"

	"github.com/sanchey92/flowgate/internal/config"
	domainErr "github.com/sanchey92/flowgate/internal/domain/errors"
	"github.com/sanchey92/flowgate/internal/domain/model"
	"github.com/sanchey92/flowgate/internal/proxy/http/middleware"
	"github.com/sanchey92/flowgate/internal/proxy/http/reqctx"
	"github.com/sanchey92/flowgate/internal/proxy/http/retry"
	"github.com/sanchey92/flowgate/internal/proxy/http/router"
	"github.com/sanchey92/flowgate/internal/proxy/requestid"
)

const (
	requestIDHeader  = "X-Request-ID"
	upgradeWebSocket = "websocket"
)

type Balancer interface {
	Pick() (*model.Backend, error)
	Release(*model.Backend)
}

type Proxy struct {
	router         *router.Router
	groups         map[string]Balancer
	transport      *http.Transport
	rp             *httputil.ReverseProxy
	headers        *middleware.HeaderMiddleware
	standard       *middleware.StandardHeaders
	ws             config.WebSocketConfig
	requestTimeout time.Duration
	log            *slog.Logger
}

func New(
	r *router.Router,
	groups map[string]Balancer,
	transport *http.Transport,
	pool underlyingPool,
	headerRules config.HeaderRules,
	stdHeaders config.StandardHeadersConfig,
	ws config.WebSocketConfig,
	requestTimeout time.Duration,
	policy *retry.Policy,
	budget *retry.Budget,
	log *slog.Logger,
) *Proxy {
	p := &Proxy{
		router:         r,
		groups:         groups,
		transport:      transport,
		headers:        middleware.NewHeaderMiddleware(headerRules, log),
		standard:       middleware.NewStandardHeaders(stdHeaders),
		requestTimeout: requestTimeout,
		ws:             ws,
		log:            log,
	}

	p.rp = &httputil.ReverseProxy{
		Rewrite:        p.rewrite,
		Transport:      newRetryTransport(p.transport, groups, policy, budget, log),
		BufferPool:     newHTTPBufferPool(pool),
		ErrorHandler:   p.handleError,
		ModifyResponse: p.modifyResponse,
		ErrorLog:       slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}

	return p
}

func (p *Proxy) rewrite(pr *httputil.ProxyRequest) {
	slot := reqctx.SlotFrom(pr.Out.Context())
	if slot == nil {
		return
	}

	group, ok := p.router.Route(pr.In)
	if !ok {
		slot.PickErr = domainErr.ErrNoRoute
		return
	}
	slot.Group = group

	if _, ok := p.groups[group]; !ok {
		slot.PickErr = fmt.Errorf("%w: %q", domainErr.ErrUnknownGroup, group)
		return
	}

	p.standard.ApplyRequest(pr, slot)
	p.headers.ApplyRequest(pr.Out.Header, slot)
}

func (p *Proxy) handleError(w http.ResponseWriter, r *http.Request, err error) {
	status := mapErrorToStatus(err)

	slot := reqctx.SlotFrom(r.Context())
	backendAddr := ""
	if slot != nil && slot.Backend != nil {
		backendAddr = slot.Backend.Addr
	}

	p.log.Warn("http proxy: upstream error",
		slog.String("method", r.Method),
		slog.String("host", r.Host),
		slog.String("path", r.URL.Path),
		slog.String("backend", backendAddr),
		slog.Int("status", status),
		slog.String("error", err.Error()),
	)

	w.WriteHeader(status)
}

func (p *Proxy) modifyResponse(resp *http.Response) error {
	if resp.Request == nil {
		return nil
	}
	slot := reqctx.SlotFrom(resp.Request.Context())
	if slot == nil {
		return nil
	}
	p.headers.ApplyResponse(resp.Header, slot)
	return nil
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	startedAt := time.Now()

	slot := &reqctx.RequestSlot{
		RequestID:  ensureRequestID(r),
		StartedAt:  startedAt,
		ClientAddr: r.RemoteAddr,
		ClientIP:   clientIP(r.RemoteAddr),
		Host:       r.Host,
		Method:     r.Method,
		Path:       r.URL.Path,
		Scheme:     requestScheme(r),
	}

	if isWebSocketUpgrade(r) {
		slot.Upgrade = upgradeWebSocket
	}

	ctx := reqctx.WithSlot(r.Context(), slot)

	if p.requestTimeout > 0 && slot.Upgrade != upgradeWebSocket {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, p.requestTimeout)
		defer cancel()
	}

	r = r.WithContext(ctx)

	rec := &recorder{ResponseWriter: w}

	defer func() {
		if slot.Backend != nil {
			if bal, ok := p.groups[slot.Group]; ok {
				bal.Release(slot.Backend)
			}
		}
		p.accessLog(r, slot, rec, time.Since(startedAt))
	}()

	if slot.Upgrade == upgradeWebSocket && !p.ws.Enabled {
		rec.Header().Set("Content-Type", "text/plain; charset=utf-8")
		rec.WriteHeader(http.StatusNotImplemented)
		_, _ = rec.Write([]byte("websocket upgrade is disabled\n"))
		return
	}

	p.rp.ServeHTTP(rec, r)
}

func ensureRequestID(r *http.Request) string {
	if existing := r.Header.Get(requestIDHeader); existing != "" {
		return existing
	}
	return requestid.New()
}

func (p *Proxy) accessLog(r *http.Request, slot *reqctx.RequestSlot, rec *recorder, dur time.Duration) {
	fields := []any{
		slog.String("request_id", slot.RequestID),
		slog.String("method", r.Method),
		slog.String("host", r.Host),
		slog.String("path", r.URL.Path),
		slog.Int("status", rec.status),
		slog.Duration("duration", dur),
		slog.Int64("bytes_out", rec.bytesOut),
		slog.String("group", slot.Group),
	}
	if r.ContentLength >= 0 {
		fields = append(fields, slog.Int64("bytes_in", r.ContentLength))
	}
	if slot.Backend != nil {
		fields = append(fields, slog.String("backend", slot.Backend.Addr))
	}
	if slot.Upgrade != "" {
		fields = append(fields, slog.String("upgrade", slot.Upgrade))
	}
	if slot.PickErr != nil {
		fields = append(fields, slog.String("pick_error", slot.PickErr.Error()))
	}
	if slot.Retries > 0 {
		fields = append(fields, slog.Int("retries", slot.Retries))
	}

	if rec.status >= http.StatusInternalServerError {
		p.log.Warn("http access", fields...)
		return
	}
	p.log.Info("http access", fields...)
}

func clientIP(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return host
}

func requestScheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	return "http"
}
