package recorder

// Regression tests for issue #498 (original-segment POC damage), found during
// the #435 adaptive-recording field test on real cameras:
//
//	Bug A — the timelapse-exit flush carried the TRIGGER frame (observe
//	         appended it before takeGOP); the normal write path wrote it
//	         again right after → "Duplicate POC" in every exit segment.
//	Bug B — gopFrame.written is a per-SEGMENT marking but survived segment
//	         rotation; a flush landing in a FRESH segment skipped mid-GOP
//	         frames that existed only in the closed file → "Could not find
//	         ref with POC".

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
	// Simulate normal-path writes: everything retained is on disk.
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

	// The trigger IS the IDR: observe() resets the ring to [IDR] and the exit
	// path calls takeGOP — nothing remains that needs flushing (the caller
	// writes the IDR itself via the normal path).
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

	// Refill and clear — the rotation twin must forget the closed segment's
	// markings just like the video ring.
	r.append(true, []byte{3, 4}, 250*time.Microsecond, now.Add(time.Millisecond))
	r.markWritten()
	r.clearWritten()
	for i, s := range r.drain() {
		require.False(t, s.Written, "sample %d must be unwritten after clearWritten", i)
	}
}

// TestH264Adaptive_TLExitFlushAfterRotation drives writeFrames end-to-end
// through the exact field sequence that produced corrupted originals:
// normal-rate writing → segment rotation (stale written flags) → timelapse
// entry → spike exit whose flush must land in a FRESH segment containing the
// complete retained GOP, with the trigger frame written exactly once.
func TestH264Adaptive_TLExitFlushAfterRotation(t *testing.T) {
	mgr := newTestManager(t)
	const cam = "h264-adaptive-poc"
	rec := NewH264Recorder(H264Config{
		CameraID:   cam,
		RTSPURL:    "rtsp://ignored", // never connected — writeFrames driven directly
		SegmentDur: 200 * time.Millisecond, // rotate mid-NORMAL, before TL entry
		RingBufCap: 256,
		Adaptive: &AdaptiveConfig{
			CalmThreshold:     400 * time.Millisecond,
			TimelapseInterval: time.Hour, // no sparse keyframes inside the window
			SpikeFactor:       3.0,
			MaxGOPBuffer:      8 << 20,
		},
	}, mgr)
	b := rec.baseRecorder
	b.frameCh = make(chan []byte, 256) // normally created in connectAndRecord
	b.resetAdaptive()

	done := make(chan struct{})
	go b.writeFrames(done)
	defer func() {
		close(b.frameCh)
		<-done
	}()

	send := func(nal []byte) {
		b.frameCh <- append(append([]byte{}, 0, 0, 0, 1), nal...)
	}

	// Parameter sets + IDR anchor (padded to a realistic keyframe size).
	send(testSPS)
	send(testPPS)
	idr := append(append([]byte{}, testIDR...), bytes.Repeat([]byte{0x11}, 20000)...)
	send(idr)

	// 35 calm P-frames at 20ms (700ms total): rotation fires at ~200ms
	// (frames written to segment 1, flags marked), timelapse entry at ~400ms,
	// the rest is sparse-skipped but retained.
	const totalPs = 35
	for i := range totalPs {
		p := append([]byte{0x41}, bytes.Repeat([]byte{0x22}, 799)...)
		p[len(p)-2] = byte(i >> 8) // unique per frame — adjacent-duplicate
		p[len(p)-1] = byte(i)      // detection must not false-positive
		send(p)
		time.Sleep(20 * time.Millisecond)
	}

	// Major spike (300KB vs 800B baseline): single-frame timelapse exit.
	spike := append([]byte{0x41}, bytes.Repeat([]byte{0x33}, 299999)...)
	send(spike)

	// Let the writer drain, then collect the finalized segments.
	require.Eventually(t, func() bool { return len(b.frameCh) == 0 }, 3*time.Second, 10*time.Millisecond)
	time.Sleep(100 * time.Millisecond)

	files, err := mgr.ListFiles(cam)
	require.NoError(t, err)
	require.Len(t, files, 2, "rotation + exit flush must produce exactly 2 segments")
	sort.Strings(files) // nano-suffixed names sort chronologically

	seg1, err := merge.ParseSegment(files[0])
	require.NoError(t, err)
	// Bug B precondition: frames WERE written pre-rotation (their written
	// flags went stale when segment 1 closed).
	require.GreaterOrEqual(t, seg1.SampleCount, 3, "segment 1 must hold the IDR plus pre-rotation P-frames")

	seg2, err := merge.ParseSegment(files[1])
	require.NoError(t, err)
	// The exit flush must carry the whole retained ring (IDR + every P since
	// connect — rotation cleared the stale flags) plus the trigger written
	// once by the normal path. Bug B skipped the pre-rotation frames here;
	// Bug A wrote the trigger twice.
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
			t.Fatalf("segments %d/%d are identical — a frame was written twice (size=%d tail=%x)", i-1, i, len(sample), sample[len(sample)-4:])
		}
		prev = sample
	}
	require.Equal(t, 1, spikeCount, "trigger frame must be on disk exactly once")
}
