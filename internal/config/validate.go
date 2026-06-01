package config

import (
	"errors"
	"fmt"
	"strings"
)

func (c *Config) Validate() error {
	if len(c.Routes) == 0 {
		return errors.New("config: routes must not be empty")
	}

	seen := make(map[string]struct{}, len(c.Routes))
	for i := range c.Routes {
		r := &c.Routes[i]
		if r.Name == "" {
			return fmt.Errorf("config: routes[%d]: name is required", i)
		}
		if _, dup := seen[r.Name]; dup {
			return fmt.Errorf("config: routes[%d]: duplicate name %q", i, r.Name)
		}
		seen[r.Name] = struct{}{}

		if r.HTTP != nil {
			if err := validateHTTP(r.HTTP); err != nil {
				return fmt.Errorf("config: routes[%d] (%s): %w", i, r.Name, err)
			}
		}
	}
	return nil
}

func validateHTTP(h *HTTPConfig) error {
	groups := make(map[string]struct{}, len(h.BackendGroups))
	for i, g := range h.BackendGroups {
		if _, dup := groups[g.Name]; dup {
			return fmt.Errorf("backend_groups[%d]: duplicate name %q", i, g.Name)
		}
		groups[g.Name] = struct{}{}
	}

	for i, rule := range h.RoutingRules {
		if _, ok := groups[rule.BackendGroup]; !ok {
			return fmt.Errorf("routing_rules[%d]: backend_group %q not found", i, rule.BackendGroup)
		}
		for name, value := range rule.Match.Headers {
			if err := checkHeader(name, value); err != nil {
				return fmt.Errorf("routing_rules[%d].match.headers: %w", i, err)
			}
		}
	}

	if err := checkHeaderOp(h.HeaderRules.Request, "request"); err != nil {
		return err
	}
	return checkHeaderOp(h.HeaderRules.Response, "response")
}

func checkHeaderOp(op HeaderOp, side string) error {
	for name, value := range op.Add {
		if err := checkHeader(name, value); err != nil {
			return fmt.Errorf("headers.%s.add: %w", side, err)
		}
	}
	for name, value := range op.Replace {
		if err := checkHeader(name, value); err != nil {
			return fmt.Errorf("headers.%s.replace: %w", side, err)
		}
	}
	for _, name := range op.Remove {
		if strings.ContainsAny(name, " \t\r\n:") {
			return fmt.Errorf("headers.%s.remove: header name %q contains forbidden characters", side, name)
		}
	}
	return nil
}

func checkHeader(name, value string) error {
	if name == "" {
		return errors.New("header name is empty")
	}
	if strings.ContainsAny(name, " \t\r\n:") {
		return fmt.Errorf("header name %q contains forbidden characters", name)
	}
	if strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("header %q value contains CR/LF", name)
	}
	return nil
}
