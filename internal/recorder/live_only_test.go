package recorder

import (
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

// countingStore wraps plainStore and counts CreateSegment calls. When
// RecordEnabled is false, writeFrames must drain frameCh WITHOUT ever creating
// a segment — this counter asserts that.
type countingStore struct {
	plainStore
	creates atomic.Int64
}

func (s *countingStore) CreateSegment(cameraID, name string) (string, string, error) {
	s.creates.Add(1)
	return "", "", nil
}

// newLiveOnlyBaseRecorder builds a minimal baseRecorder wired with an H.264
// driver and a counting store. When recordDisabled is true, RecordEnabled points
// to false (live-only). It does NOT connect RTSP — writeFrames is driven
// directly via frameCh.
func newLiveOnlyBaseRecorder(t *testing.T, recordDisabled bool) (*baseRecorder, *countingStore) {
	t.Helper()
	store := &countingStore{}
	var recEnabled *bool
	if recordDisabled {
		f := false
		recEnabled = &f
	}
	b := &baseRecorder{
		cfg: BaseConfig{
			CameraID:      "cam-live-only",
			SegmentDur:    2 * time.Second,
			RingBufCap:    16,
			RecordEnabled: recEnabled,
		},
		store:   store,
		driver:  H264NALDriver{},
		log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		frameCh: make(chan framePacket, 32),
	}
	return b, store
}

// TestWriteFrames_LiveOnlyDrainsWithoutRecording feeds IDR + parameter-set
// NALUs into writeFrames with RecordEnabled=false and asserts NO segment is
// ever created. The frame channel must drain cleanly (no deadlock) — mirroring
// how a live-only camera keeps the StreamHub fed while writing nothing to disk.
func TestWriteFrames_LiveOnlyDrainsWithoutRecording(t *testing.T) {
	t.Helper()
	b, store := newLiveOnlyBaseRecorder(t, true)

	// Build valid H.264 NALUs with the 4-byte start-code prefix that frameCh
	// expects: SPS, PPS, then an IDR slice.
	sps := append([]byte{0, 0, 0, 1}, testSPS...)
	pps := append([]byte{0, 0, 0, 1}, testPPS...)
	idr := append([]byte{0, 0, 0, 1}, testIDR...)

	done := make(chan struct{})
	go b.writeFrames(done)

	b.frameCh <- framePacket{data: sps, at: time.Now()}
	b.frameCh <- framePacket{data: pps, at: time.Now()}
	b.frameCh <- framePacket{data: idr, at: time.Now()}
	// Give the goroutine a moment to process.
	time.Sleep(50 * time.Millisecond)
	close(b.frameCh)
	<-done

	if got := store.creates.Load(); got != 0 {
		t.Fatalf("live-only recorder created %d segments; expected 0", got)
	}
}

// TestWriteFrames_RecordingEnabledCreatesSegment confirms the gate does NOT
// suppress recording when RecordEnabled is nil (default = record). This guards
// against accidentally inverting the opt-out semantics.
func TestWriteFrames_RecordingEnabledCreatesSegment(t *testing.T) {
	t.Helper()
	b, store := newLiveOnlyBaseRecorder(t, false) // nil => record

	sps := append([]byte{0, 0, 0, 1}, testSPS...)
	pps := append([]byte{0, 0, 0, 1}, testPPS...)
	idr := append([]byte{0, 0, 0, 1}, testIDR...)

	done := make(chan struct{})
	go b.writeFrames(done)

	b.frameCh <- framePacket{data: sps, at: time.Now()}
	b.frameCh <- framePacket{data: pps, at: time.Now()}
	b.frameCh <- framePacket{data: idr, at: time.Now()}
	time.Sleep(50 * time.Millisecond)
	close(b.frameCh)
	<-done

	// With recording enabled and parameter sets + IDR present, createNewSegment
	// should have run at least once. (It may fail to fully build the muxer
	// without a real store path, but CreateSegment itself must be called.)
	if got := store.creates.Load(); got < 1 {
		t.Fatalf("recording-enabled recorder created %d segments; expected >=1", got)
	}
}
