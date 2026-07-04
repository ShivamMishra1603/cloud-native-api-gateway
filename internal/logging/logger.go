package logging

import (
	"log/slog"
	"os"
	"strings"
)

// Init initializes the global structured logger with the specified log level.
// It sets up a JSON handler outputting to standard output.
// Supported levels are: debug, info, warn, error. Defaults to info.
func Init(levelStr string) {
	var level slog.Level
	switch strings.ToLower(strings.TrimSpace(levelStr)) {
	case "debug":
		level = slog.LevelDebug
	case "info":
		level = slog.LevelInfo
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: level,
	})

	logger := slog.New(handler)
	slog.SetDefault(logger)
}
