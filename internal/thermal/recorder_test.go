package thermal

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func f(v float64) *float64 { return &v }

func TestMinuteRecorder_RollsAndFoldsExtremes(t *testing.T) {
	r := NewMinuteRecorder()
	base := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)

	// First reading opens the bucket; same-minute readings fold extremes.
	require.Nil(t, r.Observe(base, Reading{CameraID: "cam", Timestamp: base, MaxC: f(30.1), MinC: f(28.0)}))
	require.Nil(t, r.Observe(base, Reading{CameraID: "cam", Timestamp: base.Add(20 * time.Second), MaxC: f(31.5), MinC: f(27.4)}))
	require.Nil(t, r.Observe(base, Reading{CameraID: "cam", Timestamp: base.Add(40 * time.Second), MaxC: f(29.0), MinC: f(27.9)}))

	// A reading in the NEXT minute closes the first as a sample with the
	// folded extremes (max of maxes, min of mins).
	s := r.Observe(base.Add(70*time.Second), Reading{CameraID: "cam", Timestamp: base.Add(70 * time.Second), MaxC: f(28.2), MinC: f(26.0)})
	require.NotNil(t, s)
	require.Equal(t, "cam", s.CameraID)
	require.Equal(t, base, s.Minute)
	require.InDelta(t, 31.5, *s.MaxC, 1e-9)
	require.InDelta(t, 27.4, *s.MinC, 1e-9)

	// Straggler from the closed minute is dropped, not folded.
	require.Nil(t, r.Observe(base.Add(75*time.Second), Reading{CameraID: "cam", Timestamp: base.Add(30 * time.Second), MaxC: f(99)}))
}

func TestMinuteRecorder_DueFlushesAgedBucket(t *testing.T) {
	r := NewMinuteRecorder()
	base := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	require.Nil(t, r.Observe(base, Reading{CameraID: "cam", Timestamp: base, MaxC: f(30), MinC: f(28)}))

	// Before the grace window: nothing due.
	require.Empty(t, r.Due(base.Add(time.Minute+5*time.Second)))

	// After the grace window the bucket flushes even with no newer reading.
	out := r.Due(base.Add(time.Minute + 15*time.Second))
	require.Len(t, out, 1)
	require.Equal(t, base, out[0].Minute)
	require.Empty(t, r.Due(base.Add(time.Minute+20*time.Second)), "bucket consumed")
}

func TestMinuteRecorder_SpotOnlyAndNilHandling(t *testing.T) {
	r := NewMinuteRecorder()
	base := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)

	// Spot-only frames (no extremes) never produce a sample — the spot-only
	// minute closes empty when the next minute's reading arrives.
	require.Nil(t, r.Observe(base, Reading{CameraID: "cam", Timestamp: base, Spots: []SpotReading{{Name: "n", TempC: 30}}}))
	require.Nil(t, r.Observe(base.Add(90*time.Second), Reading{CameraID: "cam", Timestamp: base.Add(90 * time.Second)}))

	// A max-only reading opens a real bucket; it closes when the minute
	// after it arrives. max-only rows still chart (min stays nil).
	require.Nil(t, r.Observe(base.Add(3*time.Minute), Reading{CameraID: "cam", Timestamp: base.Add(2 * time.Minute), MaxC: f(33.3)}))
	s := r.Observe(base.Add(4*time.Minute), Reading{CameraID: "cam", Timestamp: base.Add(3 * time.Minute), MaxC: f(1)})
	require.NotNil(t, s)
	require.Equal(t, base.Add(2*time.Minute), s.Minute)
	require.NotNil(t, s.MaxC)
	require.InDelta(t, 33.3, *s.MaxC, 1e-9)
	require.Nil(t, s.MinC)
}

func TestMinuteRecorder_ZeroTimestampFallsBackToNow(t *testing.T) {
	r := NewMinuteRecorder()
	now := time.Date(2026, 11, 6, 12, 34, 56, 0, time.UTC)
	require.Nil(t, r.Observe(now, Reading{CameraID: "cam"})) // zero ts, no data
	out := r.Due(now.Add(2 * time.Minute))
	require.Empty(t, out, "no extremes — nothing to record")
}

func TestMinuteRecorder_PerCameraIsolation(t *testing.T) {
	r := NewMinuteRecorder()
	base := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	require.Nil(t, r.Observe(base, Reading{CameraID: "a", Timestamp: base, MaxC: f(30), MinC: f(20)}))
	require.Nil(t, r.Observe(base, Reading{CameraID: "b", Timestamp: base, MaxC: f(10), MinC: f(5)}))
	out := r.Due(base.Add(2 * time.Minute))
	require.Len(t, out, 2)
	byCam := map[string]MinuteSample{}
	for _, s := range out {
		byCam[s.CameraID] = s
	}
	require.InDelta(t, 30, *byCam["a"].MaxC, 1e-9)
	require.InDelta(t, 10, *byCam["b"].MaxC, 1e-9)
}
