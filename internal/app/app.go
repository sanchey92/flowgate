package app

import (
	"context"
	"log/slog"

	"github.com/sanchey92/flowgate/internal/config"
	"github.com/sanchey92/flowgate/pkg/logger"
)

type App struct {
	log *slog.Logger
}

func New(cfg *config.Config) *App {
	log := logger.Setup(cfg.Env, cfg.LogLevel)
	return &App{log: log}
}

func (a *App) Run(_ context.Context) error {
	a.log.Info("App starting...")
	return nil
}
