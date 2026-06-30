package logger

import (
	"log/slog"
	"os"
)

func Init(service string) *slog.Logger {
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})
	logger := slog.New(handler).With("service", service)
	slog.SetDefault(logger)
	return logger
}
