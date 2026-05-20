package middleware

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sanchey92/flowgate/internal/config"
	"github.com/sanchey92/flowgate/internal/domain/model"
	"github.com/sanchey92/flowgate/internal/proxy/http/reqctx"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func bufferedLogger() (*slog.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})), buf
}

// fullSlot — слот с заполненными полями, чтобы шаблоны рендерились корректно.
func fullSlot() *reqctx.RequestSlot {
	return &reqctx.RequestSlot{
		RequestID:  "rid-42",
		Backend:    model.NewBackend("10.0.0.1:8080", 1, 0),
		Group:      "g1",
		StartedAt:  time.Unix(1700000000, 0),
		ClientIP:   "203.0.113.7",
		ClientAddr: "203.0.113.7:55555",
		Host:       "example.com",
		Method:     http.MethodGet,
		Path:       "/api/v1/widgets",
		Scheme:     "https",
	}
}

// --- ApplyRequest ----------------------------------------------------------

func TestHeaderMiddleware_ApplyRequest(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		rules    config.HeaderOp
		initial  http.Header
		expected http.Header
	}{
		{
			name:     "empty rules are a no-op",
			rules:    config.HeaderOp{},
			initial:  http.Header{"X-Keep": {"yes"}},
			expected: http.Header{"X-Keep": {"yes"}},
		},
		{
			name: "add inserts a header",
			rules: config.HeaderOp{
				Add: map[string]string{"X-Trace": "abc"},
			},
			initial:  http.Header{},
			expected: http.Header{"X-Trace": {"abc"}},
		},
		{
			name: "add appends to existing values",
			rules: config.HeaderOp{
				Add: map[string]string{"X-Multi": "second"},
			},
			initial:  http.Header{"X-Multi": {"first"}},
			expected: http.Header{"X-Multi": {"first", "second"}},
		},
		{
			name: "add canonicalises the header name",
			rules: config.HeaderOp{
				Add: map[string]string{"x-custom-key": "value"},
			},
			initial:  http.Header{},
			expected: http.Header{"X-Custom-Key": {"value"}},
		},
		{
			name: "remove deletes a header",
			rules: config.HeaderOp{
				Remove: []string{"X-Drop"},
			},
			initial:  http.Header{"X-Drop": {"gone"}, "X-Keep": {"yes"}},
			expected: http.Header{"X-Keep": {"yes"}},
		},
		{
			name: "remove is case-insensitive",
			rules: config.HeaderOp{
				Remove: []string{"x-drop"},
			},
			initial:  http.Header{"X-Drop": {"gone"}},
			expected: http.Header{},
		},
		{
			name: "replace overwrites all existing values",
			rules: config.HeaderOp{
				Replace: map[string]string{"X-Auth": "new"},
			},
			initial:  http.Header{"X-Auth": {"old1", "old2"}},
			expected: http.Header{"X-Auth": {"new"}},
		},
		{
			name: "order is remove then add — same key ends up only with the added value",
			rules: config.HeaderOp{
				Add:    map[string]string{"X-Tag": "fresh"},
				Remove: []string{"X-Tag"},
			},
			initial:  http.Header{"X-Tag": {"stale"}},
			expected: http.Header{"X-Tag": {"fresh"}},
		},
		{
			name: "order is add then replace — replace wins over add",
			rules: config.HeaderOp{
				Add:     map[string]string{"X-Mode": "added"},
				Replace: map[string]string{"X-Mode": "replaced"},
			},
			initial:  http.Header{},
			expected: http.Header{"X-Mode": {"replaced"}},
		},
		{
			name: "hop-by-hop headers are ignored in add",
			rules: config.HeaderOp{
				Add: map[string]string{"Connection": "close"},
			},
			initial:  http.Header{},
			expected: http.Header{},
		},
		{
			name: "hop-by-hop headers are ignored in remove",
			rules: config.HeaderOp{
				Remove: []string{"Keep-Alive"},
			},
			initial:  http.Header{"Keep-Alive": {"timeout=5"}},
			expected: http.Header{"Keep-Alive": {"timeout=5"}},
		},
		{
			name: "hop-by-hop headers are ignored in replace",
			rules: config.HeaderOp{
				Replace: map[string]string{"Transfer-Encoding": "gzip"},
			},
			initial:  http.Header{"Transfer-Encoding": {"chunked"}},
			expected: http.Header{"Transfer-Encoding": {"chunked"}},
		},
		{
			name: "template variable is rendered from slot",
			rules: config.HeaderOp{
				Add: map[string]string{"X-Request-Trace": "${request_id}"},
			},
			initial:  http.Header{},
			expected: http.Header{"X-Request-Trace": {"rid-42"}},
		},
		{
			name: "template mixes literals and variables",
			rules: config.HeaderOp{
				Add: map[string]string{"X-Trace": "req-${request_id}-end"},
			},
			initial:  http.Header{},
			expected: http.Header{"X-Trace": {"req-rid-42-end"}},
		},
		{
			name: "template renders multiple variables",
			rules: config.HeaderOp{
				Add: map[string]string{"X-Route": "${method} ${path}"},
			},
			initial:  http.Header{},
			expected: http.Header{"X-Route": {"GET /api/v1/widgets"}},
		},
		{
			name: "unknown template variable renders as empty",
			rules: config.HeaderOp{
				Add: map[string]string{"X-Bogus": "pre-${nope}-post"},
			},
			initial:  http.Header{},
			expected: http.Header{"X-Bogus": {"pre--post"}},
		},
		{
			name: "purely static value bypasses substitution",
			rules: config.HeaderOp{
				Add: map[string]string{"X-Static": "literal value"},
			},
			initial:  http.Header{},
			expected: http.Header{"X-Static": {"literal value"}},
		},
		{
			name: "backend_addr renders from slot.Backend",
			rules: config.HeaderOp{
				Replace: map[string]string{"X-Backend": "${backend_addr}"},
			},
			initial:  http.Header{},
			expected: http.Header{"X-Backend": {"10.0.0.1:8080"}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := NewHeaderMiddleware(config.HeaderRules{Request: tc.rules}, discardLogger())

			h := tc.initial.Clone()
			if h == nil {
				h = http.Header{}
			}
			m.ApplyRequest(h, fullSlot())

			assert.Equal(t, tc.expected, h)
		})
	}
}

// --- ApplyResponse ---------------------------------------------------------

func TestHeaderMiddleware_ApplyResponse(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		rules    config.HeaderOp
		initial  http.Header
		expected http.Header
	}{
		{
			name: "response add inserts a header",
			rules: config.HeaderOp{
				Add: map[string]string{"X-Frame-Options": "DENY"},
			},
			initial:  http.Header{},
			expected: http.Header{"X-Frame-Options": {"DENY"}},
		},
		{
			name: "response remove deletes a header",
			rules: config.HeaderOp{
				Remove: []string{"Server"},
			},
			initial:  http.Header{"Server": {"nginx"}, "X-Keep": {"yes"}},
			expected: http.Header{"X-Keep": {"yes"}},
		},
		{
			name: "response replace overwrites values",
			rules: config.HeaderOp{
				Replace: map[string]string{"Cache-Control": "no-store"},
			},
			initial:  http.Header{"Cache-Control": {"public, max-age=60"}},
			expected: http.Header{"Cache-Control": {"no-store"}},
		},
		{
			// Имя пишется как "X-Echo-Request-Id" (а не "...-ID"): http.CanonicalHeaderKey
			// делает в каждом слове только первую букву заглавной — стандартное поведение Go.
			name: "response template uses request slot",
			rules: config.HeaderOp{
				Add: map[string]string{"X-Echo-Request-Id": "${request_id}"},
			},
			initial:  http.Header{},
			expected: http.Header{"X-Echo-Request-Id": {"rid-42"}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := NewHeaderMiddleware(config.HeaderRules{Response: tc.rules}, discardLogger())

			h := tc.initial.Clone()
			if h == nil {
				h = http.Header{}
			}
			m.ApplyResponse(h, fullSlot())

			assert.Equal(t, tc.expected, h)
		})
	}
}

// --- Side channels ---------------------------------------------------------

// Request- и response-правила должны жить независимо: правило, навешенное
// на одну сторону, не должно протекать на другую.
func TestHeaderMiddleware_RequestAndResponseAreIndependent(t *testing.T) {
	t.Parallel()
	m := NewHeaderMiddleware(config.HeaderRules{
		Request:  config.HeaderOp{Add: map[string]string{"X-Req-Only": "1"}},
		Response: config.HeaderOp{Add: map[string]string{"X-Resp-Only": "1"}},
	}, discardLogger())

	req := http.Header{}
	resp := http.Header{}

	m.ApplyRequest(req, fullSlot())
	m.ApplyResponse(resp, fullSlot())

	assert.Equal(t, http.Header{"X-Req-Only": {"1"}}, req)
	assert.Equal(t, http.Header{"X-Resp-Only": {"1"}}, resp)
}

func TestHeaderMiddleware_WarnsAboutUnknownVariable(t *testing.T) {
	t.Parallel()
	log, buf := bufferedLogger()
	_ = NewHeaderMiddleware(config.HeaderRules{
		Request: config.HeaderOp{Add: map[string]string{"X": "${nope}-${request_id}"}},
	}, log)

	assert.Contains(t, buf.String(), "unknown substitution variable")
	assert.Contains(t, buf.String(), "nope")
}

func TestHeaderMiddleware_WarnsAboutHopByHopRule(t *testing.T) {
	t.Parallel()
	log, buf := bufferedLogger()
	_ = NewHeaderMiddleware(config.HeaderRules{
		Request: config.HeaderOp{
			Add:    map[string]string{"Connection": "close"},
			Remove: []string{"Keep-Alive"},
		},
	}, log)

	logged := buf.String()
	assert.Contains(t, logged, "ignoring rule for hop-by-hop header")
	assert.Contains(t, logged, "Connection")
	assert.Contains(t, logged, "Keep-Alive")
}

// --- isHopByHop ------------------------------------------------------------

func TestIsHopByHop(t *testing.T) {
	t.Parallel()

	cases := []struct {
		header string
		want   bool
	}{
		{"Connection", true},
		{"connection", true},
		{"Proxy-Connection", true},
		{"Keep-Alive", true},
		{"Proxy-Authenticate", true},
		{"Proxy-Authorization", true},
		{"TE", true},
		{"te", true},
		{"Trailer", true},
		{"Transfer-Encoding", true},
		{"transfer-encoding", true},
		{"Upgrade", true},
		{"Content-Type", false},
		{"X-Forwarded-For", false},
		{"Trailers", false}, // именно Trailer, а не Trailers — см. комментарий в hopheaders.go
		{"", false},
	}

	for _, tc := range cases {
		t.Run(tc.header, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, isHopByHop(tc.header))
		})
	}
}

// --- Misc edge cases -------------------------------------------------------

// nil slot не должен ронять рендер шаблонов: переменные просто отдадут пустую
// строку, литералы останутся. Это сценарий, когда middleware вызвали в
// контексте без RequestSlot — теоретически невозможно, но не должен паниковать.
func TestHeaderMiddleware_NilSlotDoesNotPanic(t *testing.T) {
	t.Parallel()
	m := NewHeaderMiddleware(config.HeaderRules{
		Request: config.HeaderOp{Add: map[string]string{"X": "literal-only"}},
	}, discardLogger())

	require.NotPanics(t, func() {
		m.ApplyRequest(http.Header{}, &reqctx.RequestSlot{})
	})
}
