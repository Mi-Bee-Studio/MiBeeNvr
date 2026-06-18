package relay

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

const (
	// maxEmitsPerSecond is the hard cap on silent frame emissions (≤10% of
	// the 50-frame audio consumer buffer).
	maxEmitsPerSecond = 5

	// dropRecoveryPeriod is the time we suppress emissions after an audio
	// drop is reported.
	dropRecoveryPeriod = 3 * time.Second

	// emitterChannelDepth is the buffer size of the output channel returned
	// by Start().
	emitterChannelDepth = 10
)

// ---------------------------------------------------------------------------
// BufferAwareSilenceEmitter
// ---------------------------------------------------------------------------

// BufferAwareSilenceEmitter wraps a SilenceAACGenerator and gates emission
// via ShouldEmit() based on audio buffer health (drop detection) and a 5 fps
// hard cap.  The goal is to keep the audio bus from being flooded with silent
// frames when the downstream is saturated.
//
// Use NotifyDrop() to signal that the StreamHub audio consumer dropped a frame
// (called from the relay engine's OnAudioDrop callback).  After a drop, the
// emitter suppresses all frame emission for dropRecoveryPeriod (3 seconds),
// then resumes normally — subject to the 5 fps hard cap.
//
// The 5 fps cap corresponds to ≤10% of the 50-frame audio consumer buffer
// per second, which is well below the 10 fps the generator produces.
type BufferAwareSilenceEmitter struct {
	generator *SilenceAACGenerator
	mu        sync.Mutex

	dropDetected atomic.Bool
	lastDropTime time.Time

	emitCount   int
	windowStart time.Time

	totalEmitted    atomic.Int64
	totalSuppressed atomic.Int64
}

// NewBufferAwareSilenceEmitter creates a new emitter wrapping the provided
// generator.  The caller is responsible for lifecycle (Start/Stop).
func NewBufferAwareSilenceEmitter(generator *SilenceAACGenerator) *BufferAwareSilenceEmitter {
	return &BufferAwareSilenceEmitter{
		generator:   generator,
		windowStart: time.Now(),
	}
}

// ShouldEmit reports whether a silent frame should be emitted right now.
//
// It returns false when:
//   - A drop was detected within the last dropRecoveryPeriod (3 s).
//   - The per-second hard cap (maxEmitsPerSecond = 5) has been reached.
//
// Thread-safe.
func (e *BufferAwareSilenceEmitter) ShouldEmit() bool {
	e.mu.Lock()
	defer e.mu.Unlock()

	// Drop backoff: if a drop was reported, suppress for the recovery period.
	if e.dropDetected.Load() {
		if time.Since(e.lastDropTime) > dropRecoveryPeriod {
			e.dropDetected.Store(false)
		} else {
			return false
		}
	}

	// Reset the 1 s counting window when it expires.
	if time.Since(e.windowStart) > time.Second {
		e.emitCount = 0
		e.windowStart = time.Now()
	}

	// Hard cap.
	if e.emitCount >= maxEmitsPerSecond {
		return false
	}

	e.emitCount++
	return true
}

// NotifyDrop is called by the relay engine when the StreamHub audio consumer
// reports a dropped frame (channel full).  After this, ShouldEmit returns
// false for dropRecoveryPeriod, then resumes normally.
//
// Thread-safe.
func (e *BufferAwareSilenceEmitter) NotifyDrop() {
	e.mu.Lock()
	e.dropDetected.Store(true)
	e.lastDropTime = time.Now()
	e.mu.Unlock()
}

// AudioConfig returns the AudioSpecificConfig bytes as configured in the
// underlying SilenceAACGenerator (delegates to generator.Config()).
func (e *BufferAwareSilenceEmitter) AudioConfig() []byte {
	return e.generator.Config()
}

// Start begins the silence generator and returns a channel of silent AAC-LC
// frames filtered by ShouldEmit().  Frames that pass the gate are forwarded;
// suppressed frames are counted in totalSuppressed.
//
// The returned channel is closed when the underlying generator stops (context
// cancelled, Stop() called, or silence timeout expires).
func (e *BufferAwareSilenceEmitter) Start(ctx context.Context) <-chan []byte {
	genCh := e.generator.Start(ctx)
	outCh := make(chan []byte, emitterChannelDepth)

	go e.filterLoop(genCh, outCh)

	return outCh
}

// Stop delegates to the underlying SilenceAACGenerator.Stop(), which cancels
// the context and closes the output channel.
func (e *BufferAwareSilenceEmitter) Stop() {
	e.generator.Stop()
}

// ---------------------------------------------------------------------------
// Metrics accessors
// ---------------------------------------------------------------------------

// Emitted returns the total number of silent frames forwarded since Start.
func (e *BufferAwareSilenceEmitter) Emitted() int64 {
	return e.totalEmitted.Load()
}

// Suppressed returns the total number of silent frames dropped by the limiter
// (either due to drop backoff or the per-second hard cap).
func (e *BufferAwareSilenceEmitter) Suppressed() int64 {
	return e.totalSuppressed.Load()
}

// ---------------------------------------------------------------------------
// internal
// ---------------------------------------------------------------------------

// filterLoop reads frames from genCh, gates them via ShouldEmit, and forwards
// approved frames to outCh.  It exits when genCh is closed.
func (e *BufferAwareSilenceEmitter) filterLoop(genCh <-chan []byte, outCh chan<- []byte) {
	defer close(outCh)

	for frame := range genCh {
		if e.ShouldEmit() {
			select {
			case outCh <- frame:
				e.totalEmitted.Add(1)
			default:
				// outCh full — this should not normally happen with a
				// 10-deep buffer and 5 fps, but protect the producer
				// goroutine regardless.
				e.totalSuppressed.Add(1)
			}
		} else {
			e.totalSuppressed.Add(1)
		}
	}
}
