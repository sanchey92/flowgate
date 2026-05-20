package middleware

import (
	"net/http"
	"net/http/httputil"
	"strings"

	"github.com/sanchey92/flowgate/internal/config"
	"github.com/sanchey92/flowgate/internal/proxy/http/reqctx"
)

const (
	headerXForwardedFor   = "X-Forwarded-For"
	headerXForwardedProto = "X-Forwarded-Proto"
	headerXForwardedHost  = "X-Forwarded-Host"
	headerXRealIP         = "X-Real-IP"
	headerXRequestID      = "X-Request-ID"
)

type StandardHeaders struct {
	cfg config.StandardHeadersConfig
}

func NewStandardHeaders(cfg config.StandardHeadersConfig) *StandardHeaders {
	return &StandardHeaders{
		cfg: cfg.EffectiveSet(),
	}
}

func (s *StandardHeaders) ApplyRequest(pr *httputil.ProxyRequest, slot *reqctx.RequestSlot) {
	if !s.cfg.Enabled {
		return
	}
	out := pr.Out.Header

	if s.cfg.ForwardedFor && slot.ClientIP != "" {
		out.Set(headerXForwardedFor, forwardedFor(pr.In, slot.ClientIP))
	}
	if s.cfg.RealIP && slot.ClientIP != "" {
		out.Set(headerXRealIP, slot.ClientIP)
	}
	if s.cfg.ForwardedProto {
		out.Set(headerXForwardedProto, slot.Scheme)
	}
	if s.cfg.ForwardedHost {
		out.Set(headerXForwardedHost, slot.Host)
	}
	if s.cfg.RequestID {
		if slot.RequestID != "" {
			out.Set(headerXRequestID, slot.RequestID)
		}
	} else {
		out.Del(headerXRequestID)
	}
}

func forwardedFor(in *http.Request, clientIP string) string {
	prior := in.Header.Values(headerXForwardedFor)
	if len(prior) == 0 {
		return clientIP
	}
	return strings.Join(prior, ", ") + ", " + clientIP
}
