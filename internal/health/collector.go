package health

import (
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
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

	eventHandler func(cameraID string, event model.HealthEvent)
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
		cameras:                make(map[string]*cameraStats),
		prevBitrate:            make(map[string]float64),
		eventHandler:           handler,
	}
}

// OnFrame returns a frame callback for the given camera.
// The callback uses only atomic operations — no mutex, no allocations.
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
	s.mu.Unlock()

	windowSeconds := s.windowSize.Seconds()
	if windowSeconds <= 0 {
		windowSeconds = 1
	}

	for cameraID, stats := range snapshot {
		frameCount := stats.frameCount.Swap(0)
		byteCount := stats.byteCount.Swap(0)

		fps := float64(frameCount) / windowSeconds
		bitrate := float64(byteCount*8) / windowSeconds

		// Check FPS threshold
		if s.minFPS > 0 && fps < s.minFPS && frameCount > 0 {
			s.emitEvent(cameraID, model.HealthEvent{
				CameraID:  cameraID,
				EventType: string(model.HealthEventStreamAnomaly),
				Status:    string(model.HealthStatusWarning),
				Message:   "Low FPS detected",
				Metadata:  mustJSON(map[string]any{"fps": fps, "threshold": s.minFPS}),
			})
		}

		// Check bitrate change
		s.mu.Lock()
		prevBps, had := s.prevBitrate[cameraID]
		s.prevBitrate[cameraID] = bitrate
		s.mu.Unlock()

		if had && prevBps > 0 {
			change := bitrate - prevBps
			if change < 0 {
				change = -change
			}
			change /= prevBps
			if change > s.bitrateChangeThreshold {
				s.emitEvent(cameraID, model.HealthEvent{
					CameraID:  cameraID,
					EventType: string(model.HealthEventStreamAnomaly),
					Status:    string(model.HealthStatusWarning),
					Message:   "Bitrate anomaly detected",
					Metadata:  mustJSON(map[string]any{"bitrate": bitrate, "prev": prevBps, "change": change}),
				})
			}
		}

		// Check IDR interval
		if v := stats.lastIDRTime.Load(); v != nil {
			if lastIDR, ok := v.(time.Time); ok {
				since := time.Since(lastIDR)
				if since > s.maxIDRInterval {
					s.emitEvent(cameraID, model.HealthEvent{
						CameraID:  cameraID,
						EventType: string(model.HealthEventStreamAnomaly),
						Status:    string(model.HealthStatusWarning),
						Message:   "IDR interval too long",
						Metadata: mustJSON(map[string]any{
							"idr_interval": since.String(),
							"max":          s.maxIDRInterval.String(),
						}),
					})
				}
			}
		}
	}
}

// RemoveCamera removes tracking for a camera.
func (s *StreamStatsCollector) RemoveCamera(cameraID string) {
	s.mu.Lock()
	delete(s.cameras, cameraID)
	delete(s.prevBitrate, cameraID)
	s.mu.Unlock()
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

	// Clear prevBitrate entry
	s.mu.Lock()
	delete(s.prevBitrate, cameraID)
	s.mu.Unlock()
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
