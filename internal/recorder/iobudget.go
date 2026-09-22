package recorder

import (
	"context"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/iobudget"
)

// writeBudget paces segment-sample writes against the process-wide budget as
// the "recording" tenant (#886, gray-release). nil (default) = recording
// writes are unthrottled, exactly as before. Set once by pkg/app wiring at
// startup when io.recording_writes_budgeted is enabled; read-only afterwards.
var writeBudget iobudget.Limiter

// SetWriteBudget installs the recording-write I/O budget. Passing nil
// disables pacing.
func SetWriteBudget(l iobudget.Limiter) { writeBudget = l }

// chargeRecordingWrite bills n bytes of segment I/O before the muxer write.
// With no budget installed it is a nil check. It is called from the single
// writeFrames drain goroutine with context.Background() — Wait blocks (paces)
// when the bucket is starved, which is the intended semantics; the ring
// buffer absorbs the delay and drops only if the stall outlasts it.
func chargeRecordingWrite(n int64) {
	if writeBudget == nil || n <= 0 {
		return
	}
	//nolint:errcheck // ctx is never canceled; a nil check beat error plumbing here
	_ = writeBudget.Wait(context.Background(), iobudget.ConsumerRecording, n)
}
