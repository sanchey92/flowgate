package passive

import (
	"fmt"
	"time"
)

const (
	defaultErrorThreshold   = 5
	defaultWindow           = 30 * time.Second
	defaultRecoveryInterval = 60 * time.Second
	windowBuckets           = 10
)

type Config struct {
	ErrorThreshold   int
	Window           time.Duration
	RecoveryInterval time.Duration
}

func (c *Config) withDefaults() *Config {
	if c.ErrorThreshold <= 0 {
		c.ErrorThreshold = defaultErrorThreshold
	}
	if c.Window <= 0 {
		c.Window = defaultWindow
	}
	if c.RecoveryInterval <= 0 {
		c.RecoveryInterval = defaultRecoveryInterval
	}
	return c
}

func (c *Config) validate() error {
	if c.ErrorThreshold <= 0 {
		return fmt.Errorf("passive: error_threshold must be > 0, got %d", c.ErrorThreshold)
	}
	if c.Window <= 0 {
		return fmt.Errorf("passive: window must be > 0, got %s", c.Window)
	}
	if c.RecoveryInterval <= 0 {
		return fmt.Errorf("passive: recovery_interval must be > 0, got %s", c.RecoveryInterval)
	}
	if c.Window < time.Duration(windowBuckets)*time.Millisecond {
		return fmt.Errorf("passive: window %s too small for %d buckets", c.Window, windowBuckets)
	}
	return nil
}
