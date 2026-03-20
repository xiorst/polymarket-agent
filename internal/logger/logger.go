package logger

import (
	"log/slog"
	"os"
	"strings"
)

// Setup initializes the structured JSON logger with the given level.
func Setup(level string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}

	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level:     lvl,
		AddSource: true,
	})

	logger := slog.New(handler)
	slog.SetDefault(logger)
	return logger
}
