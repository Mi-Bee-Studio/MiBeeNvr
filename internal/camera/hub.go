package camera

// This file holds StreamHub lifecycle + metrics wiring. The hub is the single
// source of truth for a camera's live frames (shared by pull recorders and
// push/ingest publishers via the SRT listener / RTMP server). GetOrCreateHub is
// the entry point publishers use to obtain the SAME hub a recorder owns.
//
// Extracted from manager.go (#225).

import (
	"sync/atomic"
	"time"

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
		// Wire the same observability callbacks as initStreamHub so push hubs are
		// instrumented identically to pull hubs.
		cm.wireHubMetrics(hub, cameraID, string(model.ProtoSRT))
		s.hubs[cameraID] = hub
		created = hub
		return s
	})
	return created
}

// wireHubMetrics attaches the standard StreamHub observability callbacks
// (frame counters, drop counters, buffer-depth gauges). Reads only
// construction-time fields (cm.metrics, cm.frameSampleCounter).
func (cm *CameraManager) wireHubMetrics(hub *model.StreamHub, cameraID, protocol string) {
	m := cm.metrics
	if m == nil {
		return
	}
	sampleCounter := &cm.frameSampleCounter
	hub.OnBroadcast = func(cid string, isIDR bool) {
		m.StreamHubFramesInTotal.WithLabelValues(cid).Inc()
		if sampleCounter != nil {
			count := atomic.AddUint64(sampleCounter, 1)
			if count%100 == 0 {
				start := time.Now()
				m.FrameProcessingDurationSeconds.WithLabelValues(cid, protocol).Observe(time.Since(start).Seconds())
			}
		}
	}
	hub.OnDrop = func(consumerID string) {
		m.StreamHubFramesDropped.WithLabelValues(cameraID, consumerID, "false").Inc()
	}
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
}
