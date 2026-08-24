package health

// Recording integrity auditor (#469 Phase 5, deep check added in #489):
// subscribes to segment.completed and does two things with freshly closed MP4
// segments —
//
//  1. Structure probe (mediaprobe, pure Go — moov boxes only, ~100-300KB of
//     sequential I/O) on every (rate-spaced) segment →
//     nvr_recording_audit_total{camera,result}. Catches zero-duration and
//     unparseable moov.
//  2. Decode-level DEEP CHECK (ffmpeg -v error … -f null -) at most once per
//     deepCheckInterval per camera → nvr_recording_deepcheck_total{camera,result}.
//     Catches reference-chain corruption (missing/duplicate POC, broken
//     slices) that structure probing cannot see — found in the wild by manual
//     ffmpeg sampling (#488), automated here (#489). FFmpeg is OPTIONAL:
//     with no binary configured the deep check is silently disabled.
//
// Resource discipline (design baseline A53/1GB): the auditor NEVER competes
// with recording writes for the disk. Probes are serialized on one goroutine,
// spaced by auditMinInterval, and the event queue is capped — overflow is
// dropped by the bus (sampled, not backlogged). The deep check decodes at
// most deepCheckSampleDur of stream per run with a hard context timeout.
// Stop is safe in every state (never-started, mid-wait, mid-probe) — App.Stop
// may run without Start.

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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
	// deepCheckInterval is the per-camera deep-check cadence (#489 feedback:
	// 每小时每相机抽 1 个文件). The check piggybacks on segment.completed —
	// it fires on the FIRST segment event after the interval elapses, so
	// cameras that stop recording also stop deep-checking (nothing new to check).
	deepCheckInterval = time.Hour
	// deepCheckSampleDur caps how much of the stream is decoded per check —
	// full-segment software decode on A53 would hog the CPU budget.
	deepCheckSampleDur = 120 * time.Second
	// deepCheckTimeout is the hard wall-clock cap per deep check.
	deepCheckTimeout = 5 * time.Minute
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

	// ffmpegPath enables the decode-level deep check when non-empty. The
	// caller (register.go) resolves the configured path or a PATH fallback —
	// the auditor itself never probes the environment, keeping tests hermetic.
	ffmpegPath string
	// baseCancel cancels in-flight deep checks on Stop.
	baseCtx    context.Context
	baseCancel context.CancelFunc

	// lastDeepCheck timestamps the latest deep check per camera (guarded by
	// the single run goroutine — no lock needed).
	lastDeepCheck map[string]time.Time

	// deepCheckNow is the injectable ffmpeg runner (stderr, error). Tests
	// replace it; production shells out to ffmpegPath.
	deepCheckNow func(ctx context.Context, path string) (string, error)
}

// Option configures a RecordingAuditor.
type Option func(*RecordingAuditor)

// WithFFmpegPath enables the decode-level deep check using the given binary
// (resolved by the caller; empty = deep check disabled).
func WithFFmpegPath(path string) Option {
	return func(a *RecordingAuditor) { a.ffmpegPath = path }
}

// NewRecordingAuditor creates an auditor. Nil bus or nil metrics disables it
// (the returned value is still safe to Start/Stop).
func NewRecordingAuditor(bus *event.EventBus, m *metrics.Metrics, opts ...Option) *RecordingAuditor {
	if bus == nil || m == nil {
		return nil
	}
	a := &RecordingAuditor{
		bus:           bus,
		metrics:       m,
		ch:            make(chan event.Event, auditQueueCap),
		stop:          make(chan struct{}),
		done:          make(chan struct{}),
		lastDeepCheck: make(map[string]time.Time),
	}
	a.baseCtx, a.baseCancel = context.WithCancel(context.Background())
	a.deepCheckNow = a.runFFmpegDeepCheck
	for _, opt := range opts {
		opt(a)
	}
	return a
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

// Stop unsubscribes, interrupts any in-flight spacing wait or deep check, and
// waits for the probe loop to exit (bounded by one mediaprobe run / the deep
// check's context cancellation). Safe before Start.
func (a *RecordingAuditor) Stop() error {
	if a == nil {
		return nil
	}
	a.stopped.Store(true)
	a.stopOne.Do(func() { close(a.stop) })
	if a.baseCancel != nil {
		a.baseCancel() // kill any in-flight ffmpeg deep check
	}
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
		a.maybeDeepCheck(seg)
	}
}

// maybeDeepCheck runs the decode-level check when this camera's interval has
// elapsed (#489). Called from the single run goroutine after the structure
// probe — deep checks therefore serialize with everything else and never add
// a second IO/CPU consumer.
func (a *RecordingAuditor) maybeDeepCheck(seg event.SegmentCompleted) {
	if a.ffmpegPath == "" || a.deepCheckNow == nil {
		return // deep check disabled (no ffmpeg)
	}
	if last, ok := a.lastDeepCheck[seg.CameraID]; ok && time.Since(last) < deepCheckInterval {
		return
	}
	a.lastDeepCheck[seg.CameraID] = time.Now()

	// The rolling merge consumes source files within ~35s of completion
	// (issue #492 field data): by the time the hourly deep check fires, the
	// file may already be gone. That is not a decode failure — classify it
	// separately so ok/decode_error stay meaningful.
	if _, err := os.Stat(seg.FilePath); err != nil {
		a.metrics.IncRecordingDeepCheck(seg.CameraID, "vanished")
		slog.Debug("recording deep check skipped: source file already consumed",
			"camera_id", seg.CameraID, "file", seg.FilePath)
		return
	}

	ctx, cancel := context.WithTimeout(a.baseCtx, deepCheckTimeout)
	defer cancel()
	stderr, err := a.deepCheckNow(ctx, seg.FilePath)
	switch {
	case ctx.Err() != nil:
		// Shutdown or hard timeout raced the verdict — do not score it.
		return
	case err != nil && isVanishedStderr(stderr):
		// ffmpeg could not open the INPUT: the rolling merge consumed the
		// source between the event and the probe (issue #492 field data).
		a.metrics.IncRecordingDeepCheck(seg.CameraID, "vanished")
		slog.Debug("recording deep check skipped: source file already consumed",
			"camera_id", seg.CameraID, "file", seg.FilePath)
	case err != nil:
		a.metrics.IncRecordingDeepCheck(seg.CameraID, "decode_error")
		slog.Warn("recording deep check failed",
			"camera_id", seg.CameraID, "file", seg.FilePath, "error", err, "stderr", firstLine(stderr))
	default:
		if real := filterDeepCheckStderr(stderr); real != "" {
			// ffmpeg exited 0 but printed real error-level lines — corruption
			// (e.g. reference-chain issues that don't abort the decode).
			a.metrics.IncRecordingDeepCheck(seg.CameraID, "decode_error")
			slog.Warn("recording deep check found decode errors",
				"camera_id", seg.CameraID, "file", seg.FilePath, "stderr", firstLine(real))
		} else {
			a.metrics.IncRecordingDeepCheck(seg.CameraID, "ok")
		}
	}
}

// isVanishedStderr reports whether ffmpeg's failure output says the INPUT
// file could not be opened — the rolling merge consumed the source between
// the segment event and the probe (issue #492), which is not a decode defect.
func isVanishedStderr(stderr string) bool {
	return strings.Contains(stderr, "Error opening input") ||
		strings.Contains(stderr, "No such file or directory")
}

// filterDeepCheckStderr strips benign error-level lines from ffmpeg's stderr
// so a healthy recording can still score ok:
//
//   - "non monotonically increasing dts" — the null muxer warns because the
//     MP4 samples carry pts==dts by muxer design; virtually every real
//     recording trips it, so without the filter `ok` was structurally
//     unreachable (issue #492 field data: 28 steady-state checks, ok=0).
//   - "Last message repeated N times" notices inherit the verdict of the
//     line they repeat — dropped only when that line was dropped.
func filterDeepCheckStderr(stderr string) string {
	var kept []string
	prevDropped := false
	for _, line := range strings.Split(stderr, "\n") {
		if line == "" {
			continue
		}
		if strings.Contains(line, "non monotonically increasing dts") {
			prevDropped = true
			continue
		}
		if prevDropped && strings.Contains(line, "Last message repeated") {
			continue
		}
		prevDropped = false
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

// runFFmpegDeepCheck decodes up to deepCheckSampleDur of the file, discarding
// output, and returns ffmpeg's error-level stderr. Exit != 0 with empty stderr
// still reports an error via err.
func (a *RecordingAuditor) runFFmpegDeepCheck(ctx context.Context, path string) (string, error) {
	cmd := exec.CommandContext(ctx, a.ffmpegPath,
		"-v", "error",
		"-i", path,
		"-t", strconv.Itoa(int(deepCheckSampleDur/time.Second)), // ffmpeg wants plain seconds, not Go durations
		"-f", "null", "-",
	)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil && ctx.Err() == nil {
		return stderr.String(), err
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	return stderr.String(), nil
}

// firstLine trims stderr to its first line for compact log output.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// probe runs mediaprobe on one segment and records the outcome.
func (a *RecordingAuditor) probe(seg event.SegmentCompleted) {
	if _, err := os.Stat(seg.FilePath); err != nil {
		// The rolling merge already consumed the source before the spaced
		// probe reached it (issue #487 field data: probe_error grew on files
		// deleted between event and probe) — expected on rolling-merge
		// cameras, not a probe failure.
		a.metrics.IncRecordingAudit(seg.CameraID, "vanished")
		slog.Debug("recording audit: source file already consumed",
			"camera_id", seg.CameraID, "file", seg.FilePath)
		return
	}
	info, err := mediaprobe.ProbeMP4(seg.FilePath)
	switch {
	case err != nil:
		if errors.Is(err, fs.ErrNotExist) {
			// Vanished between the stat above and the open — same race.
			a.metrics.IncRecordingAudit(seg.CameraID, "vanished")
			slog.Debug("recording audit: source file vanished mid-probe",
				"camera_id", seg.CameraID, "file", seg.FilePath)
			return
		}
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
