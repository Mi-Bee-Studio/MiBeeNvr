package relay

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Unit tests — DriftMonitor
// ---------------------------------------------------------------------------

func TestDriftMonitor_BelowThreshold(t *testing.T) {
	m := NewDriftMonitor()

	// Video at 0, audio at 45000 ticks (0.5s) → drift = 0.5s < 1s threshold.
	m.RecordVideo(0)
	m.RecordAudio(45000)

	require.InDelta(t, 500, m.DriftMs(), 1, "drift should be ~500ms")
	require.False(t, m.ShouldReconnect(), "below-threshold drift must not reconnect")
}

func TestDriftMonitor_AboveThresholdSustained(t *testing.T) {
	m := NewDriftMonitor()
	m.sustainedDuration = time.Millisecond // speed up test; do not wait 5s

	// Video at 150000, audio at 0 → drift = 150000 ticks ≈ 1667ms > 1000ms.
	m.RecordVideo(150000)
	m.RecordAudio(0)

	// Immediately after crossing threshold, ShouldReconnect should be false.
	require.False(t, m.ShouldReconnect(),
		"must not reconnect immediately after drift starts")

	// Wait for sustained duration.
	time.Sleep(2 * time.Millisecond)

	require.True(t, m.ShouldReconnect(),
		"must reconnect after sustained drift")
}

func TestDriftMonitor_Reset(t *testing.T) {
	m := NewDriftMonitor()
	m.sustainedDuration = time.Millisecond

	// Establish drift.
	m.RecordVideo(150000)
	m.RecordAudio(0)
	time.Sleep(2 * time.Millisecond)
	require.True(t, m.ShouldReconnect(), "drift should be active before reset")

	// Reset and verify all state is cleared.
	m.Reset()
	require.False(t, m.ShouldReconnect(), "reset must clear reconnect state")
	require.InDelta(t, 0, m.DriftMs(), 0.5, "reset must clear drift ms")
	require.InDelta(t, 0, m.HighWaterMs(), 0.5, "reset must clear high-water mark")
}

func TestDriftMonitor_HighWaterMs(t *testing.T) {
	m := NewDriftMonitor()

	// Initial: no data.
	require.InDelta(t, 0, m.HighWaterMs(), 0.5, "initial high water must be 0")

	// First: 0.5s drift.
	m.RecordVideo(0)
	m.RecordAudio(45000)

	require.InDelta(t, 500, m.HighWaterMs(), 1, "high water must track ~500ms")
	require.InDelta(t, 500, m.DriftMs(), 1, "current drift must be ~500ms")

	// Increase drift to ~1.11s.
	m.RecordVideo(100000)
	m.RecordAudio(0)

	require.InDelta(t, 1111, m.HighWaterMs(), 2, "high water must track ~1111ms")

	// Decrease drift — high water must NOT decrease.
	m.RecordVideo(10000)
	m.RecordAudio(0)

	require.InDelta(t, 1111, m.HighWaterMs(), 2,
		"high water must NOT decrease when drift shrinks")
	require.InDelta(t, 111, m.DriftMs(), 1, "current drift must reflect new value")
}

func TestDriftMonitor_NoAudio(t *testing.T) {
	m := NewDriftMonitor()

	// Only video recorded — no audio PTS yet (no-audio camera).
	m.RecordVideo(100000)

	require.InDelta(t, 0, m.DriftMs(), 0.5,
		"drift must be 0 when audio PTS is missing")
	require.False(t, m.ShouldReconnect(),
		"must not reconnect when audio is missing")
}

func TestDriftMonitor_NoVideo(t *testing.T) {
	m := NewDriftMonitor()

	// Only audio recorded — no video PTS yet.
	m.RecordAudio(100000)

	require.InDelta(t, 0, m.DriftMs(), 0.5,
		"drift must be 0 when video PTS is missing")
	require.False(t, m.ShouldReconnect(),
		"must not reconnect when video is missing")
}

func TestDriftMonitor_DriftRecovers(t *testing.T) {
	m := NewDriftMonitor()
	m.sustainedDuration = 2 * time.Millisecond

	// Establish drift above threshold.
	m.RecordVideo(150000)
	m.RecordAudio(0)
	time.Sleep(3 * time.Millisecond)
	require.True(t, m.ShouldReconnect(), "drift must be active")

	// Drift recovers (below threshold).
	m.RecordVideo(45000)
	m.RecordAudio(0)

	require.InDelta(t, 500, m.DriftMs(), 1, "drift should be ~500ms (below threshold)")
	require.False(t, m.ShouldReconnect(),
		"drift recovery must clear reconnect state")
}

func TestDriftMonitor_DriftMsValues(t *testing.T) {
	tests := []struct {
		name   string
		video  int64
		audio  int64
		wantMs float64
	}{
		{"no drift", 100000, 100000, 0},
		{"half second", 45000, 0, 500},
		{"one second", 90000, 0, 1000},
		{"two seconds", 0, 180000, 2000},
		{"negative drift (abs)", 0, 50000, 555.6},
		{"large drift", 1000000, 0, 11111.1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m2 := NewDriftMonitor()
			m2.RecordVideo(tt.video)
			m2.RecordAudio(tt.audio)
			require.InDelta(t, tt.wantMs, m2.DriftMs(), 1, "DriftMs mismatch")
		})
	}
}

func TestDriftMonitor_DriftReconnectError(t *testing.T) {
	err := DriftReconnectError
	require.Error(t, err, "DriftReconnectError must be a non-nil error")
	require.Contains(t, err.Error(), "drift",
		"error message must mention drift")
	require.Contains(t, err.Error(), "reconnect",
		"error message must mention reconnect")
}

func TestDriftMonitor_StartLogging(t *testing.T) {
	// Verify StartLogging is non-blocking and the goroutine exits on cancel.
	m := NewDriftMonitor()
	ctx, cancel := context.WithCancel(context.Background())

	m.StartLogging(ctx) // must return immediately (not block)

	// Give the goroutine a moment to start.
	time.Sleep(10 * time.Millisecond)

	// Cancel and verify no panic/block.
	cancel()
	time.Sleep(10 * time.Millisecond) // allow goroutine to exit
}

// ---------------------------------------------------------------------------
// Integration test — combined video+audio callback pattern
// ---------------------------------------------------------------------------

func TestDriftMonitor_IntegrationCallbackPattern(t *testing.T) {
	m := NewDriftMonitor()
	m.sustainedDuration = 5 * time.Millisecond

	// Simulate interleaved video and audio callbacks with increasing PTS.
	// Real scenario: at 30fps each callback fires ~33ms apart.
	// Audio PTS lags video PTS by 120000 ticks ≈ 1.33s.
	for i := 0; i < 10; i++ {
		videoPTS := int64(100000 + i*3000) // monotonic video
		audioPTS := int64(0 + i*3000)      // audio starts at 0, same rate

		m.RecordVideo(videoPTS)
		if m.ShouldReconnect() {
			t.Fatal("must not reconnect during sustained period")
		}

		m.RecordAudio(audioPTS)
		if m.ShouldReconnect() {
			t.Fatal("must not reconnect during sustained period")
		}
	}

	// Drift should be ~100,000 ticks ≈ 1111ms (above 1s threshold).
	drift := m.DriftMs()
	require.InDelta(t, 1111, drift, 20, "drift should be ~1111ms")

	// Wait for sustained duration.
	time.Sleep(7 * time.Millisecond)

	// After sustained window, ShouldReconnect must be true.
	require.True(t, m.ShouldReconnect(),
		"must reconnect after sustained drift in integration pattern")

	// Verify high water is tracked.
	hw := m.HighWaterMs()
	require.Greater(t, hw, 900.0, "high water must reflect the drift")
}
