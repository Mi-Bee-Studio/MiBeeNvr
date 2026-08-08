package camera

// This file holds the lock-free config/manager accessor methods — small reads
// over the immutable snapshot (configs map) plus typed recorder casts and
// sub-manager getters. None of these perform I/O or take a registry lock.
//
// Extracted from manager.go (#225).

import (
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
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
