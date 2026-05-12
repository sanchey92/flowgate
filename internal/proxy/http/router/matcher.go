package router

import (
	"net"
	"net/http"
	"net/textproto"
	"regexp"
	"strings"
)

type hostMatcher struct {
	expected string
}

func newHostMatcher(host string) *hostMatcher {
	return &hostMatcher{expected: stripPort(host)}
}

func (h *hostMatcher) Match(r *http.Request) bool {
	return strings.EqualFold(stripPort(r.Host), h.expected)
}

func stripPort(hostport string) string {
	if host, _, err := net.SplitHostPort(hostport); err == nil {
		return host
	}
	return hostport
}

type pathExactMatcher struct {
	expected string
}

func newPathExactMatcher(path string) *pathExactMatcher {
	return &pathExactMatcher{expected: path}
}

func (p *pathExactMatcher) Match(r *http.Request) bool {
	return r.URL.Path == p.expected
}

type pathPrefixMatcher struct {
	prefix string
}

func newPathPrefixMatcher(prefix string) *pathPrefixMatcher {
	return &pathPrefixMatcher{prefix: prefix}
}

func (p *pathPrefixMatcher) Match(r *http.Request) bool {
	path := r.URL.Path
	if !strings.HasPrefix(path, p.prefix) {
		return false
	}
	if len(path) == len(p.prefix) || strings.HasSuffix(p.prefix, "/") {
		return true
	}
	return path[len(p.prefix)] == '/'
}

type pathRegexMatcher struct {
	re *regexp.Regexp
}

func newPathRegexMatcher(re *regexp.Regexp) *pathRegexMatcher {
	return &pathRegexMatcher{re: re}
}

func (p *pathRegexMatcher) Match(r *http.Request) bool {
	return p.re.MatchString(r.URL.Path)
}

type headerMatcher struct {
	expected map[string]string
}

func newHeaderMatcher(in map[string]string) *headerMatcher {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[textproto.CanonicalMIMEHeaderKey(k)] = v
	}
	return &headerMatcher{expected: out}
}

func (h *headerMatcher) Match(r *http.Request) bool {
	for name, want := range h.expected {
		if r.Header.Get(name) != want {
			return false
		}
	}
	return true
}

type queryMatcher struct {
	expected map[string]string
}

func newQueryMatcher(in map[string]string) *queryMatcher {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return &queryMatcher{expected: out}
}

func (q *queryMatcher) Match(r *http.Request) bool {
	qs := r.URL.Query()
	for name, want := range q.expected {
		if qs.Get(name) != want {
			return false
		}
	}
	return true
}

type andMatcher struct {
	children []Matcher
}

func newAndMatcher(children []Matcher) Matcher {
	switch len(children) {
	case 0:
		return alwaysMatcher{}
	case 1:
		return children[0]
	default:
		return &andMatcher{children: children}
	}
}

func (a *andMatcher) Match(r *http.Request) bool {
	for _, m := range a.children {
		if !m.Match(r) {
			return false
		}
	}
	return true
}

type alwaysMatcher struct{}

func (alwaysMatcher) Match(*http.Request) bool { return true }
