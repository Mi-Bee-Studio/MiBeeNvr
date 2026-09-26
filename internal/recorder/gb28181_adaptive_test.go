package recorder

import (
	"bytes"
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/streamhub"
	"github.com/stretchr/testify/require"
)

// newGBAdaptiveRecorder builds a GB28181Recorder with the adaptive gate armed
// and short real-time thresholds, so the state machine transitions inside a
// test without sleeps beyond a few hundred milliseconds. Same wiring as the
// camera manager: real temp-dir storage, hub attached, INVITE accepted.
func newGBAdaptiveRecorder(t *testing.T, cameraID string, adaptive *AdaptiveConfig) (*GB28181Recorder, *storage.Manager) {
	t.Helper()
	store, err := storage.NewManager(t.TempDir())
	require.NoError(t, err)
	rec := NewGB28181Recorder(GB28181Config{
		CameraID:      cameraID,
		Encoding:      "h264",
		SegmentDur:    10 * time.Minute,
		Store:         store,
		RecordEnabled: true,
		Adaptive:      adaptive,
	}, nil)
	rec.Hub = streamhub.New()
	rec.Hub.SetCameraID(cameraID)
	require.NoError(t, rec.Start(context.Background()))
	rec.OnInvite()
	return rec, store
}

func gbAdaFrameCount(rec *GB28181Recorder) int {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return rec.frameCount
}

// feedCalmAUs pushes n small uniform P-frames while pacing the real-time
// clock, so the gate's wall-clock calm threshold can expire mid-feed. The
// RTP timestamps advance at 30fps regardless.
func feedCalmAUs(t *testing.T, rec *GB28181Recorder, n int, startTicks int64) {
	t.Helper()
	for i := range n {
		rec.WriteNALU([][]byte{[]byte(gbAdaPFrame)}, startTicks+int64(i)*3000, false)
		time.Sleep(10 * time.Millisecond)
	}
}

const (
	gbAdaSPS    = "\x67\x42\x80\x0a"
	gbAdaPPS    = "\x68\xce\x3c\x80"
	gbAdaIDR    = "\x65\x88\x80\x00"
	gbAdaPFrame = "\x41\x9a\x24\x80"
)

// enterGB28181Sparse drives a fresh recorder into sparse mode: the segment
// opens on the first IDR, then sustained calm over the real-time threshold
// drops the gate into timelapse.
func enterGB28181Sparse(t *testing.T, rec *GB28181Recorder) {
	t.Helper()
	rec.WriteNALU([][]byte{[]byte(gbAdaSPS), []byte(gbAdaPPS), []byte(gbAdaIDR)}, 90000, true)
	require.Equal(t, 1, gbAdaFrameCount(rec), "IDR must open the segment")
	feedCalmAUs(t, rec, 40, 90000+3000)
	require.True(t, rec.gate.Timelapse(), "sustained calm must enter sparse mode")
}

// TestGB28181Adaptive_SparseDropsPFrames: in sparse mode P-frames stay off
// disk while a sparse keyframe after a full interval still lands — and the
// hub fan-out keeps flowing for every AU regardless of write density.
func TestGB28181Adaptive_SparseDropsPFrames(t *testing.T) {
	cfg := AdaptiveConfig{
		CalmThreshold:     150 * time.Millisecond,
		TimelapseInterval: 100 * time.Millisecond,
		SpikeFactor:       3.0,
		MaxGOPBuffer:      16 << 20,
		AutoNoiseFloor:    false,
	}
	rec, _ := newGBAdaptiveRecorder(t, "gb-adaptive", &cfg)
	defer func() { _ = rec.Stop() }()

	require.True(t, rec.AdaptiveArmed(), "gate must be armed when cfg.Adaptive is set")

	var broadcasts atomic.Int64
	require.NoError(t, rec.Hub.Subscribe("counter", func(pts int64, au [][]byte) {
		broadcasts.Add(1)
	}))
	defer rec.Hub.Unsubscribe("counter")

	enterGB28181Sparse(t, rec)

	written := gbAdaFrameCount(rec)
	for i := range 5 {
		rec.WriteNALU([][]byte{[]byte(gbAdaPFrame)}, 90000+3000+40*3000+int64(i)*3000, false)
	}
	require.Equal(t, written, gbAdaFrameCount(rec), "sparse P-frames must not reach the muxer")

	// Sparse keyframe after a full interval is written.
	time.Sleep(100 * time.Millisecond)
	rec.WriteNALU([][]byte{[]byte(gbAdaSPS), []byte(gbAdaPPS), []byte(gbAdaIDR)}, 90000+46*3000, true)
	require.Equal(t, written+1, gbAdaFrameCount(rec), "sparse keyframe must be written")

	// Live fan-out is never gated: skipped AUs still reached the hub.
	require.Eventually(t, func() bool { return broadcasts.Load() >= 45 }, 2*time.Second, 10*time.Millisecond)
}

// TestGB28181Adaptive_SpikeFlushesAndResumes: a scene-cut-scale P-frame exits
// sparse mode, the retained GOP is flushed into the segment, and full-rate
// writing resumes.
func TestGB28181Adaptive_SpikeFlushesAndResumes(t *testing.T) {
	cfg := AdaptiveConfig{
		CalmThreshold:     150 * time.Millisecond,
		TimelapseInterval: 100 * time.Millisecond,
		SpikeFactor:       3.0,
		MaxGOPBuffer:      16 << 20,
		AutoNoiseFloor:    false,
	}
	rec, _ := newGBAdaptiveRecorder(t, "gb-adaptive", &cfg)
	defer func() { _ = rec.Stop() }()

	enterGB28181Sparse(t, rec)

	// Scene-cut-scale frame: a single major spike must exit sparse mode.
	big := append([]byte{0x41}, bytes.Repeat([]byte{0x9A}, 100*1024)...)
	tick := int64(90000 + 3000 + 43*3000)
	rec.WriteNALU([][]byte{big}, tick, false)
	require.False(t, rec.gate.Timelapse(), "major spike must exit sparse mode")

	afterSpike := gbAdaFrameCount(rec)
	require.Greater(t, afterSpike, 2, "flush + spike frame must be written")

	// Full-rate resumed: the next ordinary P-frame lands too.
	rec.WriteNALU([][]byte{[]byte(gbAdaPFrame)}, tick+3000, false)
	require.Equal(t, afterSpike+1, gbAdaFrameCount(rec), "P-frames must be written again after the exit")
}

// TestGB28181Adaptive_UnarmedRecordsContinuously: without cfg.Adaptive the
// recorder behaves exactly as before — every frame is written no matter how
// long the scene stays calm.
func TestGB28181Adaptive_UnarmedRecordsContinuously(t *testing.T) {
	rec, _ := newGBAdaptiveRecorder(t, "gb-plain", nil)
	defer func() { _ = rec.Stop() }()

	require.False(t, rec.AdaptiveArmed(), "gate must stay unarmed without cfg.Adaptive")

	rec.WriteNALU([][]byte{[]byte(gbAdaSPS), []byte(gbAdaPPS), []byte(gbAdaIDR)}, 90000, true)
	feedCalmAUs(t, rec, 40, 90000+3000)
	require.Equal(t, 41, gbAdaFrameCount(rec), "unarmed recorder must write every frame")
}
