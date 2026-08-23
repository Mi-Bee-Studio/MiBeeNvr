package health

// Recording integrity auditor (#469 Phase 5): subscribes to segment.completed
// and probes a rate-limited sample of freshly closed MP4 segments with
// mediaprobe (pure Go — reads moov boxes only, ~100-300KB of sequential I/O).
// Outcomes feed nvr_recording_audit_total{camera,result}; anomalies (duration
// == 0, unparseable moov) are logged as warnings so a degrading camera or disk
// surfaces before a user hits an unplayable recording.
//
// Resource discipline (design baseline A53/1GB): the auditor NEVER competes
// with recording writes for the disk. Probes are serialized on one goroutine,
// spaced by auditMinInterval, and the event queue is capped — overflow is
// dropped by the bus (sampled, not backlogged). Stop is safe in every state
// (never-started, mid-wait, mid-probe) — App.Stop may run without Start.

import (
	"context"
	"log/slog"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/mediaprobe"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
)

const (
	// auditMinInterval spaces consecutive probes so the auditor's sequential
	// reads never saturate an SD card that is also absorbing recording writes.
	auditMinInterval = 5 * time.Second
	// auditQueueCap bounds pending audits; overflow is dropped by the bus
	// (sampled, not backlogged).
	auditQueueCap = 16
)

// RecordingAuditor samples closed segments for integrity. Nil-safe: a nil
// *RecordingAuditor is a no-op (Start/Stop return immediately).
type RecordingAuditor struct {
	bus     *event.EventBus
	metrics *metrics.Metrics

	ch      chan event.Event
	stop    chan struct{}
	done    chan struct{}
	doneOne sync.Once   // done closes exactly once across all exit paths
	stopOne sync.Once   // stop closes exactly once
	started atomic.Bool // set before the run goroutine exists
	stopped atomic.Bool // Stop() was called — Start becomes a no-op
}

// NewRecordingAuditor creates an auditor. Nil bus or nil metrics disables it
// (the returned value is still safe to Start/Stop).
func NewRecordingAuditor(bus *event.EventBus, m *metrics.Metrics) *RecordingAuditor {
	if bus == nil || m == nil {
		return nil
	}
	return &RecordingAuditor{
		bus:     bus,
		metrics: m,
		ch:      make(chan event.Event, auditQueueCap),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
}

// Start subscribes to segment.completed and launches the probe loop.
// No-op if already started or if Stop ran first (App.Stop can precede the
// detached Start goroutine).
func (a *RecordingAuditor) Start(_ context.Context) error {
	if a == nil || !a.started.CompareAndSwap(false, true) {
		return nil
	}
	if a.stopped.Load() {
		// Stop already ran — close done so a concurrent Stop-waiter (if any
		// read started==true) is released, and skip the goroutine.
		a.finishDone()
		return nil
	}
	if err := a.bus.Subscribe(event.TopicSegmentCompleted, a.ch, 0); err != nil {
		a.started.Store(false)
		a.finishDone()
		return err
	}
	go a.run()
	return nil
}

// Stop unsubscribes, interrupts any in-flight spacing wait, and waits for the
// probe loop to exit (bounded by one mediaprobe run). Safe before Start.
func (a *RecordingAuditor) Stop() error {
	if a == nil {
		return nil
	}
	a.stopped.Store(true)
	a.stopOne.Do(func() { close(a.stop) })
	a.bus.Unsubscribe(event.TopicSegmentCompleted, a.ch)
	if a.started.Load() {
		<-a.done
	}
	return nil
}

// finishDone closes the done channel exactly once.
func (a *RecordingAuditor) finishDone() {
	a.doneOne.Do(func() { close(a.done) })
}

func (a *RecordingAuditor) run() {
	defer a.finishDone()
	var lastProbe time.Time
	for {
		var ev event.Event
		select {
		case ev = <-a.ch:
		case <-a.stop:
			// Shutdown: drop any queued audits rather than probing through them.
			return
		}
		seg, ok := ev.Data.(event.SegmentCompleted)
		if !ok || seg.FilePath == "" {
			continue
		}
		// Non-MP4 outputs (AVI legacy container, MJPEG frame dirs) are out of
		// mediaprobe's scope.
		if ext := filepath.Ext(seg.FilePath); ext != ".mp4" {
			continue
		}
		// Space probes: never more than one per auditMinInterval — but bail
		// out immediately on shutdown instead of sleeping through the wait.
		if wait := auditMinInterval - time.Since(lastProbe); wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-timer.C:
			case <-a.stop:
				timer.Stop()
				return
			}
		}
		lastProbe = time.Now()
		a.probe(seg)
	}
}

// probe runs mediaprobe on one segment and records the outcome.
func (a *RecordingAuditor) probe(seg event.SegmentCompleted) {
	info, err := mediaprobe.ProbeMP4(seg.FilePath)
	switch {
	case err != nil:
		a.metrics.IncRecordingAudit(seg.CameraID, "probe_error")
		slog.Warn("recording audit: probe failed",
			"camera_id", seg.CameraID, "file", seg.FilePath, "error", err)
	case info.Duration < 0.01:
		a.metrics.IncRecordingAudit(seg.CameraID, "zero_duration")
		slog.Warn("recording audit: zero-duration segment",
			"camera_id", seg.CameraID, "file", seg.FilePath, "frames", info.FrameCount)
	default:
		a.metrics.IncRecordingAudit(seg.CameraID, "ok")
	}
}
