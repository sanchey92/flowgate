package main

import (
	"context"

	"github.com/sanchey92/flowgate/internal/app"
	"github.com/sanchey92/flowgate/internal/config"
)

func main() {
	cfg := config.MustLoad(".env")

	a := app.New(cfg)

	if err := a.Run(context.Background()); err != nil {
		panic("failed to run app" + err.Error())
	}
}
