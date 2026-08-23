package camera

// This file holds StreamHub lifecycle + metrics wiring + the periodic stats
// flusher. The hub is the single source of truth for a camera's live frames
// (shared by pull recorders and push/ingest publishers via the SRT listener /
// RTMP server). GetOrCreateHub is the entry point publishers use to obtain the
// SAME hub a recorder owns.
//
// Extracted from manager.go (#225); stats flusher + compositional drop
// callbacks added in #469.

import (
	"log/slog"
	"strconv"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// GetOrCreateHub returns the existing StreamHub for the camera ID, or creates a
// new one (with metrics callbacks wired) if none exists. This is the entry point
// used by the SRT listener and RTMP server: when a publisher pushes a stream,
// they obtain the SAME hub the recorder owns, so frames reach the live
// consumers (HLS/WebRTC/FLV/WS) that subscribe on demand. If a pull recorder
// already created the hub via initStreamHub, that instance is returned.
//
// The check-then-create is atomic via apply() (configMu held only for the map
// swap — hub construction + metrics wiring run inside apply but are pure CPU,
// no I/O). Two concurrent GetOrCreateHub calls for the same camera return the
// same hub: the second's apply sees the first's published snapshot.
func (cm *CameraManager) GetOrCreateHub(cameraID string) *model.StreamHub {
	// Fast path: hub already exists (lock-free).
	if hub := cm.snapshotHub(cameraID); hub != nil {
		return hub
	}
	// Slow path: create under configMu.
	var created *model.StreamHub
	cm.apply(func(s *snapshot) *snapshot {
		if existing, ok := s.hubs[cameraID]; ok {
			// Another goroutine won the race inside apply — reuse its hub.
			created = existing
			return s
		}
		hub := model.NewStreamHub()
		hub.SetCameraID(cameraID)
		hub.SetSource("push")
		// Wire the same observability callbacks as initStreamHub so push hubs are
		// instrumented identically to pull hubs.
		wireHubMetrics(hub, cameraID, cm.metrics)
		s.hubs[cameraID] = hub
		created = hub
		return s
	})
	return created
}

// wireHubMetrics attaches the standard StreamHub observability callbacks
// (frame counters, drop counters, buffer-depth gauges). Shared by
// initStreamHub and GetOrCreateHub so pull and push hubs are instrumented
// identically (#469).
func wireHubMetrics(hub *model.StreamHub, cameraID string, m *metrics.Metrics) {
	if m == nil {
		return
	}
	hub.OnBroadcast = func(cid string, isIDR bool) {
		m.StreamHubFramesInTotal.WithLabelValues(cid).Inc()
	}
	// AddOnDrop (callback list) instead of assigning the field — a single field
	// let protocol managers (HLS) silently clobber the Prometheus wiring (#469 Phase 0).
	hub.AddOnDrop(func(consumerID string, isIDR bool) {
		m.StreamHubFramesDropped.WithLabelValues(cameraID, consumerID, strconv.FormatBool(isIDR)).Inc()
	})
	hub.OnBroadcastAudio = func(cid string, codec string) {
		m.AudioFramesTotal.WithLabelValues(cid, codec).Inc()
	}
	hub.OnAudioDrop = func(cid string) {
		m.AudioFramesDroppedTotal.WithLabelValues(cid).Inc()
	}
	hub.OnBufferDepth = func(cid, consumerID string, depth int) {
		m.StreamHubBufferDepth.WithLabelValues(cid, consumerID).Set(float64(depth))
	}
	hub.OnJitterBufferDepth = func(cid string, depth int) {
		m.JitterBufferDepth.WithLabelValues(cid).Set(float64(depth))
	}
	hub.OnJitterReorder = func(cid string) {
		m.JitterBufferReordersTotal.WithLabelValues(cid).Inc()
	}
	// #469: previously defined but never wired.
	hub.OnDropRate = func(consumerID string, rate float64) {
		m.StreamHubDropRateExceededTotal.WithLabelValues(cameraID, consumerID).Inc()
	}
	hub.OnJitterBufferFlush = func(cid string, count int) {
		m.JitterBufferFlushesTotal.WithLabelValues(cid).Inc()
		slog.Debug("jitter_buffer_flush", "camera_id", cid, "frames", count)
	}
}

// hubStatsFlushInterval is how often hub per-consumer atomics are exported to
// Prometheus. Deliberately coarse: the hot path pays nothing per frame, and
// dashboards get 15s granularity — plenty for drop/dwell trend analysis.
const hubStatsFlushInterval = 15 * time.Second

// startHubStatsFlusher launches the periodic exporter of hub per-consumer
// counters to Prometheus (#469 Phase 1). Idempotent; stopped by
// stopHubStatsFlusher.
func (cm *CameraManager) startHubStatsFlusher() {
	cm.hubFlusherOnce.Do(func() {
		stop := make(chan struct{})
		cm.hubFlusherMu.Lock()
		cm.hubFlusherStop = stop
		cm.hubFlusherMu.Unlock()
		go func() {
			ticker := time.NewTicker(hubStatsFlushInterval)
			defer ticker.Stop()
			for {
				select {
				case <-stop:
					return
				case <-ticker.C:
					cm.flushHubStats()
				}
			}
		}()
	})
}

// stopHubStatsFlusher terminates the stats flusher goroutine.
func (cm *CameraManager) stopHubStatsFlusher() {
	cm.hubFlusherMu.Lock()
	defer cm.hubFlusherMu.Unlock()
	if cm.hubFlusherStop != nil {
		close(cm.hubFlusherStop)
		cm.hubFlusherStop = nil
	}
}

// flushHubStats exports per-consumer sends/dwell and hub-level bytes from every
// registered hub's atomics into Prometheus. Counters are emitted as deltas
// since the previous flush so rate() works in dashboards.
func (cm *CameraManager) flushHubStats() {
	m := cm.metrics
	if m == nil {
		return
	}
	hubs := cm.snapshotHubs()
	cm.hubFlusherMu.Lock()
	prevState := cm.hubFlushLast
	cm.hubFlushLast = make(map[string][2]int64, len(prevState))
	prevBytes := cm.hubBytesLast
	cm.hubBytesLast = make(map[string]int64, len(prevBytes))
	cm.hubFlusherMu.Unlock()

	for cameraID, hub := range hubs {
		stats := hub.Snapshot()
		if base, ok := prevBytes[cameraID]; ok {
			if d := stats.BytesIn - base; d > 0 {
				m.StreamHubBytesInTotal.WithLabelValues(cameraID).Add(float64(d))
			}
		}
		cm.hubBytesLast[cameraID] = stats.BytesIn
		for _, c := range stats.Consumers {
			key := cameraID + "\x00" + c.ID
			if prev, ok := prevState[key]; ok {
				if d := c.Sends - prev[0]; d > 0 {
					m.StreamHubFramesSentTotal.WithLabelValues(cameraID, c.ID).Add(float64(d))
				}
			}
			cm.hubFlushLast[key] = [2]int64{c.Sends, c.Bytes}
			m.StreamHubHopDwellAvgMS.WithLabelValues(cameraID, c.ID).Set(c.DwellAvgMS)
			m.StreamHubHopDwellMaxMS.WithLabelValues(cameraID, c.ID).Set(c.DwellMaxMS)
		}
	}
}
