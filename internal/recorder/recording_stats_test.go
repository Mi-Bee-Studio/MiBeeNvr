package recorder

// Tests for the recording-branch snapshot exposed to the flow API (#480).

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecordingStats_ZeroBeforeRun(t *testing.T) {
	rec := NewH264Recorder(H264Config{CameraID: "stats-cam"}, nil)
	stats := rec.RecordingStats()
	assert.False(t, stats.Segmenting, "no muxer before run")
	assert.Zero(t, stats.RingLen, "no channel allocated before run")
	assert.Equal(t, DefaultRingBufCap, stats.RingCap)
	assert.Equal(t, DefaultSegmentDur.Seconds(), stats.SegmentDurS)
	assert.Zero(t, stats.RingDropsTotal)
}

func TestRecordingStats_AfterChannelAllocAndDrops(t *testing.T) {
	rec := NewH264Recorder(H264Config{CameraID: "stats-cam"}, nil)
	// Simulate run() start: allocate the ring + publish the pointer.
	ch := make(chan framePacket, rec.cfg.RingBufCap)
	rec.frameCh = ch
	rec.frameChPtr.Store(&ch)
	rec.dropped.Store(7)
	ch <- framePacket{data: []byte{0x65}}

	stats := rec.RecordingStats()
	require.Equal(t, 1, stats.RingLen, "one queued packet")
	require.Equal(t, rec.cfg.RingBufCap, stats.RingCap)
	require.Equal(t, int64(7), stats.RingDropsTotal)

	// Ring reads are race-free against a run() restart: a fresh pointer
	// replaces the old channel and the snapshot follows it.
	ch2 := make(chan framePacket, 2)
	rec.frameChPtr.Store(&ch2)
	stats = rec.RecordingStats()
	assert.Equal(t, 0, stats.RingLen, "snapshot follows the latest ring")
}
