package middleware

import (
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/sanchey92/flowgate/internal/config"
	"github.com/sanchey92/flowgate/internal/proxy/http/reqctx"
)

// --- EffectiveSet ----------------------------------------------------------

func TestStandardHeadersConfig_EffectiveSet(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   config.StandardHeadersConfig
		want config.StandardHeadersConfig
	}{
		{
			name: "disabled is returned as-is",
			in:   config.StandardHeadersConfig{},
			want: config.StandardHeadersConfig{},
		},
		{
			name: "disabled keeps individual flags untouched",
			in:   config.StandardHeadersConfig{Enabled: false, ForwardedFor: true, RequestID: true},
			want: config.StandardHeadersConfig{Enabled: false, ForwardedFor: true, RequestID: true},
		},
		{
			name: "enabled with no flags turns everything on",
			in:   config.StandardHeadersConfig{Enabled: true},
			want: config.StandardHeadersConfig{
				Enabled:        true,
				ForwardedFor:   true,
				RealIP:         true,
				ForwardedProto: true,
				ForwardedHost:  true,
				RequestID:      true,
			},
		},
		{
			name: "enabled with any explicit flag keeps the rest off",
			in:   config.StandardHeadersConfig{Enabled: true, ForwardedFor: true},
			want: config.StandardHeadersConfig{Enabled: true, ForwardedFor: true},
		},
		{
			name: "enabled with only RequestID keeps the rest off",
			in:   config.StandardHeadersConfig{Enabled: true, RequestID: true},
			want: config.StandardHeadersConfig{Enabled: true, RequestID: true},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.in.EffectiveSet())
		})
	}
}

// --- ApplyRequest ----------------------------------------------------------

// newPR собирает httputil.ProxyRequest для middleware: In имитирует входящий
// запрос (с возможными предустановленными заголовками), Out — outgoing-копию.
func newPR(inHeaders http.Header) *httputil.ProxyRequest {
	in := httptest.NewRequest(http.MethodGet, "http://example.com/p", nil)
	for k, vs := range inHeaders {
		for _, v := range vs {
			in.Header.Add(k, v)
		}
	}
	out := in.Clone(in.Context())
	return &httputil.ProxyRequest{In: in, Out: out}
}

func TestStandardHeaders_ApplyRequest(t *testing.T) {
	t.Parallel()

	baseSlot := func() *reqctx.RequestSlot {
		return &reqctx.RequestSlot{
			RequestID: "rid-1",
			ClientIP:  "203.0.113.7",
			Host:      "example.com",
			Scheme:    "https",
		}
	}

	cases := []struct {
		name      string
		cfg       config.StandardHeadersConfig
		slotPatch func(*reqctx.RequestSlot)
		inHeaders http.Header
		// expectGot: каждое значение проверяется через Header.Values.
		expectGot map[string][]string
		// expectAbsent: заголовков с этими именами быть не должно.
		expectAbsent []string
	}{
		{
			name:         "disabled writes nothing",
			cfg:          config.StandardHeadersConfig{Enabled: false, ForwardedFor: true, RequestID: true},
			expectAbsent: []string{"X-Forwarded-For", "X-Real-Ip", "X-Forwarded-Proto", "X-Forwarded-Host", "X-Request-Id"},
		},
		{
			name: "ForwardedFor inserts client IP when none present",
			cfg:  config.StandardHeadersConfig{Enabled: true, ForwardedFor: true},
			expectGot: map[string][]string{
				"X-Forwarded-For": {"203.0.113.7"},
			},
			expectAbsent: []string{"X-Real-Ip", "X-Forwarded-Proto", "X-Forwarded-Host"},
		},
		{
			name: "ForwardedFor appends to a single existing value",
			cfg:  config.StandardHeadersConfig{Enabled: true, ForwardedFor: true},
			inHeaders: http.Header{
				"X-Forwarded-For": {"10.0.0.1"},
			},
			expectGot: map[string][]string{
				"X-Forwarded-For": {"10.0.0.1, 203.0.113.7"},
			},
		},
		{
			name: "ForwardedFor joins multiple existing values",
			cfg:  config.StandardHeadersConfig{Enabled: true, ForwardedFor: true},
			inHeaders: http.Header{
				"X-Forwarded-For": {"10.0.0.1", "10.0.0.2"},
			},
			expectGot: map[string][]string{
				"X-Forwarded-For": {"10.0.0.1, 10.0.0.2, 203.0.113.7"},
			},
		},
		{
			name: "ForwardedFor with empty ClientIP is skipped",
			cfg:  config.StandardHeadersConfig{Enabled: true, ForwardedFor: true},
			slotPatch: func(s *reqctx.RequestSlot) {
				s.ClientIP = ""
			},
			expectAbsent: []string{"X-Forwarded-For"},
		},
		{
			name: "RealIP sets X-Real-IP from slot.ClientIP",
			cfg:  config.StandardHeadersConfig{Enabled: true, RealIP: true},
			expectGot: map[string][]string{
				"X-Real-Ip": {"203.0.113.7"},
			},
		},
		{
			name: "RealIP with empty ClientIP is skipped",
			cfg:  config.StandardHeadersConfig{Enabled: true, RealIP: true},
			slotPatch: func(s *reqctx.RequestSlot) {
				s.ClientIP = ""
			},
			expectAbsent: []string{"X-Real-Ip"},
		},
		{
			name: "ForwardedProto sets the scheme",
			cfg:  config.StandardHeadersConfig{Enabled: true, ForwardedProto: true},
			expectGot: map[string][]string{
				"X-Forwarded-Proto": {"https"},
			},
		},
		{
			name: "ForwardedHost sets the host",
			cfg:  config.StandardHeadersConfig{Enabled: true, ForwardedHost: true},
			expectGot: map[string][]string{
				"X-Forwarded-Host": {"example.com"},
			},
		},
		{
			name: "RequestID writes the slot's ID",
			cfg:  config.StandardHeadersConfig{Enabled: true, RequestID: true},
			expectGot: map[string][]string{
				"X-Request-Id": {"rid-1"},
			},
		},
		{
			name: "RequestID overwrites incoming X-Request-ID with slot's value",
			cfg:  config.StandardHeadersConfig{Enabled: true, RequestID: true},
			inHeaders: http.Header{
				"X-Request-Id": {"client-supplied-ignored"},
			},
			expectGot: map[string][]string{
				"X-Request-Id": {"rid-1"},
			},
		},
		{
			name: "RequestID with empty slot ID leaves header untouched",
			cfg:  config.StandardHeadersConfig{Enabled: true, RequestID: true},
			slotPatch: func(s *reqctx.RequestSlot) {
				s.RequestID = ""
			},
			inHeaders: http.Header{
				"X-Request-Id": {"keep-me"},
			},
			expectGot: map[string][]string{
				"X-Request-Id": {"keep-me"},
			},
		},
		{
			name: "RequestID disabled removes incoming X-Request-ID",
			cfg:  config.StandardHeadersConfig{Enabled: true, ForwardedFor: true, RequestID: false},
			inHeaders: http.Header{
				"X-Request-Id": {"leak-me"},
			},
			expectAbsent: []string{"X-Request-Id"},
		},
		{
			name: "all flags on writes the full set",
			cfg: config.StandardHeadersConfig{
				Enabled:        true,
				ForwardedFor:   true,
				RealIP:         true,
				ForwardedProto: true,
				ForwardedHost:  true,
				RequestID:      true,
			},
			expectGot: map[string][]string{
				"X-Forwarded-For":   {"203.0.113.7"},
				"X-Real-Ip":         {"203.0.113.7"},
				"X-Forwarded-Proto": {"https"},
				"X-Forwarded-Host":  {"example.com"},
				"X-Request-Id":      {"rid-1"},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			slot := baseSlot()
			if tc.slotPatch != nil {
				tc.slotPatch(slot)
			}

			pr := newPR(tc.inHeaders)
			NewStandardHeaders(tc.cfg).ApplyRequest(pr, slot)

			for name, want := range tc.expectGot {
				assert.Equal(t, want, pr.Out.Header.Values(name), "header %q", name)
			}
			for _, name := range tc.expectAbsent {
				assert.Empty(t, pr.Out.Header.Values(name), "header %q should be absent", name)
			}
		})
	}
}

// EffectiveSet применяется при конструировании: даже если в конфиге Enabled=true
// без явных флагов, конструктор раскрывает его до полного набора.
func TestNewStandardHeaders_AppliesEffectiveSet(t *testing.T) {
	t.Parallel()

	slot := &reqctx.RequestSlot{
		RequestID: "rid-x",
		ClientIP:  "198.51.100.1",
		Host:      "h.example",
		Scheme:    "http",
	}

	pr := newPR(nil)
	NewStandardHeaders(config.StandardHeadersConfig{Enabled: true}).ApplyRequest(pr, slot)

	assert.Equal(t, "198.51.100.1", pr.Out.Header.Get("X-Forwarded-For"))
	assert.Equal(t, "198.51.100.1", pr.Out.Header.Get("X-Real-Ip"))
	assert.Equal(t, "http", pr.Out.Header.Get("X-Forwarded-Proto"))
	assert.Equal(t, "h.example", pr.Out.Header.Get("X-Forwarded-Host"))
	assert.Equal(t, "rid-x", pr.Out.Header.Get("X-Request-Id"))
}
