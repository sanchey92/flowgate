package router

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
)

func newRequest(t *testing.T, target string) *http.Request {
	t.Helper()
	return httptest.NewRequest(http.MethodGet, target, nil)
}

func TestHostMatcher(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		expected string
		reqHost  string
		want     bool
	}{
		{"exact match", "example.com", "example.com", true},
		{"case-insensitive", "Example.COM", "example.com", true},
		{"request has port", "example.com", "example.com:8080", true},
		{"config has port", "example.com:8080", "example.com", true},
		{"both have port, host differs", "example.com:80", "other.com:80", false},
		{"different host", "example.com", "other.com", false},
		{"ipv6 with port", "::1", "[::1]:8080", true},
		{"empty request host", "example.com", "", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := newHostMatcher(tc.expected)
			r := newRequest(t, "http://placeholder/")
			r.Host = tc.reqHost
			assert.Equal(t, tc.want, m.Match(r))
		})
	}
}

func TestPathExactMatcher(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		expected string
		path     string
		want     bool
	}{
		{"exact match", "/api", "/api", true},
		{"trailing slash differs", "/api", "/api/", false},
		{"longer path", "/api", "/api/users", false},
		{"different path", "/api", "/v1/api", false},
		{"root", "/", "/", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := newPathExactMatcher(tc.expected)
			r := newRequest(t, "http://example.com"+tc.path)
			assert.Equal(t, tc.want, m.Match(r))
		})
	}
}

func TestPathPrefixMatcher(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		prefix string
		path   string
		want   bool
	}{
		{"exact match", "/api", "/api", true},
		{"with subpath", "/api", "/api/users", true},
		{"with deep subpath", "/api", "/api/v1/users/42", true},
		{"boundary not at segment", "/api", "/apinotreal", false},
		{"boundary not at segment, longer", "/api", "/apinotreal/x", false},
		{"different path", "/api", "/v1", false},
		{"prefix with trailing slash, exact", "/api/", "/api/", true},
		{"prefix with trailing slash, subpath", "/api/", "/api/users", true},
		{"prefix with trailing slash, no slash in path", "/api/", "/api", false},
		{"root prefix matches all", "/", "/anything", true},
		{"root prefix matches root", "/", "/", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := newPathPrefixMatcher(tc.prefix)
			r := newRequest(t, "http://example.com"+tc.path)
			assert.Equal(t, tc.want, m.Match(r))
		})
	}
}

func TestPathRegexMatcher(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		pattern string
		path    string
		want    bool
	}{
		{"simple match", `^/users/\d+$`, "/users/42", true},
		{"no match", `^/users/\d+$`, "/users/abc", false},
		{"partial match without anchors", `users`, "/api/users/42", true},
		{"anchored no match", `^/users$`, "/users/42", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			re := regexp.MustCompile(tc.pattern)
			m := newPathRegexMatcher(re)
			r := newRequest(t, "http://example.com"+tc.path)
			assert.Equal(t, tc.want, m.Match(r))
		})
	}
}

func TestHeaderMatcher(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		expected map[string]string
		headers  map[string]string
		want     bool
	}{
		{
			name:     "single header matches",
			expected: map[string]string{"X-Tenant": "acme"},
			headers:  map[string]string{"X-Tenant": "acme"},
			want:     true,
		},
		{
			name:     "header name canonicalized",
			expected: map[string]string{"x-tenant": "acme"},
			headers:  map[string]string{"X-Tenant": "acme"},
			want:     true,
		},
		{
			name:     "multiple headers all match",
			expected: map[string]string{"X-Tenant": "acme", "X-Env": "prod"},
			headers:  map[string]string{"X-Tenant": "acme", "X-Env": "prod"},
			want:     true,
		},
		{
			name:     "one of many missing",
			expected: map[string]string{"X-Tenant": "acme", "X-Env": "prod"},
			headers:  map[string]string{"X-Tenant": "acme"},
			want:     false,
		},
		{
			name:     "value differs",
			expected: map[string]string{"X-Tenant": "acme"},
			headers:  map[string]string{"X-Tenant": "other"},
			want:     false,
		},
		{
			name:     "value case-sensitive",
			expected: map[string]string{"X-Tenant": "acme"},
			headers:  map[string]string{"X-Tenant": "ACME"},
			want:     false,
		},
		{
			name:     "empty expected matches anything",
			expected: map[string]string{},
			headers:  map[string]string{"X-Tenant": "acme"},
			want:     true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := newHeaderMatcher(tc.expected)
			r := newRequest(t, "http://example.com/")
			for k, v := range tc.headers {
				r.Header.Set(k, v)
			}
			assert.Equal(t, tc.want, m.Match(r))
		})
	}
}

func TestHeaderMatcher_InputCopied(t *testing.T) {
	t.Parallel()

	in := map[string]string{"X-Tenant": "acme"}
	m := newHeaderMatcher(in)
	in["X-Tenant"] = "tampered"

	r := newRequest(t, "http://example.com/")
	r.Header.Set("X-Tenant", "acme")
	assert.True(t, m.Match(r), "matcher must not be affected by post-construction mutation")
}

func TestQueryMatcher(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		expected map[string]string
		query    string
		want     bool
	}{
		{"single param match", map[string]string{"v": "1"}, "v=1", true},
		{"multiple params all match", map[string]string{"v": "1", "env": "prod"}, "v=1&env=prod", true},
		{"one of many missing", map[string]string{"v": "1", "env": "prod"}, "v=1", false},
		{"value differs", map[string]string{"v": "1"}, "v=2", false},
		{"key case-sensitive", map[string]string{"V": "1"}, "v=1", false},
		{"first value used when repeated", map[string]string{"v": "1"}, "v=1&v=2", true},
		{"first value used when repeated, mismatch", map[string]string{"v": "2"}, "v=1&v=2", false},
		{"empty expected matches anything", map[string]string{}, "v=1", true},
		{"missing param", map[string]string{"v": "1"}, "", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := newQueryMatcher(tc.expected)
			target := "http://example.com/"
			if tc.query != "" {
				target += "?" + tc.query
			}
			r := newRequest(t, target)
			assert.Equal(t, tc.want, m.Match(r))
		})
	}
}

type stubMatcher struct {
	result bool
	calls  int
}

func (s *stubMatcher) Match(*http.Request) bool {
	s.calls++
	return s.result
}

func TestAndMatcher(t *testing.T) {
	t.Parallel()

	t.Run("empty returns alwaysMatcher", func(t *testing.T) {
		t.Parallel()
		m := newAndMatcher(nil)
		_, ok := m.(alwaysMatcher)
		assert.True(t, ok, "expected alwaysMatcher for empty children")
		assert.True(t, m.Match(newRequest(t, "http://example.com/")))
	})

	t.Run("single child unwrapped", func(t *testing.T) {
		t.Parallel()
		child := &stubMatcher{result: true}
		m := newAndMatcher([]Matcher{child})
		assert.Same(t, child, m, "single child must be returned unwrapped")
	})

	t.Run("all children match", func(t *testing.T) {
		t.Parallel()
		a := &stubMatcher{result: true}
		b := &stubMatcher{result: true}
		m := newAndMatcher([]Matcher{a, b})
		assert.True(t, m.Match(newRequest(t, "http://example.com/")))
		assert.Equal(t, 1, a.calls)
		assert.Equal(t, 1, b.calls)
	})

	t.Run("short-circuits on first failure", func(t *testing.T) {
		t.Parallel()
		a := &stubMatcher{result: false}
		b := &stubMatcher{result: true}
		m := newAndMatcher([]Matcher{a, b})
		assert.False(t, m.Match(newRequest(t, "http://example.com/")))
		assert.Equal(t, 1, a.calls)
		assert.Equal(t, 0, b.calls, "second matcher must not be evaluated after first fails")
	})
}

func TestAlwaysMatcher(t *testing.T) {
	t.Parallel()
	assert.True(t, alwaysMatcher{}.Match(newRequest(t, "http://example.com/")))
}
