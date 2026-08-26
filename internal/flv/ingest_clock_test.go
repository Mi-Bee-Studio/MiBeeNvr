package flv

// Tests for the ingest-wallclock piggyback in the FLV tag StreamID field
// (#481): end-to-end live latency for the FLV egress path.

import (
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVideoFrameTag_EncodesIngestDelta(t *testing.T) {
	// Keyframe with a 1234ms ingest offset.
	tag := videoFrameTag(model.FormatH264, [][]byte{{0x65, 0x01}}, 90000, true, 1234)
	require.GreaterOrEqual(t, len(tag), 16)
	delta := int64(tag[8])<<16 | int64(tag[9])<<8 | int64(tag[10])
	assert.Equal(t, int64(1234), delta, "StreamID carries the ingest offset")

	// Unknown sentinel round-trips as 0xFFFFFF.
	tag = videoFrameTag(model.FormatH264, [][]byte{{0x65}}, 0, true, flvIngestUnknown)
	delta = int64(tag[8])<<16 | int64(tag[9])<<8 | int64(tag[10])
	assert.Equal(t, int64(0xFFFFFF), delta)
}

func TestWriteLoopBakesDeltaAndClockMs(t *testing.T) {
	m, hub := newTestManagerWithHub(t)
	require.NoError(t, m.RegisterStream("clock-cam", model.FormatH264, minimalSPS, minimalPPS, nil, hub))
	t.Cleanup(func() { m.UnregisterStream("clock-cam") })

	base := m.ClockMs("clock-cam")
	require.NotZero(t, base, "clock base set at registration")

	// hub.Broadcast stamps IngestAt at entry; the baked delta must be a
	// small non-negative offset — NOT the unknown sentinel.
	hub.Broadcast(90000, [][]byte{{0x65, 0x01}}, true)

	require.Eventually(t, func() bool {
		m.mu.RLock()
		entry := m.streams["clock-cam"]
		m.mu.RUnlock()
		if entry == nil {
			return false
		}
		entry.gopMu.RLock()
		defer entry.gopMu.RUnlock()
		for _, f := range entry.gopCache.frames {
			delta := int64(f.tag[8])<<16 | int64(f.tag[9])<<8 | int64(f.tag[10])
			if delta >= 0 && delta < 10_000 {
				return true
			}
		}
		return false
	}, 2*time.Second, 20*time.Millisecond, "tag carries a real ingest delta (not the unknown sentinel)")
}
