package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/sanchey92/flowgate/internal/app"
	"github.com/sanchey92/flowgate/internal/config"
)

func main() {
	cfg := config.MustLoad(".env")

	a := app.New(cfg)

	if err := a.Run(context.Background()); err != nil {
		slog.Error("app exited with error", slog.Any("error", err))
		os.Exit(1)
	}
}
