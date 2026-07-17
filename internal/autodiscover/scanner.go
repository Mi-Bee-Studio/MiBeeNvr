package autodiscover

import (
	"context"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/onvif"
)

// Scanner is the active discovery mode: it periodically issues a multicast
// WS-Discovery Probe sweep and feeds every discovered device to the Adder. This
// complements the passive HelloListener — it catches devices that did not send
// a Hello (e.g. devices already powered on before the NVR started, or after a
// silent IP change) and serves as a backstop if the listener fails to bind 3702.
//
// Each sweep is a full onvif.Discover call (multicast Probe + parallel
// GetDeviceInformation enrichment). The interval is bounded by config
// (≥30s, default 60s) to respect RPi-3B constraints.
type Scanner struct {
	adder    *Adder
	interval time.Duration
}

// NewScanner constructs a Scanner. intervalSeconds should come from
// config.AutoDiscoverConfig.ScanIntervalSeconds (ApplyDefaults floors it at 30).
func NewScanner(adder *Adder, intervalSeconds int) *Scanner {
	if intervalSeconds < 30 {
		intervalSeconds = 30
	}
	return &Scanner{
		adder:    adder,
		interval: time.Duration(intervalSeconds) * time.Second,
	}
}

// Run executes discovery sweeps until ctx is cancelled. The first sweep is
// delayed by a short grace period (10s) so that on NVR startup the resident
// services (camera manager, health) settle before the first multicast burst,
// and so that a device already announcing itself via Hello is caught by the
// passive listener first (avoiding a redundant Probe-based enrollment race).
func (s *Scanner) Run(ctx context.Context) {
	// Grace period before the first sweep. Use a timer (not a leading ticker
	// tick) so the interval between subsequent sweeps is exactly `interval`.
	const startupGrace = 10 * time.Second
	timer := time.NewTimer(startupGrace)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		s.sweep(ctx)
		timer.Reset(s.interval)
	}
}

// sweep performs one discovery cycle: multicast Probe + enrichment, then feed
// each result to the Adder. The Adder owns dedup, so duplicates across sweeps
// (or vs. the passive listener) are silently skipped.
//
// Each device is processed in its own goroutine so a slow/hung device does not
// block the others — bounded only by the 5s enrichment timeout inside the Adder.
func (s *Scanner) sweep(ctx context.Context) {
	// 8s timeout covers a 5s multicast Probe window + a few seconds of
	// enrichment headroom. Longer than the listener's per-device path because
	// Discover enriches in bulk.
	sweepCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	result := onvif.Discover(sweepCtx, 5*time.Second)
	for _, dev := range result.Devices {
		dev := dev // capture for goroutine
		go s.adder.HandleDiscovered(ctx, dev)
	}
}
