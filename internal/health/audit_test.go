package health

// Tests for the recording integrity auditor (#469 Phase 5).

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	"github.com/stretchr/testify/require"
)

func TestRecordingAuditor_NilSafe(t *testing.T) {
	var a *RecordingAuditor
	require.NoError(t, a.Start(context.Background()))
	require.NoError(t, a.Stop())
}

func TestRecordingAuditor_RecordsProbeError(t *testing.T) {
	bus := event.NewEventBus(16)
	m := metrics.NewMetrics()
	a := NewRecordingAuditor(bus, m)
	require.NotNil(t, a)
	require.NoError(t, a.Start(context.Background()))
	t.Cleanup(func() { _ = a.Stop() })

	// A garbage .mp4 must be classified probe_error, never panic.
	garbage := filepath.Join(t.TempDir(), "bad.mp4")
	require.NoError(t, os.WriteFile(garbage, []byte("not an mp4 at all"), 0o600))

	bus.Publish(context.Background(), event.TopicSegmentCompleted, event.SegmentCompleted{
		CameraID: "cam-audit",
		FilePath: garbage,
	})

	require.Eventually(t, func() bool {
		families, err := m.Registry.Gather()
		if err != nil {
			return false
		}
		for _, f := range families {
			if f.GetName() == "nvr_recording_audit_total" {
				for _, metric := range f.GetMetric() {
					if metric.GetCounter().GetValue() >= 1 {
						return true
					}
				}
			}
		}
		return false
	}, 3*time.Second, 20*time.Millisecond, "audit outcome must reach nvr_recording_audit_total")
}

func TestRecordingAuditor_SkipsNonMP4(t *testing.T) {
	bus := event.NewEventBus(16)
	m := metrics.NewMetrics()
	a := NewRecordingAuditor(bus, m)
	require.NotNil(t, a)
	require.NoError(t, a.Start(context.Background()))
	t.Cleanup(func() { _ = a.Stop() })

	bus.Publish(context.Background(), event.TopicSegmentCompleted, event.SegmentCompleted{
		CameraID: "cam-avi",
		FilePath: "/tmp/legacy.avi",
	})

	// Give the loop a moment; nothing must be recorded for non-MP4 outputs.
	time.Sleep(50 * time.Millisecond)
	families, err := m.Registry.Gather()
	require.NoError(t, err)
	for _, f := range families {
		require.NotEqual(t, "nvr_recording_audit_total", f.GetName(), "non-MP4 segments must not be audited")
	}
}

// TestRecordingAuditor_StopBeforeStart verifies Stop is safe when Start never
// ran (App.Stop can precede the detached Start goroutine — the deadlock that
// hung TestRunFree_DoesNotBlockOnStorageScan).
func TestRecordingAuditor_StopBeforeStart(t *testing.T) {
	bus := event.NewEventBus(16)
	m := metrics.NewMetrics()
	a := NewRecordingAuditor(bus, m)
	require.NotNil(t, a)

	done := make(chan struct{})
	go func() {
		_ = a.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop before Start must not block")
	}

	// A late Start (after Stop) must be a no-op that returns promptly and
	// doesn't panic on the closed channels.
	require.NoError(t, a.Start(context.Background()))
	_ = a.Stop() // still returns
}

// ─── Deep check (#489) ──────────────────────────────────────────────────────

// deepCheckMetricSum returns the summed deepcheck counter across all series.
func deepCheckMetricSum(t *testing.T, m *metrics.Metrics) float64 {
	t.Helper()
	families, err := m.Registry.Gather()
	if err != nil {
		return -1
	}
	var sum float64
	for _, f := range families {
		if f.GetName() != "nvr_recording_deepcheck_total" {
			continue
		}
		for _, metric := range f.GetMetric() {
			sum += metric.GetCounter().GetValue()
		}
	}
	return sum
}

func TestRecordingAuditor_DeepCheckOK(t *testing.T) {
	bus := event.NewEventBus(16)
	m := metrics.NewMetrics()
	a := NewRecordingAuditor(bus, m, WithFFmpegPath("/usr/bin/ffmpeg"))
	require.NotNil(t, a)
	// Inject a fake decoder: clean exit, no stderr.
	a.deepCheckNow = func(_ context.Context, _ string) (string, error) { return "", nil }
	// Deep check on the very first segment for a camera.
	a.lastDeepCheck["cam-dc"] = time.Now().Add(-2 * deepCheckInterval)
	require.NoError(t, a.Start(context.Background()))
	t.Cleanup(func() { _ = a.Stop() })

	bus.Publish(context.Background(), event.TopicSegmentCompleted, event.SegmentCompleted{
		CameraID: "cam-dc",
		FilePath: newValidMP4Path(t, "good.mp4"),
	})

	require.Eventually(t, func() bool { return deepCheckMetricSum(t, m) == 1 }, 3*time.Second, 20*time.Millisecond,
		"clean decode must record nvr_recording_deepcheck_total{result=ok}")
}

func TestRecordingAuditor_DeepCheckDecodeError(t *testing.T) {
	bus := event.NewEventBus(16)
	m := metrics.NewMetrics()
	a := NewRecordingAuditor(bus, m, WithFFmpegPath("/usr/bin/ffmpeg"))
	require.NotNil(t, a)
	// Two failure shapes on two cameras (avoids resetting lastDeepCheck from
	// the test goroutine, which would race the run loop): camera A's decoder
	// exits non-zero, camera B's exits 0 but prints error-level stderr. Both
	// must land in decode_error.
	shape := map[string]int{"cam-a": 1, "cam-b": 2} // 1 = exit error, 2 = stderr-only
	a.deepCheckNow = func(_ context.Context, path string) (string, error) {
		if strings.Contains(path, "a.mp4") {
			return "", errors.New("exit status 1")
		}
		return "[error] POC missing", nil
	}
	_ = shape
	require.NoError(t, a.Start(context.Background()))
	t.Cleanup(func() { _ = a.Stop() })

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.mp4"), validMP4Bytes(), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.mp4"), validMP4Bytes(), 0o600))

	bus.Publish(context.Background(), event.TopicSegmentCompleted, event.SegmentCompleted{
		CameraID: "cam-a", FilePath: filepath.Join(dir, "a.mp4"),
	})
	bus.Publish(context.Background(), event.TopicSegmentCompleted, event.SegmentCompleted{
		CameraID: "cam-b", FilePath: filepath.Join(dir, "b.mp4"),
	})

	// The second event waits out the 5s audit spacing, so allow a wide window.
	require.Eventually(t, func() bool { return deepCheckMetricSum(t, m) == 2 }, 15*time.Second, 50*time.Millisecond,
		"both exit-error and stderr-only decode failures must record decode_error")
}

// validMP4Bytes returns a tiny parseable MP4 (no video track needed — the
// deep-check tests only assert on the deepcheck metric, not the audit one).
func validMP4Bytes() []byte {
	return []byte{
		0x00, 0x00, 0x00, 0x18, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm', 0x00, 0x00, 0x02, 0x00,
		'i', 's', 'o', 'm', 'm', 'p', '4', 1,
		0x00, 0x00, 0x00, 0x08, 'f', 'r', 'e', 'e',
	}
}

func newValidMP4Path(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, validMP4Bytes(), 0o600))
	return path
}

func TestRecordingAuditor_DeepCheckRateLimited(t *testing.T) {
	bus := event.NewEventBus(16)
	m := metrics.NewMetrics()
	a := NewRecordingAuditor(bus, m, WithFFmpegPath("/usr/bin/ffmpeg"))
	require.NotNil(t, a)
	runs := 0
	a.deepCheckNow = func(_ context.Context, _ string) (string, error) {
		runs++
		return "", nil
	}
	a.lastDeepCheck["cam-dc"] = time.Now().Add(-2 * deepCheckInterval) // first event eligible
	require.NoError(t, a.Start(context.Background()))
	t.Cleanup(func() { _ = a.Stop() })

	// Burst of events within the interval: only the first may deep check.
	for i := range 4 {
		bus.Publish(context.Background(), event.TopicSegmentCompleted, event.SegmentCompleted{
			CameraID: "cam-dc",
			FilePath: newValidMP4Path(t, "good.mp4"),
		})
		_ = i
	}
	require.Eventually(t, func() bool { return deepCheckMetricSum(t, m) == 1 }, 3*time.Second, 20*time.Millisecond)
	time.Sleep(300 * time.Millisecond)
	require.Equal(t, 1, runs, "deep check must run at most once per deepCheckInterval per camera")
}

func TestRecordingAuditor_DeepCheckDisabledWithoutFFmpeg(t *testing.T) {
	bus := event.NewEventBus(16)
	m := metrics.NewMetrics()
	a := NewRecordingAuditor(bus, m) // no WithFFmpegPath → disabled
	require.NotNil(t, a)
	require.NoError(t, a.Start(context.Background()))
	t.Cleanup(func() { _ = a.Stop() })

	bus.Publish(context.Background(), event.TopicSegmentCompleted, event.SegmentCompleted{
		CameraID: "cam-dc",
		FilePath: newValidMP4Path(t, "good.mp4"),
	})
	time.Sleep(200 * time.Millisecond)
	families, err := m.Registry.Gather()
	require.NoError(t, err)
	for _, f := range families {
		require.NotEqual(t, "nvr_recording_deepcheck_total", f.GetName(),
			"deep check must not emit metrics when ffmpeg is not configured")
	}
}
