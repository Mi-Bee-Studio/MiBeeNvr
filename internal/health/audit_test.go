package health

// Tests for the recording integrity auditor (#469 Phase 5).

import (
	"context"
	"os"
	"path/filepath"
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
