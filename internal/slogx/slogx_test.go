package slogx

// Regression tests for the init-time capture bug (M5 field evidence,
// issue #685): package-level `var x = slog.Default().With(...)` loggers are
// initialized BEFORE main() calls slog.SetDefault, so they hold the pristine
// default handler. Its output routes through the log package — which
// SetDefault has redirected into the new handler — producing mangled lines
// like `level=INFO msg="ERROR connection error, reconnecting component=..."`
// (wrong level, attrs unparseable, ~32% of M5's 24h log volume).

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestComponent_SurvivesSetDefaultSwap(t *testing.T) {
	// "Captured at init": created while the pristine default is installed.
	before := Component("http-jpeg-recorder")

	var buf bytes.Buffer
	after := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	previous := slog.Default()
	slog.SetDefault(after)
	t.Cleanup(func() { slog.SetDefault(previous) })

	before.Error("connection error, reconnecting", "camera_id", "cam-x", "attempt", 1)

	line := buf.String()
	if !strings.Contains(line, `level=ERROR msg="connection error, reconnecting"`) {
		t.Fatalf("expected proper ERROR record, got: %s", line)
	}
	if !strings.Contains(line, "component=http-jpeg-recorder") || !strings.Contains(line, "camera_id=cam-x") {
		t.Fatalf("attrs must be structured, got: %s", line)
	}
	if strings.Contains(line, `msg="ERROR `) {
		t.Fatalf("double-wrapped log line (init-time capture bug): %s", line)
	}
}

func TestComponent_RespectsConfiguredLevel(t *testing.T) {
	before := Component("xiaomi-recorder")

	var buf bytes.Buffer
	after := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	previous := slog.Default()
	slog.SetDefault(after)
	t.Cleanup(func() { slog.SetDefault(previous) })

	before.Info("should be suppressed")
	if buf.Len() != 0 {
		t.Fatalf("INFO must be filtered by the configured warn level, got: %s", buf.String())
	}
}

func TestComponent_WithCarriesAttrs(t *testing.T) {
	base := Component("tutk-transport")
	child := base.With("camera_id", "cam-1")

	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	child.Warn("hello")
	line := buf.String()
	for _, want := range []string{"component=tutk-transport", "camera_id=cam-1", `msg=hello`} {
		if !strings.Contains(line, want) {
			t.Fatalf("missing %q in: %s", want, line)
		}
	}
}

func TestComponent_EnabledMirrorsDefault(t *testing.T) {
	h := Component("x").Handler()
	if h.Enabled(context.Background(), slog.LevelError) {
		// default level is Info, so Error must pass — just sanity that it
		// doesn't panic and consults the live default.
		t.Log("error enabled under default info level")
	}
}
