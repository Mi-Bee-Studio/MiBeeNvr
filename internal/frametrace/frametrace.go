// Package frametrace implements per-camera sampling for frame_trace
// breadcrumb logs (#482).
//
// The frame_trace breadcrumbs (hub in/drop → ws/flv/hls/webrtc recv/drop)
// are Debug-level and normally invisible without raising the global log
// level. This package adds a per-camera sampling window: while active, that
// camera's breadcrumbs are re-emitted at Info level through a dedicated
// logger ("component": "frame-trace") so they can be tailed in isolation —
// without enabling per-frame logging for every camera on the box
// (log volume = frame rate × consumers × ~200-300B/line).
package frametrace

import (
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// Defaults and clamps for the sampling window requested via the API.
const (
	DefaultDuration = 30 * time.Second
	MaxDuration     = 5 * time.Minute
)

var traceLogger atomic.Pointer[slog.Logger]

func init() {
	traceLogger.Store(slog.Default().With("component", "frame-trace"))
}

// SetLogger overrides the destination logger (tests).
func SetLogger(l *slog.Logger) {
	if l == nil {
		l = slog.Default()
	}
	traceLogger.Store(l.With("component", "frame-trace"))
}

// registry tracks the sampling deadline per camera. Zero deadline = inactive.
// The map is copy-on-write behind an atomic pointer so the hot path
// (Active + Log, once per frame per stage) only pays one atomic load.
type registry struct {
	mu      sync.Mutex // serializes Enable/Disable/clearExpired writers
	current atomic.Pointer[map[string]time.Time]
}

var reg registry

func loadReg() map[string]time.Time {
	if m := reg.current.Load(); m != nil {
		return *m
	}
	return nil
}

// Enable starts (or extends) the sampling window for a camera and returns
// the absolute instant it ends. Duration is clamped to (0, MaxDuration].
func Enable(cameraID string, d time.Duration) time.Time {
	if d <= 0 {
		d = DefaultDuration
	}
	if d > MaxDuration {
		d = MaxDuration
	}
	until := time.Now().Add(d)

	reg.mu.Lock()
	defer reg.mu.Unlock()
	next := make(map[string]time.Time, len(loadReg())+1)
	for id, t := range loadReg() {
		if time.Now().Before(t) {
			next[id] = t
		}
	}
	next[cameraID] = until
	reg.current.Store(&next)
	return until
}

// Disable ends the sampling window for a camera (best-effort troubleshooting
// stop).
func Disable(cameraID string) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	old := loadReg()
	if _, ok := old[cameraID]; !ok {
		return
	}
	next := make(map[string]time.Time, len(old))
	for id, t := range old {
		if id != cameraID {
			next[id] = t
		}
	}
	reg.current.Store(&next)
}

// Active reports whether the camera's sampling window is open. Hot path —
// one atomic load plus one time comparison when the registry is empty.
func Active(cameraID string) bool {
	m := loadReg()
	if m == nil {
		return false
	}
	until, ok := m[cameraID]
	return ok && time.Now().Before(until)
}

// Log emits one frame_trace breadcrumb: Info through the dedicated
// frame-trace logger while the camera is sampled, plain Debug otherwise.
// The args format matches the existing call sites
// ("trace_id", id, "stage", stage, ...).
func Log(cameraID string, args ...any) {
	if Active(cameraID) {
		traceLogger.Load().Info("frame_trace", args...)
		return
	}
	slog.Debug("frame_trace", args...)
}

// LogDrop emits a drop breadcrumb: Info through the dedicated logger while
// sampled, Warn otherwise (drops were Warn before #482 — keep that
// visibility; the escalation only changes the destination while sampled).
func LogDrop(cameraID string, args ...any) {
	if Active(cameraID) {
		traceLogger.Load().Info("frame_trace", args...)
		return
	}
	slog.Warn("frame_trace", args...)
}
