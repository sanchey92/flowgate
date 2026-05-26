//go:build integration

package integration

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sanchey92/flowgate/internal/config"
)

// TestE2E_HTTP_RoutingMatrix — Stage G2 §1: «HTTP route с 5 rules → для каждого
// по 1 запросу, ассерт на правильный backend_group».
//
// Берём пять групп, под каждой — отдельный httptest-бэкенд. Маршруты подобраны
// так, чтобы каждый из четырёх не-fallback приоритетов сработал ровно один раз,
// а пятый запрос ушёл в default.
func TestE2E_HTTP_RoutingMatrix(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	beExact := startHTTPBackend(t, "exact")
	beRegex := startHTTPBackend(t, "regex")
	bePrefix := startHTTPBackend(t, "prefix")
	beHost := startHTTPBackend(t, "host")
	beDefault := startHTTPBackend(t, "default")

	groups := mkGroups(
		httpGroupSpec{Name: "g-exact", Addrs: []string{beExact.Addr()}},
		httpGroupSpec{Name: "g-regex", Addrs: []string{beRegex.Addr()}},
		httpGroupSpec{Name: "g-prefix", Addrs: []string{bePrefix.Addr()}},
		httpGroupSpec{Name: "g-host", Addrs: []string{beHost.Addr()}},
		httpGroupSpec{Name: "g-default", Addrs: []string{beDefault.Addr()}},
	)

	rules := []config.RoutingRule{
		{Match: config.MatchCondition{PathExact: "/healthz"}, BackendGroup: "g-exact"},
		{Match: config.MatchCondition{PathRegex: `^/users/\d+$`}, BackendGroup: "g-regex"},
		{Match: config.MatchCondition{PathPrefix: "/api"}, BackendGroup: "g-prefix"},
		{Match: config.MatchCondition{Host: "tenant.example.com"}, BackendGroup: "g-host"},
		{Match: config.MatchCondition{PathPrefix: "/"}, BackendGroup: "g-default"},
	}

	route := httpRoute("matrix", groups, rules, config.WebSocketConfig{Enabled: false})
	p := startProxy(t, ctx, route, discardLogger())
	addr := p.Addr().String()
	waitListening(t, addr, 2*time.Second)

	type expect struct {
		name string
		path string
		host string
		want string // backend label
	}
	cases := []expect{
		{"exact path /healthz", "/healthz", "any.example.com", "exact"},
		{"regex /users/42", "/users/42", "any.example.com", "regex"},
		{"prefix /api wins over default", "/api/orders", "any.example.com", "prefix"},
		{"host tenant.example.com wins over default", "/anywhere", "tenant.example.com", "host"},
		{"fallthrough to default", "/random/path", "other.example.com", "default"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			resp, body := httpGET(t, addr, tc.host, tc.path, nil)
			require.Equal(t, http.StatusOK, resp.StatusCode, "body=%s", body)
			assert.Equal(t, tc.want, resp.Header.Get("X-Backend-Label"),
				"маршрут %q должен был попасть в backend %q (X-Backend-Label)", tc.path, tc.want)
		})
	}

	// Дополнительная страховка: каждый backend взял ровно один запрос.
	assert.EqualValues(t, 1, beExact.Hits(), "exact hits")
	assert.EqualValues(t, 1, beRegex.Hits(), "regex hits")
	assert.EqualValues(t, 1, bePrefix.Hits(), "prefix hits")
	assert.EqualValues(t, 1, beHost.Hits(), "host hits")
	assert.EqualValues(t, 1, beDefault.Hits(), "default hits")
}
