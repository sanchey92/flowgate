package config

import (
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
)

func (c *Config) Validate(log *slog.Logger) error {
	if len(c.Routes) == 0 {
		return errors.New("config: routes must not be empty")
	}

	seenNames := make(map[string]struct{}, len(c.Routes))
	for i := range c.Routes {
		r := &c.Routes[i]
		if r.Name == "" {
			return fmt.Errorf("config: routes[%d]: name is required", i)
		}
		if _, dup := seenNames[r.Name]; dup {
			return fmt.Errorf("config: routes[%d]: duplicate name %q", i, r.Name)
		}
		seenNames[r.Name] = struct{}{}

		if err := validateRoute(r, log); err != nil {
			return fmt.Errorf("config: routes[%d] (%s): %w", i, r.Name, err)
		}
	}
	return nil
}

func validateRoute(r *Route, log *slog.Logger) error {
	proto := strings.ToLower(strings.TrimSpace(r.Protocol))
	switch proto {
	case "tcp", "udp":
		return validateL4Route(r)
	case "http":
		return validateHTTPRoute(r, log)
	default:
		return fmt.Errorf("unknown protocol %q (want tcp|udp|http)", r.Protocol)
	}
}

func validateL4Route(r *Route) error {
	if r.HTTP != nil {
		return errors.New("http block must be empty for l4 protocol")
	}
	if r.Listen == "" {
		return errors.New("listen address is required")
	}
	if len(r.Backends) == 0 {
		return errors.New("backends must not be empty")
	}
	return nil
}

func validateHTTPRoute(r *Route, log *slog.Logger) error {
	if r.HTTP == nil {
		return errors.New("http block is required for http protocol")
	}
	if len(r.Backends) > 0 {
		return errors.New("top-level backends must be empty for http protocol; use http.backend_groups")
	}
	if r.Listen == "" {
		return errors.New("listen address is required")
	}

	h := r.HTTP

	if err := validateBackendGroups(h.BackendGroups); err != nil {
		return fmt.Errorf("backend_groups: %w", err)
	}

	groupNames := make(map[string]struct{}, len(h.BackendGroups))
	for _, g := range h.BackendGroups {
		groupNames[g.Name] = struct{}{}
	}

	if err := validateRoutingRules(h.RoutingRules, groupNames, r.Name, log); err != nil {
		return err
	}

	if err := validateHeaderRules(h.HeaderRules); err != nil {
		return fmt.Errorf("headers: %w", err)
	}

	return nil
}

func validateBackendGroups(groups []BackendGroup) error {
	if len(groups) == 0 {
		return errors.New("must define at least one group")
	}
	seen := make(map[string]struct{}, len(groups))
	for i, g := range groups {
		if g.Name == "" {
			return fmt.Errorf("[%d]: name is required", i)
		}
		if _, dup := seen[g.Name]; dup {
			return fmt.Errorf("[%d]: duplicate name %q", i, g.Name)
		}
		seen[g.Name] = struct{}{}
		if len(g.Backends) == 0 {
			return fmt.Errorf("[%d] (%s): backends must not be empty", i, g.Name)
		}
		if g.Balancer != "" {
			switch strings.ToLower(strings.TrimSpace(g.Balancer)) {
			case "round_robin", "least_conn":
				// ok
			default:
				return fmt.Errorf("[%d] (%s): unknown balancer %q", i, g.Name, g.Balancer)
			}
		}
	}
	return nil
}

func validateRoutingRules(
	rules []RoutingRule,
	groups map[string]struct{},
	routeName string,
	log *slog.Logger,
) error {
	if len(rules) == 0 {
		return errors.New("routing_rules: must define at least one rule")
	}
	hasDefault := false
	for j := range rules {
		rule := &rules[j]
		if err := validateRule(rule, groups); err != nil {
			return fmt.Errorf("routing_rules[%d]: %w", j, err)
		}
		if rule.Match.IsDefault() {
			hasDefault = true
		}
	}
	if !hasDefault && log != nil {
		log.Warn("http route has no default rule; unmatched requests will receive 404",
			slog.String("route", routeName),
			slog.String("hint", `add a rule with match.path_prefix: "/"`),
		)
	}
	return nil
}

func validateRule(rule *RoutingRule, groups map[string]struct{}) error {
	if rule.BackendGroup == "" {
		return errors.New("backend_group is required")
	}
	if _, ok := groups[rule.BackendGroup]; !ok {
		return fmt.Errorf("backend_group %q not found in route", rule.BackendGroup)
	}
	return validateMatch(&rule.Match)
}

func validateMatch(m *MatchCondition) error {
	pathSet := 0
	if m.PathExact != "" {
		pathSet++
	}
	if m.PathPrefix != "" {
		pathSet++
	}
	if m.PathRegex != "" {
		pathSet++
	}
	if pathSet > 1 {
		return errors.New("match: only one of path_exact, path_prefix, path_regex allowed")
	}

	hasMatcher := m.Host != "" ||
		pathSet > 0 ||
		len(m.Headers) > 0 ||
		len(m.QueryParams) > 0
	if !hasMatcher {
		return errors.New("match: at least one matcher required (host|path|headers|query)")
	}

	for name, value := range m.Headers {
		if err := validateHeaderName(name); err != nil {
			return fmt.Errorf("match.headers: %w", err)
		}
		if strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("match.headers[%q]: value contains CR/LF", name)
		}
	}

	if m.PathRegex != "" {
		if _, err := regexp.Compile(m.PathRegex); err != nil {
			return fmt.Errorf("path_regex: %w", err)
		}
	}

	return nil
}

func validateHeaderRules(h HeaderRules) error {
	if err := validateHeaderOp(h.Request, "request"); err != nil {
		return err
	}
	if err := validateHeaderOp(h.Response, "response"); err != nil {
		return err
	}
	return nil
}

func validateHeaderOp(op HeaderOp, side string) error {
	for name, value := range op.Add {
		if err := validateHeaderName(name); err != nil {
			return fmt.Errorf("%s.add: %w", side, err)
		}
		if strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("%s.add[%q]: value contains CR/LF", side, name)
		}
	}
	for _, name := range op.Remove {
		if err := validateHeaderName(name); err != nil {
			return fmt.Errorf("%s.remove: %w", side, err)
		}
	}
	for name, value := range op.Replace {
		if err := validateHeaderName(name); err != nil {
			return fmt.Errorf("%s.replace: %w", side, err)
		}
		if strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("%s.replace[%q]: value contains CR/LF", side, name)
		}
		if _, dup := op.Add[name]; dup {
			return fmt.Errorf("%s: header %q present in both add and replace", side, name)
		}
	}
	return nil
}

func validateHeaderName(name string) error {
	if name == "" {
		return errors.New("header name is empty")
	}
	if strings.ContainsAny(name, " \t\r\n:") {
		return fmt.Errorf("header name %q contains forbidden characters", name)
	}
	return nil
}
