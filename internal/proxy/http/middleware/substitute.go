package middleware

import (
	"strconv"
	"strings"

	"github.com/sanchey92/flowgate/internal/proxy/http/reqctx"
)

type varResolver func(*reqctx.RequestSlot) string

var varResolvers = map[string]varResolver{
	"client_id":   func(s *reqctx.RequestSlot) string { return s.ClientIP },
	"client_addr": func(s *reqctx.RequestSlot) string { return s.ClientAddr },
	"request_id":  func(s *reqctx.RequestSlot) string { return s.RequestID },
	"backend_addr": func(s *reqctx.RequestSlot) string {
		if s.Backend == nil {
			return ""
		}
		return s.Backend.Addr
	},
	"timestamp_ms": func(s *reqctx.RequestSlot) string {
		return strconv.FormatInt(s.StartedAt.UnixMilli(), 10)
	},
	"host":   func(s *reqctx.RequestSlot) string { return s.Host },
	"method": func(s *reqctx.RequestSlot) string { return s.Method },
	"path":   func(s *reqctx.RequestSlot) string { return s.Path },
	"scheme": func(s *reqctx.RequestSlot) string { return s.Scheme },
}

type templateSegment struct {
	lit     string
	resolve varResolver
}

type valueTemplate struct {
	static   string
	isStatic bool
	segments []templateSegment
}

func compileTemplate(raw string, unknown map[string]struct{}) *valueTemplate {
	if !strings.Contains(raw, "${") {
		return &valueTemplate{static: raw, isStatic: true}
	}

	segments := parseSegments(raw, unknown)

	if literal, ok := collapseToLiteral(segments); ok {
		return &valueTemplate{static: literal, isStatic: true}
	}
	return &valueTemplate{segments: segments}
}

func parseSegments(raw string, unknown map[string]struct{}) []templateSegment {
	var segments []templateSegment

	for len(raw) > 0 {
		start := strings.Index(raw, "${")
		if start < 0 {
			segments = append(segments, templateSegment{lit: raw})
			break
		}
		if start > 0 {
			segments = append(segments, templateSegment{lit: raw[:start]})
		}

		rest := raw[start+2:]
		end := strings.IndexByte(rest, '}')
		if end < 0 {
			segments = append(segments, templateSegment{lit: raw[start:]})
			break
		}

		name := rest[:end]
		if resolve, ok := varResolvers[name]; ok {
			segments = append(segments, templateSegment{resolve: resolve})
		} else {
			unknown[name] = struct{}{}
		}
		raw = rest[end+1:]
	}
	return segments
}

func collapseToLiteral(segments []templateSegment) (string, bool) {
	for i := range segments {
		if segments[i].resolve != nil {
			return "", false
		}
	}
	var b strings.Builder
	for i := range segments {
		b.WriteString(segments[i].lit)
	}
	return b.String(), true
}

func (t *valueTemplate) render(slot *reqctx.RequestSlot) string {
	if t.isStatic {
		return t.static
	}
	var b strings.Builder
	for i := range t.segments {
		seg := &t.segments[i]
		if seg.resolve != nil {
			b.WriteString(seg.resolve(slot))
			continue
		}
		b.WriteString(seg.lit)
	}
	return b.String()
}
