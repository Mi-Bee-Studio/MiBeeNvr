package camera

import (
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/health"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/recorder"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/xiaomi"
)

// Status returns the status of all managed recorders. Lock-free: loads an
// immutable snapshot and reads each recorder's Status() (each recorder guards
// its own status with a short internal mutex). This never blocks writers.
func (cm *CameraManager) Status() map[string]model.RecorderStatus {
	s := cm.loadSnapshot()
	result := make(map[string]model.RecorderStatus, len(s.recorders))
	for id, rec := range s.recorders {
		result[id] = rec.Status()
	}
	return result
}

// CameraStatus returns the status of a single camera recorder. Lock-free read.
func (cm *CameraManager) CameraStatus(cameraID string) model.RecorderStatus {
	rec := cm.snapshotRecorder(cameraID)
	if rec == nil {
		return model.StatusError
	}
	return rec.Status()
}

// SetErrorDetail sets the error detail for a camera. Thread-safe.
func (cm *CameraManager) SetErrorDetail(cameraID string, detail *model.CameraErrorDetail) {
	cm.errorDetailsMu.Lock()
	cm.errorDetails[cameraID] = detail
	cm.errorDetailsMu.Unlock()
}

// GetErrorDetail returns the error detail for a camera, or nil if none. Thread-safe.
func (cm *CameraManager) GetErrorDetail(cameraID string) *model.CameraErrorDetail {
	cm.errorDetailsMu.RLock()
	defer cm.errorDetailsMu.RUnlock()
	return cm.errorDetails[cameraID]
}

// RecorderCount returns the number of managed recorders. Lock-free read.
func (cm *CameraManager) RecorderCount() int {
	return len(cm.loadSnapshot().recorders)
}

// GetRecorder returns the recorder for the given camera ID, or nil if not found.
// Lock-free read: loads the snapshot and indexes it. This is the hot path for
// latest-frame polling (500ms/tile) and must never block.
func (cm *CameraManager) GetRecorder(cameraID string) model.Recorder {
	return cm.snapshotRecorder(cameraID)
}

// SetTestRecorder sets a recorder for testing purposes only.
// This allows tests to inject a recorder into the camera manager.
func (cm *CameraManager) SetTestRecorder(cameraID string, rec model.Recorder) {
	cm.apply(func(s *snapshot) *snapshot {
		s.recorders[cameraID] = rec
		return s
	})
}

// GetHub returns the StreamHub registered for the given camera ID, or nil if
// none exists. This is the read-only lookup consumed by HLS/WebRTC/FLV/WS
// handlers (they fall back to getRecorderHub, but push-only cameras — srt/rtmp —
// expose their hub through this registry). Lock-free read.
func (cm *CameraManager) GetHub(cameraID string) *model.StreamHub {
	return cm.snapshotHub(cameraID)
}

// GetSPS returns the source camera's current H.264 SPS/PPS (raw NALUs, no start
// code) and whether the source is H.264. Used by the relay engine to initialize
// RTMP/RTSP target tracks. Returns nil when the camera is not yet streaming or
// is not H.264.
func (cm *CameraManager) GetSPS(cameraID string) (sps, pps []byte, isH264 bool) {
	rec := cm.GetRecorder(cameraID)
	if rec == nil {
		return nil, nil, false
	}
	switch r := rec.(type) {
	case *recorder.H264Recorder:
		return r.SPS(), r.PPS(), true
	case *recorder.IngestRecorder:
		f, s, p, _ := r.CodecParams()
		if s == nil || p == nil {
			return nil, nil, f != model.FormatH265
		}
		if f == model.FormatH265 {
			// H.265 param sets need the VPS for target-track init — callers
			// wanting H.265 should use GetCodecInfo (which carries VPS).
			return nil, nil, false
		}
		return s, p, true
	case *recorder.ONVIFRecorder:
		if d := r.Delegate(); d != nil {
			if h264, ok := d.(*recorder.H264Recorder); ok {
				return h264.SPS(), h264.PPS(), true
			}
		}
	}
	return nil, nil, false
}

// GetSourceCodec returns the source camera's current video encoding as a
// model.Format string ("h264", "h265", "mjpeg", "jpeg"; "" = unknown — no
// recorder, or a recorder type without a meaningful single codec). Used by the
// relay manager so push targets can fail fast on sources the transcode path
// cannot handle (MJPEG/JPEG, #423).
func (cm *CameraManager) GetSourceCodec(cameraID string) string {
	rec := cm.GetRecorder(cameraID)
	if rec == nil {
		return ""
	}
	switch r := rec.(type) {
	case *recorder.H264Recorder:
		return string(model.FormatH264)
	case *recorder.H265Recorder:
		return string(model.FormatH265)
	case *recorder.MJPEGRecorder:
		return string(model.FormatMJPEG)
	case *recorder.HTTPJPEGRecorder:
		return string(model.EncJPEG)
	case *recorder.ONVIFRecorder:
		if d := r.Delegate(); d != nil {
			switch d.(type) {
			case *recorder.H264Recorder:
				return string(model.FormatH264)
			case *recorder.H265Recorder:
				return string(model.FormatH265)
			case *recorder.HTTPJPEGRecorder:
				return string(model.EncJPEG)
			}
		}
		return ""
	}
	// Generic fallback: recorders that probe their codec at runtime (GB28181,
	// Ingest/SRT, Xiaomi) implement model.HLSProvider.CodecParams.
	if cp, ok := rec.(model.HLSProvider); ok {
		if c, _, _, _ := cp.CodecParams(); c != "" {
			return string(c)
		}
	}
	return ""
}

// GetCodecInfo returns the source camera's current video and audio codec
// parameters as a single CodecInfo struct. Used by the relay engine to
// initialize target tracks with complete codec information.
// Returns a zero-value CodecInfo when the camera is not found or not streaming.
func (cm *CameraManager) GetCodecInfo(cameraID string) model.CodecInfo {
	rec := cm.GetRecorder(cameraID)
	if rec == nil {
		return model.CodecInfo{}
	}

	// Helper to build CodecInfo from audio-getter interface.
	type audioInfo interface {
		AudioCodec() string
		AudioConfig() []byte
		AudioSampleRate() int
		AudioChannels() int
	}

	switch r := rec.(type) {
	case *recorder.H264Recorder:
		ci := model.CodecInfo{
			SPS:    r.SPS(),
			PPS:    r.PPS(),
			IsH264: true,
		}
		ci.AudioCodec = r.AudioCodec()
		ci.AudioConfig = r.AudioConfig()
		ci.AudioSampleRate = r.AudioSampleRate()
		ci.AudioChannels = r.AudioChannels()
		return ci
	case *recorder.H265Recorder:
		ci := model.CodecInfo{
			VPS: r.VPS(),
			SPS: r.SPS(),
			PPS: r.PPS(),
		}
		ci.AudioCodec = r.AudioCodec()
		ci.AudioConfig = r.AudioConfig()
		ci.AudioSampleRate = r.AudioSampleRate()
		ci.AudioChannels = r.AudioChannels()
		return ci
	case *recorder.ONVIFRecorder:
		if d := r.Delegate(); d != nil {
			switch delegate := d.(type) {
			case *recorder.H264Recorder:
				ci := model.CodecInfo{
					SPS:    delegate.SPS(),
					PPS:    delegate.PPS(),
					IsH264: true,
				}
				ci.AudioCodec = delegate.AudioCodec()
				ci.AudioConfig = delegate.AudioConfig()
				ci.AudioSampleRate = delegate.AudioSampleRate()
				ci.AudioChannels = delegate.AudioChannels()
				return ci
			case *recorder.H265Recorder:
				ci := model.CodecInfo{
					VPS: delegate.VPS(),
					SPS: delegate.SPS(),
					PPS: delegate.PPS(),
				}
				ci.AudioCodec = delegate.AudioCodec()
				ci.AudioConfig = delegate.AudioConfig()
				ci.AudioSampleRate = delegate.AudioSampleRate()
				ci.AudioChannels = delegate.AudioChannels()
				return ci
			}
		}
	case *xiaomi.XiaomiRecorder:
		xc, _, _, _ := r.CodecParams()
		ci := model.CodecInfo{
			SPS:    r.SPS(),
			PPS:    r.PPS(),
			VPS:    r.VPS(),
			IsH264: xc != model.FormatH265,
		}
		if ai, ok := rec.(audioInfo); ok {
			ci.AudioCodec = ai.AudioCodec()
			ci.AudioConfig = ai.AudioConfig()
			ci.AudioSampleRate = ai.AudioSampleRate()
			ci.AudioChannels = ai.AudioChannels()
		}
		return ci
	case *recorder.IngestRecorder:
		f, _, _, _ := r.CodecParams()
		ci := model.CodecInfo{
			SPS:    r.SPS(),
			PPS:    r.PPS(),
			VPS:    r.VPS(),
			IsH264: f != model.FormatH265,
		}
		if ai, ok := rec.(audioInfo); ok {
			ci.AudioCodec = ai.AudioCodec()
			ci.AudioConfig = ai.AudioConfig()
			ci.AudioSampleRate = ai.AudioSampleRate()
			ci.AudioChannels = ai.AudioChannels()
		}
		return ci
	}

	// Fallback: try audioInfo interface on any unknown recorder type.
	ci := model.CodecInfo{IsH264: true}
	if ai, ok := rec.(audioInfo); ok {
		ci.AudioCodec = ai.AudioCodec()
		ci.AudioConfig = ai.AudioConfig()
		ci.AudioSampleRate = ai.AudioSampleRate()
		ci.AudioChannels = ai.AudioChannels()
	}
	return ci
}

// SetHealthManager sets the health manager for camera health monitoring.
// Can be called with nil to disable health monitoring.
func (cm *CameraManager) SetHealthManager(m *health.Manager) {
	cm.healthMgr = m
	if m != nil {
		m.SetStatusFunc(cm.statusSnapshot)
	}
}

// statusSnapshot returns the current status of every camera the manager knows
// about, for consumption by the health manager's periodic loop. It merges two
// snapshot sources (both lock-free):
//   - snapshot.recorders: active recorders report their real Status().
//   - snapshot.failedStarts: cameras whose recorder failed to start (e.g. ONVIF
//     endpoint unreachable after an IP change). These are NOT in the recorders
//     map (startRecorder removes them on failure), so without surfacing them
//     here they would be invisible to the health loop → never auto-remediated →
//     never rediscovered. They are reported as StatusError so the existing
//     CheckAll → restart → blacklist → rediscovery chain can self-heal them.
//
// A camera present in BOTH maps (stale failed-start entry + live recorder) is
// dominated by its real recorder status.
func (cm *CameraManager) statusSnapshot() map[string]string {
	// Lock-free: load the immutable snapshot and read each recorder's Status().
	// Each recorder guards its own status with a short internal mutex; this loop
	// never holds any CameraManager lock, so it can never block lifecycle ops.
	s := cm.loadSnapshot()
	result := make(map[string]string, len(s.recorders)+len(s.failedStarts))
	for id, rec := range s.recorders {
		result[id] = string(rec.Status())
	}
	for id := range s.failedStarts {
		// Only surface a failed-start for cameras that still exist in config.
		// A failedStarts entry can outlive its camera if the camera was removed
		// and a stale entry lingered (failedStarts is in-memory only, not
		// persisted); reporting it would surface a phantom camera to the health
		// loop and the /api/health details. The configs map is the source of
		// truth for "does this camera still exist".
		if _, stillConfigured := s.configs[id]; !stillConfigured {
			continue
		}
		if _, exists := result[id]; !exists {
			result[id] = string(model.StatusError)
		}
	}
	return result
}

// markStartFailed records a camera whose recorder failed to start, so that
// statusFunc can surface it to the health manager as StatusError. This is the
// entry point that connects startup failures to the auto-remediation → IP
// rediscovery self-healing chain. Safe to call from the lifecycle actor or any
// goroutine — apply() takes only the short configMu.
func (cm *CameraManager) markStartFailed(cameraID string, err error) {
	cm.apply(func(s *snapshot) *snapshot {
		s.failedStarts[cameraID] = err
		return s
	})
}

// clearStartFailed removes a camera from the failed-start tracking. Called on
// successful (re)start so the camera transitions to normal health monitoring.
// Safe to call from the lifecycle actor or any goroutine.
func (cm *CameraManager) clearStartFailed(cameraID string) {
	cm.apply(func(s *snapshot) *snapshot {
		delete(s.failedStarts, cameraID)
		return s
	})
}
