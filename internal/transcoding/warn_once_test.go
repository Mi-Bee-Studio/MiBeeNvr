package transcoding

import (
	"context"
	"log/slog"
	"sync"
	"testing"
)

// The "software encoder on ARM" warnings state a STATIC fact derived from
// startup-probed capabilities; firing them per task flooded production
// journals (~2.9k lines/day on the M5 whose SoC has no mainline v4l2m2m
// encoder — verified 2026-09-16). They must emit once per process.
func TestSoftwareEncoderWarnEmittedOncePerProcess(t *testing.T) {
	resetSoftwareEncoderWarnOnce()
	h := &warnCountingHandler{}
	old := slog.Default()
	slog.SetDefault(slog.New(h))
	defer slog.SetDefault(old)

	// ARM arch with software-only caps — the M5 production shape.
	caps := softwareCaps()
	caps.Arch = "arm64"
	opts := TranscodeOptions{
		InputPath:   "/tmp/in.mp4",
		OutputPath:  "/tmp/out.mp4",
		InputCodec:  "h265",
		OutputCodec: "h264",
		Framerate:   30,
	}
	for range 3 {
		if _, err := BuildFFmpegCommand(opts, caps); err != nil {
			t.Fatalf("BuildFFmpegCommand: %v", err)
		}
	}
	if got := h.warnCount(); got != 1 {
		t.Errorf("software-encoder warn count = %d, want exactly 1 (once per process)", got)
	}
}

// Distinct warn keys (encoder/arch variants) each still get their one
// emission — the guard deduplicates by key, not globally.
func TestWarnSoftwareEncoderOnceDeduplicatesByKey(t *testing.T) {
	resetSoftwareEncoderWarnOnce()
	h := &warnCountingHandler{}
	old := slog.Default()
	slog.SetDefault(slog.New(h))
	defer slog.SetDefault(old)

	warnSoftwareEncoderOnce("libx264:arm64", "software H.264 encoding on ARM",
		"encoder", "libx264", "arch", "arm64")
	warnSoftwareEncoderOnce("libx264:arm64", "software H.264 encoding on ARM",
		"encoder", "libx264", "arch", "arm64")
	warnSoftwareEncoderOnce("libx265:arm64", "software H.265 encoding on ARM",
		"encoder", "libx265", "arch", "arm64")

	if got := h.warnCount(); got != 2 {
		t.Errorf("warn count = %d, want 2 (one per distinct key)", got)
	}
}

// warnCountingHandler counts emitted Warn-level records.
type warnCountingHandler struct {
	mu    sync.Mutex
	count int
}

func (h *warnCountingHandler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= slog.LevelWarn
}

func (h *warnCountingHandler) Handle(_ context.Context, r slog.Record) error {
	if r.Level == slog.LevelWarn {
		h.mu.Lock()
		h.count++
		h.mu.Unlock()
	}
	return nil
}

func (h *warnCountingHandler) WithAttrs(attrs []slog.Attr) slog.Handler { return h }

func (h *warnCountingHandler) WithGroup(name string) slog.Handler { return h }

func (h *warnCountingHandler) warnCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.count
}
