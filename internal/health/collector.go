package health

import (
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

const (
	// minConsecutiveChecks is the number of consecutive anomalous checks required
	// before emitting a stream_anomaly event. This eliminates transient spikes.
	minConsecutiveChecks = 2

	anomalyKeyFPS     = "fps"
	anomalyKeyBitrate = "bitrate"
	anomalyKeyIDR     = "idr"
)

// StreamStats holds computed statistics for a camera stream.
type StreamStats struct {
	FPS         float64
	Bitrate     float64 // bits per second
	IDRInterval float64 // seconds since last IDR
	FrameCount  int64
	TotalBytes  int64
	LastIDRTime time.Time
}

// cameraStats holds per-camera atomic counters.
// All fields use atomic operations — no mutex in the hot path.
type cameraStats struct {
	frameCount  atomic.Int64
	byteCount   atomic.Int64
	idrCount    atomic.Int64
	lastIDRTime atomic.Value // stores time.Time
}

// StreamStatsCollector collects stream statistics from frame callbacks.
// It uses atomic counters in the frame callback (non-blocking) and
// periodic CheckAndReset calls to compute stats and detect anomalies.
type StreamStatsCollector struct {
	bitrateChangeThreshold float64
	minFPS                 float64
	maxIDRInterval         time.Duration
	windowSize             time.Duration

	mu      sync.Mutex
	cameras map[string]*cameraStats
	prevBitrate map[string]float64
	consecutiveAnomaly map[string]map[string]int // cameraID → anomalyKey → count

	// Per-camera threshold overrides
	cameraOverrides map[string]*collectorOverride

	eventHandler func(cameraID string, event model.HealthEvent)
	m            *metrics.Metrics
}

// collectorOverride holds per-camera threshold overrides for the collector.
type collectorOverride struct {
	bitrateChangeThreshold float64
	minFPS                 float64
	maxIDRInterval         time.Duration
}

// NewStreamStatsCollector creates a new stats collector.
func NewStreamStatsCollector(
	bitrateChangeThreshold float64,
	minFPS float64,
	maxIDRInterval time.Duration,
	windowSize time.Duration,
	handler func(string, model.HealthEvent),
) *StreamStatsCollector {
	return &StreamStatsCollector{
		bitrateChangeThreshold: bitrateChangeThreshold,
		minFPS:                 minFPS,
		maxIDRInterval:         maxIDRInterval,
		windowSize:             windowSize,
		cameras:         make(map[string]*cameraStats),
		prevBitrate:     make(map[string]float64),
		cameraOverrides: make(map[string]*collectorOverride),
		consecutiveAnomaly: make(map[string]map[string]int),
		eventHandler:    handler,
	}
}

// OnFrame returns a frame callback for the given camera.
// The callback uses only atomic operations — no mutex, no allocations.

// SetMetrics sets the Prometheus metrics instance for exposing stream stats as gauges.
func (s *StreamStatsCollector) SetMetrics(m *metrics.Metrics) {
	s.m = m
}

func (s *StreamStatsCollector) OnFrame(cameraID string) func(pts int64, au [][]byte) {
	stats := s.getOrCreateStats(cameraID)
	return func(pts int64, au [][]byte) {
		stats.frameCount.Add(1)

		totalBytes := 0
		for _, nalu := range au {
			totalBytes += len(nalu)
		}
		stats.byteCount.Add(int64(totalBytes))

		// Detect IDR frames
		// H.264: nal_unit_type = nalu[0] & 0x1F, IDR = 5
		// H.265: nal_unit_type = (nalu[0] >> 1) & 0x3F, IDR_W_RADL = 19, IDR_N_LP = 20
		if len(au) > 0 && len(au[0]) > 0 {
			naluType := au[0][0] & 0x1F // H.264
			if naluType == 5 {          // H.264 IDR
				now := time.Now()
				stats.lastIDRTime.Store(now)
				stats.idrCount.Add(1)
			} else {
				// Check H.265 IDR
				h265Type := (au[0][0] >> 1) & 0x3F
				if h265Type == 19 || h265Type == 20 { // H.265 IDR
					now := time.Now()
					stats.lastIDRTime.Store(now)
					stats.idrCount.Add(1)
				}
			}
		}
	}
}

func (s *StreamStatsCollector) getOrCreateStats(cameraID string) *cameraStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.cameras[cameraID]; !ok {
		cs := &cameraStats{}
		cs.lastIDRTime.Store(time.Now())
		s.cameras[cameraID] = cs
	}
	return s.cameras[cameraID]
}

// GetStats returns current stats for a camera (periodic, not hot path).
func (s *StreamStatsCollector) GetStats(cameraID string) StreamStats {
	s.mu.Lock()
	stats, ok := s.cameras[cameraID]
	s.mu.Unlock()
	if !ok {
		return StreamStats{}
	}

	windowSeconds := s.windowSize.Seconds()
	if windowSeconds <= 0 {
		windowSeconds = 1
	}

	frameCount := stats.frameCount.Load()
	byteCount := stats.byteCount.Load()

	var idrInterval float64
	var lastIDR time.Time
	if v := stats.lastIDRTime.Load(); v != nil {
		if t, ok := v.(time.Time); ok {
			idrInterval = time.Since(t).Seconds()
			lastIDR = t
		}
	}

	return StreamStats{
		FPS:         float64(frameCount) / windowSeconds,
		Bitrate:     float64(byteCount*8) / windowSeconds,
		IDRInterval: idrInterval,
		FrameCount:  frameCount,
		TotalBytes:  byteCount,
		LastIDRTime: lastIDR,
	}
}

// CheckAndReset is called periodically to check thresholds and reset counters.
// It swaps counters to zero and computes per-window stats.
func (s *StreamStatsCollector) CheckAndReset() {
	s.mu.Lock()
	snapshot := make(map[string]*cameraStats, len(s.cameras))
	for id, cs := range s.cameras {
		snapshot[id] = cs
	}
	overrides := make(map[string]*collectorOverride, len(s.cameraOverrides))
	for id, co := range s.cameraOverrides {
		overrides[id] = co
	}
	s.mu.Unlock()

	windowSeconds := s.windowSize.Seconds()
	if windowSeconds <= 0 {
		windowSeconds = 1
	}

	for cameraID, stats := range snapshot {
		// Resolve per-camera thresholds, falling back to global
		btcThreshold := s.bitrateChangeThreshold
		minFPS := s.minFPS
		maxIDR := s.maxIDRInterval
		if co, ok := overrides[cameraID]; ok {
			if co.bitrateChangeThreshold > 0 {
				btcThreshold = co.bitrateChangeThreshold
			}
			if co.minFPS > 0 {
				minFPS = co.minFPS
			}
			if co.maxIDRInterval > 0 {
				maxIDR = co.maxIDRInterval
			}
		}

		frameCount := stats.frameCount.Swap(0)
		byteCount := stats.byteCount.Swap(0)

		fps := float64(frameCount) / windowSeconds
		bitrate := float64(byteCount*8) / windowSeconds

		// Prometheus bridge: expose stream stats as gauges
		if s.m != nil {
			s.m.StreamFPS.WithLabelValues(cameraID).Set(fps)
			s.m.StreamBitrateKbps.WithLabelValues(cameraID).Set(bitrate / 1000)
		}

		// Check FPS threshold
		if minFPS > 0 && fps < minFPS && frameCount > 0 {
			streak := s.incrementAnomalyStreak(cameraID, anomalyKeyFPS)
			if streak >= minConsecutiveChecks {
				s.emitEvent(cameraID, model.HealthEvent{
					CameraID:  cameraID,
					EventType: string(model.HealthEventStreamAnomaly),
					Status:    string(model.HealthStatusWarning),
					Message:   "Low FPS detected",
					Metadata:  mustJSON(map[string]any{"fps": fps, "threshold": minFPS}),
				})
			}
		} else {
			s.resetAnomalyStreak(cameraID, anomalyKeyFPS)
		}

		// Check bitrate change
		s.mu.Lock()
		prevBps, had := s.prevBitrate[cameraID]
		s.prevBitrate[cameraID] = bitrate
		s.mu.Unlock()

		// Only compare bitrate when both values are non-zero.
		// A zero bitrate means the stream dropped (connection_lost handles this).
		bitrateAnomalous := false
		if had && prevBps > 0 && bitrate > 0 {
			change := bitrate - prevBps
			if change < 0 {
				change = -change
			}
			change /= prevBps
			if change > btcThreshold {
				bitrateAnomalous = true
				streak := s.incrementAnomalyStreak(cameraID, anomalyKeyBitrate)
				if streak >= minConsecutiveChecks {
					s.emitEvent(cameraID, model.HealthEvent{
						CameraID:  cameraID,
						EventType: string(model.HealthEventStreamAnomaly),
						Status:    string(model.HealthStatusWarning),
						Message:   "Bitrate anomaly detected",
						Metadata:  mustJSON(map[string]any{"bitrate": bitrate, "prev": prevBps, "change": change}),
					})
				}
			}
		}
		if !bitrateAnomalous {
			s.resetAnomalyStreak(cameraID, anomalyKeyBitrate)
		}

		// Check IDR interval
		if v := stats.lastIDRTime.Load(); v != nil {
			if lastIDR, ok := v.(time.Time); ok {
				since := time.Since(lastIDR)
				if since > maxIDR {
					streak := s.incrementAnomalyStreak(cameraID, anomalyKeyIDR)
					if streak >= minConsecutiveChecks {
						s.emitEvent(cameraID, model.HealthEvent{
							CameraID:  cameraID,
							EventType: string(model.HealthEventStreamAnomaly),
							Status:    string(model.HealthStatusWarning),
							Message:   "IDR interval too long",
							Metadata: mustJSON(map[string]any{
								"idr_interval": since.String(),
								"max":          maxIDR.String(),
							}),
						})
					}
				} else {
				s.resetAnomalyStreak(cameraID, anomalyKeyIDR)
			}
			// Prometheus bridge: expose IDR interval as gauge
			if s.m != nil {
				s.m.StreamIDRIntervalSeconds.WithLabelValues(cameraID).Set(time.Since(lastIDR).Seconds())
				}
			}
		}
	}

	// Prune debounce entries for cameras that no longer exist.
	s.mu.Lock()
	for id := range s.consecutiveAnomaly {
		if _, ok := snapshot[id]; !ok {
			delete(s.consecutiveAnomaly, id)
		}
	}
	s.mu.Unlock()
}

// RemoveCamera removes tracking for a camera.
func (s *StreamStatsCollector) RemoveCamera(cameraID string) {
	s.mu.Lock()
	delete(s.cameras, cameraID)
	delete(s.prevBitrate, cameraID)
	delete(s.cameraOverrides, cameraID)
	delete(s.consecutiveAnomaly, cameraID)
	s.mu.Unlock()
}

// SetCameraOverride sets per-camera threshold overrides for the collector.
func (s *StreamStatsCollector) SetCameraOverride(cameraID string, btcThreshold float64, minFPS float64, maxIDR time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cameraOverrides[cameraID] = &collectorOverride{
		bitrateChangeThreshold: btcThreshold,
		minFPS:                 minFPS,
		maxIDRInterval:         maxIDR,
	}
}

// ResetCameraState resets per-camera state on reconnect.
// It resets lastIDRTime, clears prevBitrate, and zeroes atomic counters
// to prevent false "IDR interval too long" alerts after reconnection.
func (s *StreamStatsCollector) ResetCameraState(cameraID string) {
	stats := s.getOrCreateStats(cameraID)

	// Reset lastIDRTime to now (same pattern as freeze.go:65)
	stats.lastIDRTime.Store(time.Now())

	// Reset atomic counters
	stats.frameCount.Store(0)
	stats.byteCount.Store(0)

	// Clear prevBitrate and anomaly debounce state
	s.mu.Lock()
	delete(s.prevBitrate, cameraID)
	delete(s.consecutiveAnomaly, cameraID)
	s.mu.Unlock()
}

// incrementAnomalyStreak increments the consecutive anomaly counter and returns the new count.
func (s *StreamStatsCollector) incrementAnomalyStreak(cameraID, key string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.consecutiveAnomaly[cameraID] == nil {
		s.consecutiveAnomaly[cameraID] = make(map[string]int)
	}
	s.consecutiveAnomaly[cameraID][key]++
	return s.consecutiveAnomaly[cameraID][key]
}

// resetAnomalyStreak clears the consecutive anomaly counter for a given anomaly type.
func (s *StreamStatsCollector) resetAnomalyStreak(cameraID, key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m, ok := s.consecutiveAnomaly[cameraID]; ok {
		delete(m, key)
	}
}

func (s *StreamStatsCollector) emitEvent(cameraID string, event model.HealthEvent) {
	if s.eventHandler != nil {
		s.eventHandler(cameraID, event)
	}
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}
