package router

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"

	"github.com/sanchey92/flowgate/internal/config"
)

type Router struct {
	exact    []*compiledRule
	regex    []*compiledRule
	prefix   []*compiledRule
	fallback *compiledRule
}

func Build(rules []config.RoutingRule, log *slog.Logger) (*Router, error) {
	if len(rules) == 0 {
		return nil, errors.New("router: rules must not be empty")
	}

	r := &Router{}

	for i := range rules {
		matcher, err := buildMatcher(&rules[i].Match)
		if err != nil {
			return nil, fmt.Errorf("router: rules[%d] (%s): %w", i, rules[i].BackendGroup, err)
		}
		cr := &compiledRule{
			matcher: matcher,
			group:   rules[i].BackendGroup,
		}
		b, key := classify(&rules[i].Match)
		cr.priorityKey = key

		switch b {
		case bucketExact:
			r.exact = append(r.exact, cr)
		case bucketRegex:
			r.regex = append(r.regex, cr)
		case bucketPrefix:
			r.prefix = append(r.prefix, cr)
		case bucketFallback:
			if r.fallback != nil {
				if log != nil {
					log.Warn("router: duplicate default rule; first one wins",
						slog.String("ignored_group", cr.group),
						slog.String("active_group", r.fallback.group),
					)
				}
				continue
			}
			r.fallback = cr
		}
	}

	sort.SliceStable(r.prefix, func(i, j int) bool {
		return r.prefix[i].priorityKey > r.prefix[j].priorityKey
	})

	return r, nil
}

func (r *Router) Route(req *http.Request) (string, bool) {
	if rule := scanBucket(r.exact, req); rule != nil {
		return rule.group, true
	}
	if rule := scanBucket(r.regex, req); rule != nil {
		return rule.group, true
	}
	if rule := scanBucket(r.prefix, req); rule != nil {
		return rule.group, true
	}
	if r.fallback != nil {
		return r.fallback.group, true
	}
	return "", false
}

func scanBucket(rules []*compiledRule, req *http.Request) *compiledRule {
	for _, cr := range rules {
		if cr.matcher.Match(req) {
			return cr
		}
	}
	return nil
}
