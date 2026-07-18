package camera

import (
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
		_, s, p, _ := r.CodecParams()
		if s == nil || p == nil {
			return nil, nil, true
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
