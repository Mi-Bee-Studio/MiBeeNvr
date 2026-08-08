package camera

// This file holds the stream-key / push-protocol resolution helpers used by the
// SRT listener and RTMP server to authenticate incoming publishers and map them
// to cameras. All reads are lock-free (snapshot) so the live resolvers reflect
// cameras added at runtime, unlike a snapshot built once at startup.
//
// Extracted from manager.go (#225).

import (
	"strings"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/recorder"
)

// RTMPKeyMap returns a copy of camera_id → stream_key for all RTMP cameras.
// Used by main.go to build the RTMP server's StreamKeyResolver (reverse lookup).
// Lock-free read from the snapshot.
func (cm *CameraManager) RTMPKeyMap() map[string]string {
	s := cm.loadSnapshot()
	out := make(map[string]string, len(s.configs))
	for _, cam := range s.configs {
		if cam.Protocol == string(model.ProtoRTMP) && cam.StreamKey != "" {
			out[cam.ID] = cam.StreamKey
		}
	}
	return out
}

// ResolveStreamKey maps an incoming RTMP stream key to its camera ID (with the
// legacy global rtmp.stream_keys map as a fallback). This is the LIVE resolver
// used by the RTMP server on every publisher connect — it reflects cameras added
// at runtime, unlike a snapshot built once at startup. Lock-free read.
func (cm *CameraManager) ResolveStreamKey(streamKey string) (cameraID string, ok bool) {
	s := cm.loadSnapshot()
	// Per-camera stream_key fields take precedence.
	for _, cam := range s.configs {
		if cam.Protocol == string(model.ProtoRTMP) && cam.StreamKey == streamKey {
			return cam.ID, true
		}
	}
	// Legacy global rtmp.stream_keys map (camera_id → stream_key).
	for camID, key := range cm.cfg.RTMP.StreamKeys {
		if key == streamKey {
			return camID, true
		}
	}
	return "", false
}

// SRTStreamConfigs returns a copy of the SRT push parameters (passphrase,
// stream_id) for all SRT cameras. Used by main.go to keep the SRT listener's
// per-stream encryption map in sync with per-camera config. Lock-free read.
func (cm *CameraManager) SRTStreamConfigs() []config.SRTStream {
	s := cm.loadSnapshot()
	out := make([]config.SRTStream, 0, len(s.configs))
	for _, cam := range s.configs {
		if cam.Protocol == string(model.ProtoSRT) {
			out = append(out, config.SRTStream{
				CameraID:   cam.ID,
				Mode:       "listener",
				Passphrase: cam.SRTPassphrase,
				StreamID:   cam.SRTStreamID,
			})
		}
	}
	return out
}

// GetStreamURL returns the source camera's stream URL (e.g. rtsp://...)
// for FFmpeg relay mode. Returns empty string when the camera is not found.
func (cm *CameraManager) GetStreamURL(cameraID string) string {
	// For RTSP cameras, the config URL IS the stream URL.
	cam := cm.GetCameraConfig(cameraID)
	if cam == nil {
		return ""
	}
	if strings.HasPrefix(cam.URL, "rtsp://") {
		return cam.URL
	}
	// For ONVIF cameras, resolve the RTSP URL from the live recorder.
	rec := cm.GetRecorder(cameraID)
	if rec == nil {
		return ""
	}
	if onvifRec, ok := rec.(*recorder.ONVIFRecorder); ok {
		return onvifRec.RTSPURL()
	}
	return ""
}
