package relay

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// 90kHz clock constants for A/V PTS drift detection.
const (
	// driftThresholdTicks is 1 second at 90kHz (video PTS standard clock).
	driftThresholdTicks int64 = 90000

	// sustainedDriftDuration is how long drift must continuously exceed
	// the threshold before ShouldReconnect returns true.
	sustainedDriftDuration = 5 * time.Second

	// driftLogInterval is how often the background logging goroutine
	// reports the current A/V drift.
	driftLogInterval = 60 * time.Second
)

// DriftReconnectError is returned via cbErr when sustained A/V PTS drift
// triggers a target reconnect.
var DriftReconnectError = &driftReconnectError{}

type driftReconnectError struct{}

func (e *driftReconnectError) Error() string {
	return "A/V PTS drift exceeded threshold, reconnecting"
}

// DriftMonitor tracks A/V PTS drift between video and audio callbacks
// and signals a reconnect when the drift exceeds 1s for more than 5
// consecutive seconds. All hot-path operations are lock-free atomic
// reads/writes — the mutex is only taken on sustained-drift state
// transitions (at most once per second under drift conditions).
type DriftMonitor struct {
	lastVideoPTS atomic.Int64 // 90kHz ticks
	lastAudioPTS atomic.Int64 // 90kHz ticks
	hasVideo     atomic.Bool  // true after first RecordVideo call
	hasAudio     atomic.Bool  // true after first RecordAudio call
	highWater    atomic.Int64 // max |drift| in 90kHz ticks (for metrics)

	// sustainedDuration is configurable for testing; defaults to 5s.
	sustainedDuration time.Duration

	mu         sync.Mutex
	driftSince time.Time // wall clock when drift first exceeded threshold;
	// zero value means "not in drift state"
}

// NewDriftMonitor creates a DriftMonitor with a 5-second sustained-window.
func NewDriftMonitor() *DriftMonitor {
	return &DriftMonitor{
		sustainedDuration: sustainedDriftDuration,
	}
}

// RecordVideo stores the latest video PTS (90kHz clock) and re-evaluates
// the sustained-drift state. Safe to call from any goroutine — the atomic
// store is O(1) and the mutex is only held for the threshold comparison.
func (m *DriftMonitor) RecordVideo(pts int64) {
	m.lastVideoPTS.Store(pts)
	m.hasVideo.Store(true)
	m.checkDrift()
}

// RecordAudio stores the latest audio PTS (90kHz clock) and re-evaluates
// the sustained-drift state. Safe to call from any goroutine.
func (m *DriftMonitor) RecordAudio(pts int64) {
	m.lastAudioPTS.Store(pts)
	m.hasAudio.Store(true)
	m.checkDrift()
}

// checkDrift computes the current absolute drift and updates the
// sustained-drift window and high-water mark. It is called by both
// RecordVideo and RecordAudio.
func (m *DriftMonitor) checkDrift() {
	// No drift data when one stream hasn't arrived yet
	// (e.g., no-audio camera, or first frames not yet seen).
	if !m.hasVideo.Load() || !m.hasAudio.Load() {
		m.mu.Lock()
		m.driftSince = time.Time{}
		m.mu.Unlock()
		return
	}

	vpts := m.lastVideoPTS.Load()
	apts := m.lastAudioPTS.Load()

	drift := vpts - apts
	if drift < 0 {
		drift = -drift
	}

	// Lock-free high-water mark update (CAS loop).
	for {
		current := m.highWater.Load()
		if drift <= current {
			break
		}
		if m.highWater.CompareAndSwap(current, drift) {
			break
		}
	}

	// Update sustained-drift window under mutex.
	m.mu.Lock()
	if drift > driftThresholdTicks {
		if m.driftSince.IsZero() {
			m.driftSince = time.Now()
		}
	} else {
		m.driftSince = time.Time{}
	}
	m.mu.Unlock()
}

// DriftMs returns the absolute A/V PTS drift in milliseconds.
// Returns 0 when no drift data is available (at least one of video or
// audio has not been recorded yet, e.g., a no-audio camera).
func (m *DriftMonitor) DriftMs() float64 {
	if !m.hasVideo.Load() || !m.hasAudio.Load() {
		return 0
	}

	vpts := m.lastVideoPTS.Load()
	apts := m.lastAudioPTS.Load()

	drift := vpts - apts
	if drift < 0 {
		drift = -drift
	}

	// 90kHz → ms: drift × 1000 / 90000
	return float64(drift) * 1000.0 / 90000.0
}

// ShouldReconnect returns true when the A/V drift has exceeded the 1s
// threshold continuously for more than the sustained duration (default 5s).
// It is safe to call from any goroutine.
func (m *DriftMonitor) ShouldReconnect() bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.driftSince.IsZero() {
		return false
	}
	return time.Since(m.driftSince) > m.sustainedDuration
}

// Reset clears all tracked state. Must be called after a reconnect so
// that the new connection starts with a clean slate.
func (m *DriftMonitor) Reset() {
	m.lastVideoPTS.Store(0)
	m.lastAudioPTS.Store(0)
	m.hasVideo.Store(false)
	m.hasAudio.Store(false)
	m.highWater.Store(0)
	m.mu.Lock()
	m.driftSince = time.Time{}
	m.mu.Unlock()
}

// HighWaterMs returns the maximum absolute drift ever recorded, in
// milliseconds. Useful for metrics and debug logging.
func (m *DriftMonitor) HighWaterMs() float64 {
	hw := m.highWater.Load()
	// 90kHz → ms
	return float64(hw) * 1000.0 / 90000.0
}

// StartLogging spawns a background goroutine that logs the current A/V
// drift every 60 seconds. The goroutine exits cleanly when ctx is
// cancelled. Calling StartLogging multiple times creates multiple
// goroutines — the caller should only call it once per monitor.
func (m *DriftMonitor) StartLogging(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(driftLogInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				drift := m.DriftMs()
				hw := m.HighWaterMs()
				if drift > 1000 {
					engineLogger.Warn("relay A/V drift",
						"drift_ms", round1(drift),
						"high_water_ms", round1(hw))
				} else {
					engineLogger.Info("relay A/V drift",
						"drift_ms", round1(drift),
						"high_water_ms", round1(hw))
				}
			}
		}
	}()
}

// round1 rounds a float64 to one decimal place for structured logging.
func round1(v float64) float64 {
	return float64(int64(v*10)) / 10
}
