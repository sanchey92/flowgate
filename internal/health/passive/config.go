package passive

import (
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
