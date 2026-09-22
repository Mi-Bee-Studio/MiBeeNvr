package recorder

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// recordingLimiter records charges for the recording tenant.
type recordingLimiter struct {
	mu      sync.Mutex
	charged map[string]int64
}

func (f *recordingLimiter) Wait(_ context.Context, consumer string, n int64) error {
	f.mu.Lock()
	f.charged[consumer] += n
	f.mu.Unlock()
	return nil
}

// TestWriteFramesChargesRecordingBudget drives the real writeFrames →
// WriteSample path with recording enabled and asserts every written NALU is
// billed to the "recording" tenant (#886). The budget is a package-level
// global; cleanup restores the unthrottled default for other tests.
func TestWriteFramesChargesRecordingBudget(t *testing.T) {
	fl := &recordingLimiter{charged: map[string]int64{}}
	SetWriteBudget(fl)
	t.Cleanup(func() { SetWriteBudget(nil) })

	mgr := newTestManager(t)
	rec := NewH264Recorder(H264Config{
		CameraID:   "h264-io-budget",
		RTSPURL:    "rtsp://ignored",
		SegmentDur: time.Second,
		RingBufCap: 16,
	}, mgr)
	b := rec.baseRecorder
	b.frameCh = make(chan framePacket, 8)
	done := make(chan struct{})
	go b.writeFrames(done)
	defer func() {
		close(b.frameCh)
		<-done
	}()
	// Close the in-flight segment before t.TempDir cleanup — on Windows an
	// open handle blocks RemoveAll. The recorder was never Start()ed, so
	// Stop() alone would skip teardown.
	t.Cleanup(func() { b.closeCurrentSegment() })

	t0 := time.Now()
	send := func(nal []byte, at time.Time) {
		b.frameCh <- framePacket{data: append(append([]byte{}, 0, 0, 0, 1), nal...), at: at}
	}
	send(testSPS, t0)
	send(testPPS, t0)
	send(testIDR, t0)
	send(testPFrame, t0.Add(20*time.Millisecond))

	// The written-byte charge lands before the drain iteration ends; wait
	// for the segment to exist as the completion signal.
	require.Eventually(t, func() bool {
		fl.mu.Lock()
		defer fl.mu.Unlock()
		return fl.charged["recording"] >= int64(len(testIDR)+len(testPFrame))
	}, 5*time.Second, 10*time.Millisecond, "IDR+P NALU bytes must be charged to the recording tenant")
}
