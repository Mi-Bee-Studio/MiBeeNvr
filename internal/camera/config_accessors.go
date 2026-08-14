package camera

// This file holds the lock-free config/manager accessor methods — small reads
// over the immutable snapshot (configs map) plus typed recorder casts and
// sub-manager getters. None of these perform I/O or take a registry lock.
//
// Extracted from manager.go (#225).

import (
	"context"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/recorder"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/timelapse"
)

// CameraCount returns the number of configured cameras (O(1), no DB query).
// Used by stats endpoints that only need the count, avoiding a redundant
// ListCameras DB round-trip per request.
func (cm *CameraManager) CameraCount() int {
	return len(cm.loadSnapshot().configs)
}

// GetCameraConfig returns the config for the given camera ID, or nil if not found.
// Lock-free read from the immutable snapshot.
func (cm *CameraManager) GetCameraConfig(cameraID string) *config.CameraConfig {
	return cm.snapshotConfig(cameraID)
}

// GetIngestRecorder returns the IngestRecorder for a camera if it is one, else
// nil. Convenience for the SRT/RTMP servers that need to call WriteNALU /
// OnDisconnect on push cameras.
func (cm *CameraManager) GetIngestRecorder(cameraID string) *recorder.IngestRecorder {
	rec, ok := cm.GetRecorder(cameraID).(*recorder.IngestRecorder)
	if !ok {
		return nil
	}
	return rec
}

// GetTimelapseMergeMgr returns the timelapse rolling merge manager, or nil if not set.
func (cm *CameraManager) GetTimelapseMergeMgr() *timelapse.RollingMergeManager {
	return cm.timelapseMergeMgr
}

// GetGB28181Recorder returns the GB28181Recorder for a camera if it is one, else
// nil. Convenience for the GB28181 SessionManager that needs to call OnInvite/
// OnBye on GB28181 cameras.
func (cm *CameraManager) GetGB28181Recorder(cameraID string) *recorder.GB28181Recorder {
	rec, ok := cm.GetRecorder(cameraID).(*recorder.GB28181Recorder)
	if !ok {
		return nil
	}
	return rec
}

// EnsureGB28181Camera creates a camera entry for a GB28181 device if one
// doesn't already exist. Called by the SIP server on first REGISTER so
// GB28181 cameras auto-appear in the Cameras list — matching ONVIF auto-add.
// Idempotent: if a camera with matching GB28181.DeviceID exists, returns nil.
func (cm *CameraManager) EnsureGB28181Camera(deviceID, channelID string) error {
	// Check if a camera for this device already exists.
	snap := cm.loadSnapshot()
	for _, cfg := range snap.configs {
		if cfg.GB28181.DeviceID == deviceID {
			return nil // Already enrolled
		}
	}

	cam := config.CameraConfig{
		ID:       "gb-" + deviceID,
		Name:     "GB28181 " + deviceID,
		Protocol: string(model.ProtoGB28181),
		GB28181: config.GB28181ChannelConfig{
			DeviceID:  deviceID,
			ChannelID: channelID,
		},
	}
	_, err := cm.AddCamera(context.Background(), cam)
	return err
}
