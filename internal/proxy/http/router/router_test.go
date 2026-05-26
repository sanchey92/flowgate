package router

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sanchey92/flowgate/internal/config"
)

func mkRules(rules ...config.RoutingRule) []config.RoutingRule {
	return rules
}

func req(t *testing.T, target string) *http.Request {
	t.Helper()
	return httptest.NewRequest(http.MethodGet, target, nil)
}

func TestBuild_EmptyRules(t *testing.T) {
	t.Parallel()
	_, err := Build(nil, nil)
	assert.Error(t, err)

	_, err = Build([]config.RoutingRule{}, nil)
	assert.Error(t, err)
}

func TestBuild_InvalidRegexReturnsError(t *testing.T) {
	t.Parallel()
	rules := mkRules(
		config.RoutingRule{Match: config.MatchCondition{PathRegex: `(unclosed`}, BackendGroup: "g1"},
	)
	_, err := Build(rules, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "path_regex")
}

func TestBuild_NilLoggerSafe(t *testing.T) {
	t.Parallel()
	rules := mkRules(
		config.RoutingRule{Match: config.MatchCondition{PathPrefix: "/"}, BackendGroup: "g1"},
		config.RoutingRule{Match: config.MatchCondition{PathPrefix: "/"}, BackendGroup: "g2"},
	)
	r, err := Build(rules, nil)
	require.NoError(t, err)

	group, ok := r.Route(req(t, "http://example.com/anything"))
	assert.True(t, ok)
	assert.Equal(t, "g1", group)
}

func TestBuild_DuplicateDefault_FirstWins(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))

	rules := mkRules(
		config.RoutingRule{Match: config.MatchCondition{PathPrefix: "/"}, BackendGroup: "g1"},
		config.RoutingRule{Match: config.MatchCondition{PathPrefix: "/"}, BackendGroup: "g2"},
	)
	r, err := Build(rules, log)
	require.NoError(t, err)

	group, ok := r.Route(req(t, "http://example.com/anything"))
	assert.True(t, ok)
	assert.Equal(t, "g1", group)

	logOut := buf.String()
	assert.Contains(t, logOut, "duplicate default rule")
	assert.Contains(t, logOut, "ignored_group=g2")
	assert.Contains(t, logOut, "active_group=g1")
}

func TestRoute_ExactBeatsPrefixAndFallback(t *testing.T) {
	t.Parallel()
	rules := mkRules(
		config.RoutingRule{Match: config.MatchCondition{PathPrefix: "/api"}, BackendGroup: "g2"},
		config.RoutingRule{Match: config.MatchCondition{PathExact: "/api/users"}, BackendGroup: "g1"},
		config.RoutingRule{Match: config.MatchCondition{PathPrefix: "/"}, BackendGroup: "g3"},
	)
	r, err := Build(rules, nil)
	require.NoError(t, err)

	group, ok := r.Route(req(t, "http://example.com/api/users"))
	assert.True(t, ok)
	assert.Equal(t, "g1", group, "exact must win over prefix")
}

func TestRoute_RegexBeatsPrefix(t *testing.T) {
	t.Parallel()
	rules := mkRules(
		config.RoutingRule{Match: config.MatchCondition{PathPrefix: "/api"}, BackendGroup: "g2"},
		config.RoutingRule{Match: config.MatchCondition{PathRegex: `^/api/users/\d+$`}, BackendGroup: "g1"},
	)
	r, err := Build(rules, nil)
	require.NoError(t, err)

	group, ok := r.Route(req(t, "http://example.com/api/users/42"))
	assert.True(t, ok)
	assert.Equal(t, "g1", group, "regex must win over prefix")

	group, ok = r.Route(req(t, "http://example.com/api/users/abc"))
	assert.True(t, ok)
	assert.Equal(t, "g2", group, "regex miss falls through to prefix")
}

func TestRoute_PrefixBeatsFallback(t *testing.T) {
	t.Parallel()
	rules := mkRules(
		config.RoutingRule{Match: config.MatchCondition{PathPrefix: "/"}, BackendGroup: "g2"},
		config.RoutingRule{Match: config.MatchCondition{PathPrefix: "/api"}, BackendGroup: "g1"},
	)
	r, err := Build(rules, nil)
	require.NoError(t, err)

	group, ok := r.Route(req(t, "http://example.com/api/users"))
	assert.True(t, ok)
	assert.Equal(t, "g1", group, "specific prefix must win over default fallback")
}

func TestRoute_PrefixSortedByLength(t *testing.T) {
	t.Parallel()
	rules := mkRules(
		config.RoutingRule{Match: config.MatchCondition{PathPrefix: "/api"}, BackendGroup: "g1"},
		config.RoutingRule{Match: config.MatchCondition{PathPrefix: "/api/v1"}, BackendGroup: "g2"},
		config.RoutingRule{Match: config.MatchCondition{PathPrefix: "/api/v1/users"}, BackendGroup: "g3"},
	)
	r, err := Build(rules, nil)
	require.NoError(t, err)

	cases := []struct {
		path string
		want string
	}{
		{"/api/v1/users/42", "g3"},
		{"/api/v1/orders", "g2"},
		{"/api/health", "g1"},
	}
	for _, tc := range cases {
		group, ok := r.Route(req(t, "http://example.com"+tc.path))
		assert.True(t, ok, tc.path)
		assert.Equal(t, tc.want, group, tc.path)
	}
}

func TestRoute_HostOnlyRule_HasLowerPriorityThanRealPrefix(t *testing.T) {
	t.Parallel()
	rules := mkRules(
		config.RoutingRule{Match: config.MatchCondition{Host: "tenant.example.com"}, BackendGroup: "g1"},
		config.RoutingRule{Match: config.MatchCondition{PathPrefix: "/api"}, BackendGroup: "g2"},
	)
	r, err := Build(rules, nil)
	require.NoError(t, err)

	r1 := req(t, "http://placeholder/api/users")
	r1.Host = "tenant.example.com"
	group, ok := r.Route(r1)
	assert.True(t, ok)
	assert.Equal(t, "g2", group, "/api wins over host-only when both match")

	r2 := req(t, "http://placeholder/other")
	r2.Host = "tenant.example.com"
	group, ok = r.Route(r2)
	assert.True(t, ok)
	assert.Equal(t, "g1", group, "host-only catches paths /api does not")

	r3 := req(t, "http://placeholder/other")
	r3.Host = "elsewhere.example.com"
	_, ok = r.Route(r3)
	assert.False(t, ok, "no fallback => no match")
}

func TestRoute_FallbackUsed(t *testing.T) {
	t.Parallel()
	rules := mkRules(
		config.RoutingRule{Match: config.MatchCondition{PathPrefix: "/api"}, BackendGroup: "g1"},
		config.RoutingRule{Match: config.MatchCondition{PathPrefix: "/"}, BackendGroup: "g2"},
	)
	r, err := Build(rules, nil)
	require.NoError(t, err)

	group, ok := r.Route(req(t, "http://example.com/static/logo.png"))
	assert.True(t, ok)
	assert.Equal(t, "g2", group)
}

func TestRoute_NoMatch_NoFallback(t *testing.T) {
	t.Parallel()
	rules := mkRules(
		config.RoutingRule{Match: config.MatchCondition{PathPrefix: "/api"}, BackendGroup: "g1"},
	)
	r, err := Build(rules, nil)
	require.NoError(t, err)

	group, ok := r.Route(req(t, "http://example.com/other"))
	assert.False(t, ok)
	assert.Equal(t, "", group)
}

func TestRoute_CombinedHostAndPath(t *testing.T) {
	t.Parallel()
	rules := mkRules(
		config.RoutingRule{
			Match:        config.MatchCondition{Host: "a.example.com", PathPrefix: "/api"},
			BackendGroup: "g1",
		},
		config.RoutingRule{Match: config.MatchCondition{PathPrefix: "/"}, BackendGroup: "g2"},
	)
	r, err := Build(rules, nil)
	require.NoError(t, err)

	r1 := req(t, "http://placeholder/api/x")
	r1.Host = "a.example.com"
	group, _ := r.Route(r1)
	assert.Equal(t, "g1", group)

	r2 := req(t, "http://placeholder/api/x")
	r2.Host = "b.example.com"
	group, _ = r.Route(r2)
	assert.Equal(t, "g2", group, "host mismatch must fall through to default")

	r3 := req(t, "http://placeholder/other")
	r3.Host = "a.example.com"
	group, _ = r.Route(r3)
	assert.Equal(t, "g2", group, "path mismatch must fall through to default")
}

func TestRoute_HeaderAndQueryRule(t *testing.T) {
	t.Parallel()
	rules := mkRules(
		config.RoutingRule{
			Match: config.MatchCondition{
				PathPrefix:  "/api",
				Headers:     map[string]string{"X-Tenant": "acme"},
				QueryParams: map[string]string{"v": "2"},
			},
			BackendGroup: "g1",
		},
		config.RoutingRule{Match: config.MatchCondition{PathPrefix: "/"}, BackendGroup: "g2"},
	)
	r, err := Build(rules, nil)
	require.NoError(t, err)

	r1 := req(t, "http://example.com/api/items?v=2")
	r1.Header.Set("X-Tenant", "acme")
	group, _ := r.Route(r1)
	assert.Equal(t, "g1", group)

	r2 := req(t, "http://example.com/api/items?v=2")
	r2.Header.Set("X-Tenant", "other")
	group, _ = r.Route(r2)
	assert.Equal(t, "g2", group, "header mismatch falls to default")

	r3 := req(t, "http://example.com/api/items?v=1")
	r3.Header.Set("X-Tenant", "acme")
	group, _ = r.Route(r3)
	assert.Equal(t, "g2", group, "query mismatch falls to default")
}

func TestClassify(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		m       config.MatchCondition
		wantB   bucket
		wantKey int
	}{
		{"default", config.MatchCondition{PathPrefix: "/"}, bucketFallback, 0},
		{"exact", config.MatchCondition{PathExact: "/x"}, bucketExact, 0},
		{"regex", config.MatchCondition{PathRegex: `^/x$`}, bucketRegex, 0},
		{"prefix", config.MatchCondition{PathPrefix: "/api/v1"}, bucketPrefix, len("/api/v1")},
		{"host-only goes to prefix with prio 1", config.MatchCondition{Host: "h"}, bucketPrefix, 1},
		{"headers-only goes to prefix with prio 1", config.MatchCondition{Headers: map[string]string{"X": "y"}}, bucketPrefix, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b, key := classify(&tc.m)
			assert.Equal(t, tc.wantB, b)
			assert.Equal(t, tc.wantKey, key)
		})
	}
}

func TestRoute_PrefixOrderStableWithinSameLength(t *testing.T) {
	t.Parallel()
	rules := mkRules(
		config.RoutingRule{Match: config.MatchCondition{PathPrefix: "/aaa"}, BackendGroup: "g1"},
		config.RoutingRule{Match: config.MatchCondition{PathPrefix: "/bbb"}, BackendGroup: "g2"},
	)
	r, err := Build(rules, nil)
	require.NoError(t, err)

	g1, _ := r.Route(req(t, "http://example.com/aaa/x"))
	assert.Equal(t, "g1", g1)

	g2, _ := r.Route(req(t, "http://example.com/bbb/x"))
	assert.Equal(t, "g2", g2)
}

func TestRoute_RegexOrderPreservesDeclaration(t *testing.T) {
	t.Parallel()
	rules := mkRules(
		config.RoutingRule{Match: config.MatchCondition{PathRegex: `^/users/\d+$`}, BackendGroup: "g1"},
		config.RoutingRule{Match: config.MatchCondition{PathRegex: `^/users/.+$`}, BackendGroup: "g2"},
	)
	r, err := Build(rules, nil)
	require.NoError(t, err)

	group, _ := r.Route(req(t, "http://example.com/users/42"))
	assert.Equal(t, "g1", group, "first matching regex in declaration order wins")

	group, _ = r.Route(req(t, "http://example.com/users/abc"))
	assert.Equal(t, "g2", group)
}

func TestRoute_DefaultIsExactRootOnly(t *testing.T) {
	t.Parallel()
	rules := mkRules(
		config.RoutingRule{Match: config.MatchCondition{PathPrefix: "/"}, BackendGroup: "g1"},
	)
	r, err := Build(rules, nil)
	require.NoError(t, err)

	for _, p := range []string{"/", "/foo", "/foo/bar", "/x/y/z"} {
		group, ok := r.Route(req(t, "http://example.com"+p))
		assert.True(t, ok, p)
		assert.Equal(t, "g1", group, p)
	}
}

// TestRoute_TableDriven_CombinedScenarios — широкая таблица, закрывающая
// комбинации host/path/headers/query, которые приоритетные тесты выше
// специально не трогают. Один билдер, один набор правил, много кейсов.
func TestRoute_TableDriven_CombinedScenarios(t *testing.T) {
	t.Parallel()

	rules := mkRules(
		// exact corner-case: точный "/" — не fallback, но всё равно exact-bucket.
		config.RoutingRule{Match: config.MatchCondition{PathExact: "/"}, BackendGroup: "g-root-exact"},
		// host + regex
		config.RoutingRule{
			Match:        config.MatchCondition{Host: "api.example.com", PathRegex: `^/v\d+/users/\d+$`},
			BackendGroup: "g-api-users",
		},
		// host + query
		config.RoutingRule{
			Match:        config.MatchCondition{Host: "shop.example.com", QueryParams: map[string]string{"v": "2"}},
			BackendGroup: "g-shop-v2",
		},
		// prefix с трейлинг-слешем
		config.RoutingRule{Match: config.MatchCondition{PathPrefix: "/static/"}, BackendGroup: "g-static"},
		// длинный prefix
		config.RoutingRule{Match: config.MatchCondition{PathPrefix: "/api/v1"}, BackendGroup: "g-api-v1"},
		// header-only
		config.RoutingRule{
			Match:        config.MatchCondition{Headers: map[string]string{"X-Internal": "1"}},
			BackendGroup: "g-internal",
		},
		// fallback
		config.RoutingRule{Match: config.MatchCondition{PathPrefix: "/"}, BackendGroup: "g-default"},
	)
	r, err := Build(rules, nil)
	require.NoError(t, err)

	type kase struct {
		name    string
		path    string
		host    string
		headers map[string]string
		query   string
		want    string
	}

	cases := []kase{
		{"exact root wins over default", "/", "any.example.com", nil, "", "g-root-exact"},
		{"host+regex picks api-users", "/v1/users/42", "api.example.com", nil, "", "g-api-users"},
		{"api-users regex miss falls to longer prefix", "/v1/users/abc", "api.example.com", nil, "", "g-default"},
		{"host+query matches shop-v2", "/items", "shop.example.com", nil, "v=2", "g-shop-v2"},
		{"shop without query falls to default", "/items", "shop.example.com", nil, "", "g-default"},
		{"static prefix with trailing slash", "/static/logo.png", "any.example.com", nil, "", "g-static"},
		{"static prefix matches exactly /static/", "/static/", "any.example.com", nil, "", "g-static"},
		{"longer prefix /api/v1 wins over /", "/api/v1/products", "any.example.com", nil, "", "g-api-v1"},
		{"header-only rule matches when header present", "/anything", "any.example.com", map[string]string{"X-Internal": "1"}, "", "g-internal"},
		{"header miss falls to default", "/anything", "any.example.com", map[string]string{"X-Internal": "0"}, "", "g-default"},
		{"unknown path lands on default", "/random/path", "any.example.com", nil, "", "g-default"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			target := "http://placeholder" + tc.path
			if tc.query != "" {
				target += "?" + tc.query
			}
			req := httptest.NewRequest(http.MethodGet, target, nil)
			req.Host = tc.host
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			got, ok := r.Route(req)
			require.True(t, ok, "ожидаем попадание для %s", tc.name)
			assert.Equal(t, tc.want, got)
		})
	}
}

// guard against accidental use of strings.HasPrefix without segment boundary
// (the matcher tests cover this too, but we want it visible at the router level).
func TestRoute_PrefixBoundaryAtRouterLevel(t *testing.T) {
	t.Parallel()
	rules := mkRules(
		config.RoutingRule{Match: config.MatchCondition{PathPrefix: "/api"}, BackendGroup: "g1"},
		config.RoutingRule{Match: config.MatchCondition{PathPrefix: "/"}, BackendGroup: "g2"},
	)
	r, err := Build(rules, nil)
	require.NoError(t, err)

	group, _ := r.Route(req(t, "http://example.com/apinotreal"))
	assert.Equal(t, "g2", group, "/apinotreal must not match /api prefix")
}
