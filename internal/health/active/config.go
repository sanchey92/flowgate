package active

import (
	"fmt"
	"time"
)

const (
	defaultInterval           = 10 * time.Second
	defaultTimeout            = 3 * time.Second
	defaultUnhealthyThreshold = 3
	defaultHealthyThreshold   = 2
	defaultExpectedStatus     = 200
	defaultPath               = "/"
)

type Config struct {
	Enabled            bool
	Interval           time.Duration
	Timeout            time.Duration
	UnhealthyThreshold int
	HealthyThreshold   int
	Path               string
	ExpectedStatus     int
}

func (c *Config) withDefaults() *Config {
	if c.Interval <= 0 {
		c.Interval = defaultInterval
	}
	if c.Timeout <= 0 {
		c.Timeout = defaultTimeout
	}
	if c.UnhealthyThreshold <= 0 {
		c.UnhealthyThreshold = defaultUnhealthyThreshold
	}
	if c.HealthyThreshold <= 0 {
		c.HealthyThreshold = defaultHealthyThreshold
	}
	if c.Path == "" {
		c.Path = defaultPath
	}
	if c.ExpectedStatus == 0 {
		c.ExpectedStatus = defaultExpectedStatus
	}
	return c
}

func (c *Config) validate() error {
	if c.Interval <= c.Timeout {
		return fmt.Errorf("active: interval (%s) must be greater than timeout (%s)",
			c.Interval, c.Timeout)
	}
	if c.UnhealthyThreshold <= 0 || c.HealthyThreshold <= 0 {
		return fmt.Errorf("active: thresholds must be > 0")
	}
	return nil
}
