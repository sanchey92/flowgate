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
