package config

import (
	"context"
	"log/slog"
	"strings"
	"testing"
)

// captureHandler records warning-or-worse log records.
type captureHandler struct {
	level slog.Level
	warns []string
}

func (h *captureHandler) Enabled(_ context.Context, l slog.Level) bool { return l >= h.level }

func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	if r.Level >= slog.LevelWarn {
		h.warns = append(h.warns, r.Message)
	}
	return nil
}

func (h *captureHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(_ string) slog.Handler      { return h }

func captureWarns(t *testing.T) *captureHandler {
	t.Helper()
	h := &captureHandler{level: slog.LevelWarn}
	prev := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return h
}

func durationTestConfig(t *testing.T, duration string, modes ...string) *Config {
	t.Helper()
	cfg := &Config{}
	applyConfigDefaults(cfg)
	cfg.Storage.SegmentDuration = duration
	for i, mode := range modes {
		cfg.Cameras = append(cfg.Cameras, CameraConfig{
			ID: "cam" + strings.Repeat("x", i), Name: "n", Encoding: "h264",
			Protocol: "rtsp", URL: "rtsp://localhost/test", RecordingMode: mode,
		})
	}
	return cfg
}

// TestSegmentDuration_WarnsShortWithContinuousCameras (#758): a global
// segment_duration below 60s combined with at least one continuous camera
// must emit a warning — rotation cadence is an I/O switch and short global
// values amplify metadata churn on every camera.
func TestSegmentDuration_WarnsShortWithContinuousCameras(t *testing.T) {
	h := captureWarns(t)

	cfg := durationTestConfig(t, "30s", "")
	if err := Validate(cfg); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(h.warns) == 0 {
		t.Fatal("expected a warning for 30s global duration with a continuous camera")
	}
	if !strings.Contains(h.warns[0], "segment_duration") {
		t.Errorf("warning %q does not mention segment_duration", h.warns[0])
	}
}

func TestSegmentDuration_NoWarnAtOrAbove60s(t *testing.T) {
	h := captureWarns(t)

	cfg := durationTestConfig(t, "60s", "")
	if err := Validate(cfg); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	for _, w := range h.warns {
		if strings.Contains(w, "segment_duration") {
			t.Errorf("unexpected short-duration warning at 60s: %q", w)
		}
	}
}

func TestSegmentDuration_NoWarnForAdaptiveOnly(t *testing.T) {
	h := captureWarns(t)

	cfg := durationTestConfig(t, "30s", "adaptive")
	if err := Validate(cfg); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	for _, w := range h.warns {
		if strings.Contains(w, "segment_duration") {
			t.Errorf("adaptive-only fleet must not warn about continuous churn: %q", w)
		}
	}
}

// TestCameraSegmentDurationOverride_Validated (#758): per-camera overrides
// must parse as positive durations.
func TestCameraSegmentDurationOverride_Validated(t *testing.T) {
	cfg := durationTestConfig(t, "120s", "")
	cfg.Cameras[0].SegmentDuration = "15s"
	if err := Validate(cfg); err != nil {
		t.Fatalf("valid per-camera override rejected: %v", err)
	}

	cfg = durationTestConfig(t, "120s", "")
	cfg.Cameras[0].SegmentDuration = "not-a-duration"
	err := Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "segment_duration") {
		t.Errorf("invalid per-camera override = %v, want segment_duration error", err)
	}

	cfg = durationTestConfig(t, "120s", "")
	cfg.Cameras[0].SegmentDuration = "0s"
	err = Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "segment_duration") {
		t.Errorf("zero per-camera override = %v, want segment_duration error", err)
	}
}
