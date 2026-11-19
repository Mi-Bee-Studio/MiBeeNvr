package recorder

// Metrics counters, panic recovery and the RecordingStats snapshot on the base
// recorder. Split out of base.go.

import (
	"runtime"
	"time"
)

func (b *baseRecorder) incActive() {
	if b.mtrics != nil {
		b.mtrics.ActiveRecordings.Inc()
	}
}

func (b *baseRecorder) decActive() {
	if b.mtrics != nil {
		b.mtrics.ActiveRecordings.Dec()
	}
}

func (b *baseRecorder) recordSegmentCreated() {
	if b.mtrics != nil {
		b.mtrics.SegmentsCreated.WithLabelValues(b.cfg.CameraID, b.driver.codecLabel()).Inc()
	}
}

func (b *baseRecorder) recordBytes(bytes int64) {
	if b.mtrics != nil {
		b.mtrics.RecordingBytesTotal.WithLabelValues(b.cfg.CameraID, b.driver.codecLabel()).Add(float64(bytes))
	}
}

func (b *baseRecorder) recordError(errorType string) {
	if b.mtrics != nil {
		b.mtrics.CameraErrors.WithLabelValues(b.cfg.CameraID, errorType).Inc()
	}
}

// ---------------------------------------------------------------------------
// Panic recovery (shared)
// ---------------------------------------------------------------------------

// recoverPanic is a deferred helper that logs panics with a stack trace
// instead of crashing the goroutine. Used by run() and writeFrames().
func (b *baseRecorder) recoverPanic(where string) {
	if panicErr := recover(); panicErr != nil {
		buf := make([]byte, 4096)
		buf = buf[:runtime.Stack(buf, false)]
		b.log.Error("PANIC recovered in "+where,
			"camera_id", b.cfg.CameraID,
			"panic", panicErr,
			"stack", string(buf))
	}
}

// RecordingStats is the recording-branch snapshot for the flow API (#480):
// current segment progress plus the frameCh ring-buffer water level. All
// reads are lock-light (mutex only for the segment triplet, atomics for the
// ring) so the flow endpoint can poll it without disturbing recording.
type RecordingStats struct {
	// Segmenting is true while an MP4 segment is open.
	Segmenting bool `json:"segmenting"`
	// SegmentDurS is the configured target segment duration in seconds.
	SegmentDurS float64 `json:"segment_dur_s,omitempty"`
	// SegmentElapsedS is how long the current segment has been recording
	// (arrival axis). Progress = elapsed / segment_dur_s.
	SegmentElapsedS float64 `json:"segment_elapsed_s,omitempty"`
	// SegmentFrames is the number of samples written into the current segment.
	SegmentFrames int64 `json:"segment_frames,omitempty"`
	// RingLen/RingCap is the frameCh watermark (len/cap) — how close the
	// recorder's write loop is to falling behind the RTP callback rate.
	RingLen int `json:"ring_buf_len"`
	RingCap int `json:"ring_buf_cap"`
	// RingDropsTotal mirrors nvr_recorder_ring_buffer_drops_total.
	RingDropsTotal int64 `json:"ring_buf_drops_total"`
}

// RecordingStats snapshots the recording branch. Implemented on baseRecorder,
// so every recorder embedding it (H.264/H.265) exposes it; the flow handler
// discovers it via the RecordingStatsProvider interface and simply omits the
// section for recorders that don't.
func (b *baseRecorder) RecordingStats() RecordingStats {
	st := RecordingStats{
		SegmentDurS:    b.cfg.SegmentDur.Seconds(),
		RingCap:        b.cfg.RingBufCap,
		RingDropsTotal: b.dropped.Load(),
	}
	if ch := b.frameChPtr.Load(); ch != nil {
		st.RingLen = len(*ch)
	}
	b.mu.Lock()
	if b.muxer != nil {
		st.Segmenting = true
		st.SegmentElapsedS = time.Since(b.segStart).Seconds()
		st.SegmentFrames = b.frameCount.Load()
	}
	b.mu.Unlock()
	return st
}
