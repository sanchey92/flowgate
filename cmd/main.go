package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/sanchey92/flowgate/internal/app"
	"github.com/sanchey92/flowgate/internal/config"
	"github.com/sanchey92/flowgate/pkg/logger"
)

func main() {
	cfg := config.MustLoad(".env")

	log := logger.Setup(cfg.Env, cfg.LogLevel, cfg.Server.InstanceID)
	if err := cfg.Validate(log); err != nil {
		log.Error("config validation failed", slog.Any("error", err))
		os.Exit(1)
	}

	a := app.New(cfg, log)
	if err := a.Run(context.Background()); err != nil {
		log.Error("app exited with error", slog.Any("error", err))
		os.Exit(1)
	}
}
