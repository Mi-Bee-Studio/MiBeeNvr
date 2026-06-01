package recorder

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func helperPTSResult(t *testing.T, result ptsCheckResult) {
	t.Helper()
	// noop helper — just ensures consistent t.Helper() usage
	_ = result
}

func TestCheckPTSMonotonicity_FirstFrame(t *testing.T) {
	t.Helper()
	result := checkPTSMonotonicity(0, 90000)
	require.True(t, result.IsFirst, "first frame should have IsFirst=true")
	require.Equal(t, ptsAnomalyNone, result.Anomaly)
}

func TestCheckPTSMonotonicity_NormalProgression(t *testing.T) {
	t.Helper()
	// 3600 ticks = 40ms at 90kHz, normal frame interval at 25fps
	result := checkPTSMonotonicity(90000, 93600)
	require.Equal(t, ptsAnomalyNone, result.Anomaly, "normal forward progression should not be anomalous")
	require.False(t, result.IsFirst)
}

func TestCheckPTSMonotonicity_SmallBackwardJitter(t *testing.T) {
	t.Helper()
	// Small backward jitter within 2 frame durations should be tolerated
	// 1 frame duration = 3600 ticks at 25fps; we allow up to 2 = 7200 ticks backward
	result := checkPTSMonotonicity(10000, 9000)
	require.Equal(t, ptsAnomalyNone, result.Anomaly, "small backward jitter within tolerance")
}

func TestCheckPTSMonotonicity_BackwardJump(t *testing.T) {
	t.Helper()
	// Large backward jump (more than 2 frame durations at 25fps = 7200 ticks)
	result := checkPTSMonotonicity(100000, 50000)
	require.Equal(t, ptsAnomalyBackwardJump, result.Anomaly, "large backward jump should be detected")
	require.Equal(t, int64(-50000), result.Delta)
	require.Equal(t, int64(100000), result.LastPTS)
	require.Equal(t, int64(50000), result.CurPTS)
	helperPTSResult(t, result)
}

func TestCheckPTSMonotonicity_ExcessiveForwardJump(t *testing.T) {
	t.Helper()
	// 10s at 90kHz = 900000 ticks; jump beyond that is anomalous
	result := checkPTSMonotonicity(100000, 2000000)
	require.Equal(t, ptsAnomalyExcessiveForwardJump, result.Anomaly, "excessive forward jump should be detected")
	require.Equal(t, int64(1900000), result.Delta)
	helperPTSResult(t, result)
}

func TestCheckPTSMonotonicity_Exactly10sForward(t *testing.T) {
	t.Helper()
	// Exactly 10s forward = exactly at the threshold — should NOT be anomalous
	result := checkPTSMonotonicity(100000, 100000+ptsMaxForwardJump)
	require.Equal(t, ptsAnomalyNone, result.Anomaly, "exactly at 10s threshold should not be anomalous")
}

func TestCheckPTSMonotonicity_SlightlyOver10sForward(t *testing.T) {
	t.Helper()
	// Just over 10s forward — should be anomalous
	result := checkPTSMonotonicity(100000, 100000+ptsMaxForwardJump+1)
	require.Equal(t, ptsAnomalyExcessiveForwardJump, result.Anomaly, "just over 10s should be anomalous")
}

func TestCheckPTSMonotonicity_WrapAround(t *testing.T) {
	t.Helper()
	// Simulate RTP timestamp wrap: lastPTS near 2^32, curPTS near 0
	maxTS := int64(1<<32) - 100
	curPTS := int64(50000)
	result := checkPTSMonotonicity(maxTS, curPTS)
	require.Equal(t, ptsAnomalyWrapAround, result.Anomaly, "wrap-around should be detected")
	require.True(t, result.Delta < 0, "delta should be negative for wrap-around")
	helperPTSResult(t, result)
}

func TestCheckPTSMonotonicity_WrapAround_SmallWrap(t *testing.T) {
	t.Helper()
	// Small wrap-around that looks like a genuine backward jump
	// lastPTS is not near upper boundary, so this should be backward jump, not wrap
	result := checkPTSMonotonicity(100000, 50000)
	require.Equal(t, ptsAnomalyBackwardJump, result.Anomaly, "should be backward jump, not wrap-around")
}

func TestCheckPTSMonotonicity_WrapAround_NoWrapWhenNotNearBoundary(t *testing.T) {
	t.Helper()
	// lastPTS in the middle of range, curPTS slightly less — should be backward jump
	mid := int64(1<<31) // half of uint32 range
	result := checkPTSMonotonicity(mid, mid-100000)
	require.Equal(t, ptsAnomalyBackwardJump, result.Anomaly, "not near boundary should be backward jump")
}

func TestCheckPTSMonotonicity_WrapAround_UnreasonablyLargeWrap(t *testing.T) {
	t.Helper()
	// lastPTS near max, curPTS near max/2 — wrapped delta would be huge (>10s)
	// This should still be detected as wrap-around since lastPTS > 2^31
	maxTS := int64(1<<32) - 100
	curPTS := int64(1<<31) // wrapped delta = 1<<31 + 100 > ptsMaxForwardJump
	result := checkPTSMonotonicity(maxTS, curPTS)
	// Wrapped delta exceeds 10s, so it's NOT a valid wrap-around → should be backward jump
	require.Equal(t, ptsAnomalyBackwardJump, result.Anomaly, "unreasonably large wrap should be backward jump")
}

func TestPTSAnomalyKind_String(t *testing.T) {
	t.Helper()
	require.Equal(t, "none", ptsAnomalyNone.String())
	require.Equal(t, "backward_jump", ptsAnomalyBackwardJump.String())
	require.Equal(t, "excessive_forward_jump", ptsAnomalyExcessiveForwardJump.String())
	require.Equal(t, "wrap_around", ptsAnomalyWrapAround.String())
}

func TestCheckPTSMonotonicity_ZeroDelta(t *testing.T) {
	t.Helper()
	// Duplicate timestamp — should be fine (delta=0, within tolerance)
	result := checkPTSMonotonicity(50000, 50000)
	require.Equal(t, ptsAnomalyNone, result.Anomaly, "zero delta should not be anomalous")
}

func TestCheckPTSMonotonicity_Normal30fps(t *testing.T) {
	t.Helper()
	// Normal 30fps: 90000/30 = 3000 ticks per frame
	for i := int64(1); i <= 100; i++ {
		last := i * 3000
		cur := (i + 1) * 3000
		result := checkPTSMonotonicity(last, cur)
		require.Equal(t, ptsAnomalyNone, result.Anomaly, "normal 30fps progression at frame %d", i)
	}
}
