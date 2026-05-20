package middleware

import (
	"log/slog"
	"net/http"

	"github.com/sanchey92/flowgate/internal/config"
	"github.com/sanchey92/flowgate/internal/proxy/http/reqctx"
)

type headerEntry struct {
	name  string
	value *valueTemplate
}

type headerOps struct {
	remove  []string
	add     []headerEntry
	replace []headerEntry
}

type HeaderMiddleware struct {
	request  headerOps
	response headerOps
}

func NewHeaderMiddleware(rules config.HeaderRules, log *slog.Logger) *HeaderMiddleware {
	unknown := make(map[string]struct{})

	m := &HeaderMiddleware{
		request:  compileOps(rules.Request, unknown, log),
		response: compileOps(rules.Response, unknown, log),
	}

	for name := range unknown {
		log.Warn("http headers: unknown substitution variable, renders as empty string",
			slog.String("variable", name))
	}

	return m
}

func compileOps(op config.HeaderOp, unknown map[string]struct{}, log *slog.Logger) headerOps {
	return headerOps{
		remove:  canonicalNames(op.Remove, log),
		add:     compileEntries(op.Add, unknown, log),
		replace: compileEntries(op.Replace, unknown, log),
	}
}

func compileEntries(src map[string]string, unknown map[string]struct{}, log *slog.Logger) []headerEntry {
	if len(src) == 0 {
		return nil
	}
	entries := make([]headerEntry, 0, len(src))
	for name, raw := range src {
		canonical := http.CanonicalHeaderKey(name)
		if isHopByHop(canonical) {
			log.Warn("http headers: ignoring rule for hop-by-hop header",
				slog.String("header", canonical))
			continue
		}

		entries = append(entries, headerEntry{
			name:  canonical,
			value: compileTemplate(raw, unknown),
		})
	}
	return entries
}

func canonicalNames(names []string, log *slog.Logger) []string {
	if len(names) == 0 {
		return nil
	}
	out := make([]string, 0, len(names))
	for _, name := range names {
		canonical := http.CanonicalHeaderKey(name)
		if isHopByHop(canonical) {
			log.Warn("http headers: ignoring rule for hop-by-hop header",
				slog.String("header", canonical))
			continue
		}
		out = append(out, canonical)
	}
	return out
}

func (m *HeaderMiddleware) ApplyRequest(h http.Header, slot *reqctx.RequestSlot) {
	applyOps(h, m.request, slot)
}

func (m *HeaderMiddleware) ApplyResponse(h http.Header, slot *reqctx.RequestSlot) {
	applyOps(h, m.response, slot)
}

// applyOps Remove -> Add -> Replace
func applyOps(h http.Header, ops headerOps, slot *reqctx.RequestSlot) {
	for _, name := range ops.remove {
		h.Del(name)
	}
	for _, e := range ops.add {
		h.Add(e.name, e.value.render(slot))
	}
	for _, e := range ops.replace {
		h.Set(e.name, e.value.render(slot))
	}
}
