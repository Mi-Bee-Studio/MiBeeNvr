package webrtc

import (
	"log/slog"
)

// Congestion control for WebRTC egress: a sliding-window tracker that measures
// the drop rate and decides when to skip non-IDR frames so a slow peer cannot
// stall the sender. Split out of manager.go.

// newCongestionTracker creates a tracker with the given sliding window size.
func newCongestionTracker(windowSize int) *congestionTracker {
	return &congestionTracker{
		windowSize: windowSize,
		window:     make([]bool, windowSize),
	}
}

// recordSent records a successfully sent frame.
func (ct *congestionTracker) recordSent() {
	ct.record(true)
}

// recordDropped records a dropped frame.
func (ct *congestionTracker) recordDropped() {
	ct.record(false)
}

// record adds an entry to the sliding window.
func (ct *congestionTracker) record(sent bool) {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	ct.window[ct.windowPos] = sent
	ct.windowPos = (ct.windowPos + 1) % ct.windowSize
	if ct.windowCount < ct.windowSize {
		ct.windowCount++
	}
}

// dropRate returns the current drop rate (0.0 to 1.0).
func (ct *congestionTracker) dropRate() float64 {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	return ct.dropRateLocked()
}

// dropRateLocked is the lock-free variant — caller must hold ct.mu.
func (ct *congestionTracker) dropRateLocked() float64 {
	if ct.windowCount == 0 {
		return 0
	}
	var drops int
	for i := range ct.windowCount {
		if !ct.window[i] {
			drops++
		}
	}
	return float64(drops) / float64(ct.windowCount)
}

// shouldSkipFrame returns true if a non-IDR frame should be skipped due to congestion.
// IDR frames are never skipped. When congested, skips every other non-IDR frame.
func (ct *congestionTracker) shouldSkipFrame(isIDR bool) bool {
	ct.mu.Lock()
	defer ct.mu.Unlock()

	// IDR frames are never skipped
	if isIDR {
		return false
	}

	// Empty window — no congestion
	if ct.windowCount == 0 {
		return false
	}

	rate := ct.dropRateLocked()

	// State transitions
	if !ct.congested && rate > highDropRateThreshold {
		ct.congested = true
		ct.skipCounter = 0
		slog.Debug("webrtc_congestion_start",
			"drop_rate", rate,
			"window_count", ct.windowCount)
	} else if ct.congested && rate < lowDropRateThreshold {
		ct.congested = false
		slog.Debug("webrtc_congestion_end",
			"drop_rate", rate,
			"window_count", ct.windowCount)
	}

	if !ct.congested {
		return false
	}

	// Alternating skip: skip, send, skip, send...
	ct.skipCounter++
	return ct.skipCounter%2 == 1
}
