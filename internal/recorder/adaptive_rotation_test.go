package recorder

// Regression tests for issue #498 (original-segment POC damage) and #506
// (timestamp collapse): writeFrames driven end-to-end with SYNTHETIC arrival
// timestamps — no real-time pacing needed since the #506 arrival-time
// refactor, which also makes burst drains keep real spacing.

import (
	"bytes"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/merge"
	"github.com/stretchr/testify/require"
)

func TestAdaptiveTracker_ClearWrittenResetsRingFlags(t *testing.T) {
	cfg := DefaultAdaptiveConfig()
	tr := newAdaptiveTracker(cfg, "cam", testAdaptiveLogger())

	idr := bytes.Repeat([]byte{0x65}, 30000)
	tr.observe(idr, true, time.Now())
	for i := range 3 {
		buf := make([]byte, 800)
		buf[0] = 0x41
		tr.observe(buf, false, time.Now().Add(time.Duration(i+1)*50*time.Millisecond))
	}
	for i := range tr.gop {
		tr.gop[i].written = true
	}

	tr.clearWritten()

	for i, f := range tr.gop {
		if f.written {
			t.Fatalf("frame %d still marked written after clearWritten", i)
		}
	}
	if len(tr.gop) != 4 {
		t.Fatalf("clearWritten must not drop ring frames, got %d", len(tr.gop))
	}
}

func TestAdaptiveTracker_TakeGOPWithOnlyTriggerRingReturnsNil(t *testing.T) {
	cfg := DefaultAdaptiveConfig()
	tr := newAdaptiveTracker(cfg, "cam", testAdaptiveLogger())

	idr := bytes.Repeat([]byte{0x65}, 30000)
	tr.mode = adaptiveTimelapse
	_, flush := tr.observe(idr, true, time.Now())
	if flush != nil {
		t.Fatalf("ring of only the trigger frame must not flush, got %d frames", len(flush))
	}
}

func TestAudioRing_ClearWrittenResetsFlags(t *testing.T) {
	r := newAudioRing(3 * time.Second)
	now := time.Now()
	r.append(true, []byte{1, 2}, 250*time.Microsecond, now)
	r.markWritten()
	require.True(t, r.drain()[0].Written)

	r.append(true, []byte{3, 4}, 250*time.Microsecond, now.Add(time.Millisecond))
	r.markWritten()
	r.clearWritten()
	for i, s := range r.drain() {
		require.False(t, s.Written, "sample %d must be unwritten after clearWritten", i)
	}
}

// feedStudio drives writeFrames through the exact field sequence that
// produced corrupted originals: normal-rate writing → segment rotation
// (stale written flags) → timelapse entry → spike exit whose flush lands in
// a FRESH segment containing the complete retained GOP, trigger written once.
func TestH264Adaptive_TLExitFlushAfterRotation(t *testing.T) {
	mgr := newTestManager(t)
	const cam = "h264-adaptive-poc"
	rec := NewH264Recorder(H264Config{
		CameraID:   cam,
		RTSPURL:    "rtsp://ignored",
		SegmentDur: 200 * time.Millisecond,
		RingBufCap: 256,
		Adaptive: &AdaptiveConfig{
			CalmThreshold:     400 * time.Millisecond,
			TimelapseInterval: time.Hour,
			SpikeFactor:       3.0,
			MaxGOPBuffer:      8 << 20,
		},
	}, mgr)
	b := rec.baseRecorder
	b.frameCh = make(chan framePacket, 256)
	b.resetAdaptive()

	done := make(chan struct{})
	go b.writeFrames(done)
	defer func() {
		close(b.frameCh)
		<-done
	}()

	send := func(nal []byte, at time.Time) {
		b.frameCh <- framePacket{data: append(append([]byte{}, 0, 0, 0, 1), nal...), at: at}
	}

	// Synthetic arrival axis: 20ms per frame.
	t0 := time.Now()
	send(testSPS, t0)
	send(testPPS, t0)
	idr := append(append([]byte{}, testIDR...), bytes.Repeat([]byte{0x11}, 20000)...)
	send(idr, t0)

	const totalPs = 35
	for i := range totalPs {
		p := append([]byte{0x41}, bytes.Repeat([]byte{0x22}, 799)...)
		p[len(p)-2] = byte(i >> 8)
		p[len(p)-1] = byte(i)
		send(p, t0.Add(time.Duration(i+1)*20*time.Millisecond))
	}

	spike := append([]byte{0x41}, bytes.Repeat([]byte{0x33}, 299999)...)
	send(spike, t0.Add(time.Duration(totalPs+1)*20*time.Millisecond))

	require.Eventually(t, func() bool { return len(b.frameCh) == 0 }, 5*time.Second, 5*time.Millisecond)
	time.Sleep(50 * time.Millisecond)

	files, err := mgr.ListFiles(cam)
	require.NoError(t, err)
	require.Len(t, files, 2, "rotation + exit flush must produce exactly 2 segments")
	sort.Strings(files)

	seg1, err := merge.ParseSegment(files[0])
	require.NoError(t, err)
	require.GreaterOrEqual(t, seg1.SampleCount, 3, "segment 1 must hold the IDR plus pre-rotation P-frames")

	seg2, err := merge.ParseSegment(files[1])
	require.NoError(t, err)
	require.True(t, seg2.Samples[0].IsKeyFrame, "flush segment must start at the ring's IDR anchor")
	require.Equal(t, 1+totalPs+1, seg2.SampleCount,
		"fresh-segment flush must contain IDR + all %d retained P-frames + trigger", totalPs)

	data, err := os.ReadFile(files[1])
	require.NoError(t, err)
	spikeTail := spike[len(spike)-16:]
	spikeCount := 0
	var prev []byte
	for i, s := range seg2.Samples {
		sample := data[s.Offset : s.Offset+int64(s.Size)]
		if bytes.Contains(sample, spikeTail) {
			spikeCount++
		}
		if i > 0 && bytes.Equal(prev, sample) {
			t.Fatalf("segments %d/%d are identical — a frame was written twice", i-1, i)
		}
		prev = sample
	}
	require.Equal(t, 1, spikeCount, "trigger frame must be on disk exactly once")
}

// TestH264Adaptive_BurstDrainKeepsArrivalSpacing (#506): a writer stall
// back-pressures the channel; the drained burst must keep its ARRIVAL spacing
// in the output timeline instead of collapsing onto the drain millisecond.
func TestH264Adaptive_BurstDrainKeepsArrivalSpacing(t *testing.T) {
	mgr := newTestManager(t)
	const cam = "h264-adaptive-burst"
	rec := NewH264Recorder(H264Config{
		CameraID:   cam,
		RTSPURL:    "rtsp://ignored",
		SegmentDur: time.Hour,
		RingBufCap: 512,
	}, mgr)
	b := rec.baseRecorder
	b.frameCh = make(chan framePacket, 512)

	// The writer is NOT running: enqueue 60 IDR-anchored frames whose arrival
	// times span 1.2s (20ms apart) — the stalled-writer backlog.
	t0 := time.Now()
	b.frameCh <- framePacket{data: append(append([]byte{}, 0, 0, 0, 1), testSPS...), at: t0}
	b.frameCh <- framePacket{data: append(append([]byte{}, 0, 0, 0, 1), testPPS...), at: t0}
	for i := range 60 {
		p := append([]byte{0x41}, bytes.Repeat([]byte{0x44}, 799)...)
		p[len(p)-1] = byte(i)
		at := t0.Add(time.Duration(i+1) * 20 * time.Millisecond)
		if i == 0 {
			p = append(append([]byte{}, testIDR...), bytes.Repeat([]byte{0x11}, 4000)...)
		}
		b.frameCh <- framePacket{data: append(append([]byte{}, 0, 0, 0, 1), p...), at: at}
	}

	done := make(chan struct{})
	go b.writeFrames(done)
	// All packets carry pre-computed arrival times spanning 1.2s while the
	// writer consumes them in microseconds — the stalled-writer burst drain.
	for len(b.frameCh) > 0 {
		time.Sleep(time.Millisecond)
	}
	close(b.frameCh)
	<-done
	b.closeCurrentSegment() // finalize the still-open segment for listing

	files, err := mgr.ListFiles(cam)
	require.NoError(t, err)
	require.Len(t, files, 1)
	seg, err := merge.ParseSegment(files[0])
	require.NoError(t, err)
	require.Equal(t, 60, seg.SampleCount)

	// The pre-#506 bug: the whole backlog drained within microseconds wrote
	// 1ms-clamped durations, collapsing the timeline to ~60ms. Arrival-based
	// durations must span ≈ the 1.2s of real arrivals.
	got := seg.TotalDuration.Truncate(10 * time.Millisecond)
	require.GreaterOrEqual(t, got, 1100*time.Millisecond,
		"drained backlog must keep its arrival spacing (got %s)", seg.TotalDuration)
}
