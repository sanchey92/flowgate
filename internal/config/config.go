package config

import (
	"fmt"
	"os"

	"github.com/ilyakaznacheev/cleanenv"
	"github.com/joho/godotenv"
)

type Config struct {
	Env      string  `yaml:"env" env:"ENV" env-default:"dev"`
	LogLevel string  `yaml:"log_level" env:"LOG_LEVEL" env-default:"info"`
	Backend  Backend `yaml:"backend"`
}

type Backend struct {
	Addr   string `yaml:"addr"`
	Weight int    `yaml:"weight"`
}

func MustLoad(env string) *Config {
	if err := loadEnv(env); err != nil {
		panic("failed to load env file" + err.Error())
	}
	cfgPath := os.Getenv("CONFIG_PATH")
	if cfgPath == "" {
		panic("CONFIG_PATH is not set")
	}

	var cfg Config

	if err := cleanenv.ReadConfig(cfgPath, &cfg); err != nil {
		panic("failed to read config" + err.Error())
	}

	return &cfg
}

func loadEnv(env string) error {
	if err := godotenv.Load(env); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("load env: %w", err)
	}
	return nil
}
