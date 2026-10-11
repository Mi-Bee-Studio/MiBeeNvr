package recorder

// Segment rollover must happen at an IDR so consecutive segments join without
// a gap. Closing on the P-frame that crossed SegmentDur left the next segment
// waiting for the following IDR, and every frame in between was dropped.

import (
	"bytes"
	"sort"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/merge"
	"github.com/stretchr/testify/require"
)

// runRollover feeds writeFrames a synthetic stream (one frame every 20ms, an
// IDR every gop frames) and returns the finalized segments in order.
func runRollover(t *testing.T, cam string, segDur time.Duration, gop, frames int) []*merge.SegmentInfo {
	t.Helper()
	mgr := newTestManager(t)
	rec := NewH264Recorder(H264Config{
		CameraID:   cam,
		RTSPURL:    "rtsp://ignored",
		SegmentDur: segDur,
		RingBufCap: 512,
	}, mgr)
	b := rec.baseRecorder
	b.frameCh = make(chan framePacket, 512)

	send := func(nal []byte, at time.Time) {
		b.frameCh <- framePacket{data: append(append([]byte{}, 0, 0, 0, 1), nal...), at: at}
	}
	t0 := time.Now()
	send(testSPS, t0)
	send(testPPS, t0)
	for i := range frames {
		var nal []byte
		if i%gop == 0 {
			nal = append(append([]byte{}, testIDR...), bytes.Repeat([]byte{0x11}, 2000)...)
		} else {
			nal = append([]byte{0x41}, bytes.Repeat([]byte{0x22}, 400)...)
		}
		nal[len(nal)-1] = byte(i)
		send(nal, t0.Add(time.Duration(i)*20*time.Millisecond))
	}

	done := make(chan struct{})
	go b.writeFrames(done)
	for len(b.frameCh) > 0 {
		time.Sleep(time.Millisecond)
	}
	close(b.frameCh)
	<-done
	b.closeCurrentSegment()

	files, err := mgr.ListFiles(cam)
	require.NoError(t, err)
	sort.Strings(files)
	segs := make([]*merge.SegmentInfo, 0, len(files))
	for _, f := range files {
		seg, err := merge.ParseSegment(f)
		require.NoError(t, err)
		segs = append(segs, seg)
	}
	return segs
}

func TestSegmentRollover_AtIDRDropsNoFrames(t *testing.T) {
	// 100ms target, IDR every 7 frames (140ms): the target is crossed mid-GOP
	// every time, so the old rollover lost 2 of every 7 frames.
	const gop, frames = 7, 70
	segs := runRollover(t, "rollover-idr", 100*time.Millisecond, gop, frames)

	require.Len(t, segs, frames/gop, "one segment per GOP")
	total := 0
	for i, seg := range segs {
		require.True(t, seg.Samples[0].IsKeyFrame, "segment %d must start at an IDR", i)
		require.Equal(t, gop, seg.SampleCount, "segment %d must hold its whole GOP", i)
		total += seg.SampleCount
	}
	require.Equal(t, frames, total, "every frame must land in a segment")
}

func TestSegmentRollover_FallbackWithoutIDR(t *testing.T) {
	// A single IDR followed by P-frames only: rollover falls back to
	// 2×SegmentDur so the segment does not grow without bound.
	const frames = 30 // 600ms of stream
	segs := runRollover(t, "rollover-noidr", 100*time.Millisecond, frames, frames)

	require.Len(t, segs, 1, "frames after the fallback close wait for an IDR")
	require.Equal(t, 11, segs[0].SampleCount, "closed on the frame at 2×SegmentDur (200ms)")
}
