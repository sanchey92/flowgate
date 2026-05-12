package router

import (
	"fmt"
	"net/http"
	"regexp"

	"github.com/sanchey92/flowgate/internal/config"
)

type Matcher interface {
	Match(r *http.Request) bool
}

type compiledRule struct {
	matcher     Matcher
	group       string
	priorityKey int
}

func buildMatcher(m *config.MatchCondition) (Matcher, error) {
	var children []Matcher

	if m.Host != "" {
		children = append(children, newHostMatcher(m.Host))
	}
	if len(m.Headers) > 0 {
		children = append(children, newHeaderMatcher(m.Headers))
	}
	if len(m.QueryParams) > 0 {
		children = append(children, newQueryMatcher(m.QueryParams))
	}

	switch {
	case m.PathExact != "":
		children = append(children, newPathExactMatcher(m.PathExact))
	case m.PathRegex != "":
		re, err := regexp.Compile(m.PathRegex)
		if err != nil {
			return nil, fmt.Errorf("compile path_regex %q: %w", m.PathRegex, err)
		}
		children = append(children, newPathRegexMatcher(re))
	case m.PathPrefix != "":
		children = append(children, newPathPrefixMatcher(m.PathPrefix))
	}

	return newAndMatcher(children), nil
}

type bucket int

const (
	bucketExact bucket = iota
	bucketRegex
	bucketPrefix
	bucketFallback
)

func classify(m *config.MatchCondition) (bucket, int) {
	if m.IsDefault() {
		return bucketFallback, 0
	}
	switch {
	case m.PathExact != "":
		return bucketExact, 0
	case m.PathRegex != "":
		return bucketRegex, 0
	case m.PathPrefix != "":
		return bucketPrefix, len(m.PathPrefix)
	default:
		return bucketPrefix, 1
	}
}
