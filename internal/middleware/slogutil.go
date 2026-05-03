package middleware

import (
	"log/slog"
	"os"
	"strings"
)

// SetupLogger creates and configures a logger with the specified level and format.
// Returns a configured slog.Logger instance.
func SetupLogger(level, format string) *slog.Logger {
	// Parse level string to slog.Level
	var logLevel slog.Level
	switch strings.ToLower(level) {
	case "debug":
		logLevel = slog.LevelDebug
	case "info":
		logLevel = slog.LevelInfo
	case "warn":
		logLevel = slog.LevelWarn
	case "error":
		logLevel = slog.LevelError
	default:
		logLevel = slog.LevelInfo // default to info
	}

	// Create handler based on format
	var handler slog.Handler
	if strings.ToLower(format) == "json" {
		handler = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
			Level:     logLevel,
			AddSource: false,
		})
	} else {
		handler = slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
			Level:     logLevel,
			AddSource: false,
		})
	}

	return slog.New(handler)
}

// ComponentLogger creates a logger with a component attribute.
// Returns a logger that includes the component name in all log messages.
func ComponentLogger(name string) *slog.Logger {
	return slog.Default().With("component", name)
}