package config

import "time"

type HealthCheckConfig struct {
	Active ActiveCheckConfig `yaml:"active"`
	// Passive PassiveCheckConfig — появится в Stage C
}

type ActiveCheckConfig struct {
	Enabled            bool          `yaml:"enabled"`
	Interval           time.Duration `yaml:"interval" env-default:"10s"`
	Timeout            time.Duration `yaml:"timeout" env-default:"3s"`
	UnhealthyThreshold int           `yaml:"unhealthy_threshold" env-default:"3"`
	HealthyThreshold   int           `yaml:"healthy_threshold" env-default:"2"`
	Path               string        `yaml:"path" env-default:"/"`
	ExpectedStatus     int           `yaml:"expected_status" env-default:"200"`
}
