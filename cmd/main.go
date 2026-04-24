package main

import (
	"github.com/sanchey92/flowgate/internal/config"
	"github.com/sanchey92/flowgate/pkg/logger"
)

func main() {
	cfg := config.MustLoad(".env")

	log := logger.Setup(cfg.Env, cfg.LogLevel)

	log.Info("FlowGate starting...", "env", cfg.Env)
}
