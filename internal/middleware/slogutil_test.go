package middleware

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSetupLoggerDefaultLevel(t *testing.T) {
	t.Helper()
	logger := SetupLogger("", "text")
	require.NotNil(t, logger)
	// Empty/unknown level defaults to info — debug should be disabled, info enabled
	require.False(t, logger.Enabled(context.Background(), slog.LevelDebug))
	require.True(t, logger.Enabled(context.Background(), slog.LevelInfo))
}

func TestSetupLoggerDebugLevel(t *testing.T) {
	t.Helper()
	logger := SetupLogger("debug", "text")
	require.NotNil(t, logger)
	require.True(t, logger.Enabled(context.Background(), slog.LevelDebug))
	require.True(t, logger.Enabled(context.Background(), slog.LevelInfo))
}

func TestSetupLoggerJSONFormat(t *testing.T) {
	t.Helper()
	logger := SetupLogger("info", "json")
	require.NotNil(t, logger)
	// Verify the logger is functional
	require.True(t, logger.Enabled(context.Background(), slog.LevelInfo))
}

func TestSetupLoggerTextFormat(t *testing.T) {
	t.Helper()
	logger := SetupLogger("info", "text")
	require.NotNil(t, logger)
	require.True(t, logger.Enabled(context.Background(), slog.LevelInfo))
}

func TestSetupLoggerWarnLevel(t *testing.T) {
	t.Helper()
	logger := SetupLogger("warn", "text")
	require.NotNil(t, logger)
	require.False(t, logger.Enabled(context.Background(), slog.LevelInfo))
	require.True(t, logger.Enabled(context.Background(), slog.LevelWarn))
}

func TestSetupLoggerErrorLevel(t *testing.T) {
	t.Helper()
	logger := SetupLogger("error", "text")
	require.NotNil(t, logger)
	require.False(t, logger.Enabled(context.Background(), slog.LevelWarn))
	require.True(t, logger.Enabled(context.Background(), slog.LevelError))
}

func TestSetupLoggerCaseInsensitive(t *testing.T) {
	t.Helper()
	logger := SetupLogger("INFO", "TEXT")
	require.NotNil(t, logger)
	require.True(t, logger.Enabled(context.Background(), slog.LevelInfo))

	logger = SetupLogger("JSON", "JSON")
	require.NotNil(t, logger)

	logger = SetupLogger("Debug", "JsOn")
	require.NotNil(t, logger)
	require.True(t, logger.Enabled(context.Background(), slog.LevelDebug))
}

func TestSetupLoggerUnknownLevelDefaultsToInfo(t *testing.T) {
	t.Helper()
	logger := SetupLogger("unknown_level", "text")
	require.NotNil(t, logger)
	require.False(t, logger.Enabled(context.Background(), slog.LevelDebug))
	require.True(t, logger.Enabled(context.Background(), slog.LevelInfo))
}

func TestComponentLoggerAddsField(t *testing.T) {
	t.Helper()
	var buf bytes.Buffer
	handler := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})
	testLogger := slog.New(handler)
	slog.SetDefault(testLogger)

	logger := ComponentLogger("test")
	require.NotNil(t, logger)

	logger.Info("component-message")
	output := buf.String()
	require.Contains(t, output, "component=test")
	require.Contains(t, output, "component-message")
}

func TestComponentLoggerDifferentComponents(t *testing.T) {
	t.Helper()
	var buf bytes.Buffer
	handler := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})
	testLogger := slog.New(handler)
	slog.SetDefault(testLogger)

	logger1 := ComponentLogger("camera-manager")
	logger2 := ComponentLogger("api")

	logger1.Info("msg-from-camera")
	logger2.Info("msg-from-api")

	output := buf.String()
	require.Contains(t, output, "component=camera-manager")
	require.Contains(t, output, "msg-from-camera")
	require.Contains(t, output, "component=api")
	require.Contains(t, output, "msg-from-api")
}
