package logger

import (
	"log/slog"
	"os"
	"strings"
)

const (
	envProd = "prod"
)

var version = "dev"

func Setup(env, level string) *slog.Logger {
	lvl := parseLevel(level)

	opts := &slog.HandlerOptions{Level: lvl}

	var handler slog.Handler
	if env == envProd {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		handler = slog.NewTextHandler(os.Stdout, opts)
	}

	log := slog.New(handler).With(
		slog.String("version", version),
		slog.String("instance_id", instanceID()),
	)

	slog.SetDefault(log)

	return log
}

func parseLevel(level string) slog.Level {
	switch strings.ToLower(level) {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func instanceID() string {
	host, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return host
}
