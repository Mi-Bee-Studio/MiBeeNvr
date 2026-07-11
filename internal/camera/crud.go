package camera

import (
	"context"
	"fmt"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/recorder"
)

// AddCamera adds a new camera to the manager, starts its recorder, and persists.
// If cam.ID is empty, a new ID is generated automatically.
// Returns the camera ID.
func (cm *CameraManager) AddCamera(ctx context.Context, cam config.CameraConfig) (string, error) {
	if cam.ID == "" {
		cam.ID = GenerateCameraID()
	}

	cm.mu.Lock()
	defer cm.mu.Unlock()

	// Check for duplicate ID
	for _, existing := range cm.cfg.Cameras {
		if existing.ID == cam.ID {
			return "", &model.CameraAlreadyExistsError{CameraID: cam.ID}
		}
	}

	// Append to config
	cm.cfg.Cameras = append(cm.cfg.Cameras, cam)

	// Persist to database
	if cm.db != nil {
		if err := cm.db.UpsertCamera(ctx, cam.ID, cam.Name, string(cam.Protocol), cam.Encoding, cam.URL, cam.Username, cam.Password, cam.ONVIFEndpoint, cam.ProfileToken, cam.StreamEncoding); err != nil {
			logger.Error("failed to upsert camera record", "camera_id", cam.ID, "error", err)
		}
	}

	// Start recorder if protocol supports it
	segDur, err := time.ParseDuration(cm.cfg.Storage.SegmentDuration)
	if err != nil {
		segDur = recorder.DefaultSegmentDur
	}
	if err := cm.startRecorder(ctx, cam, segDur); err != nil {
		logger.Error("failed to start recorder", "error", err)
	}

	// Persist config to disk (rollback on failure)
	if err := cm.persistConfig(); err != nil {
		// Rollback: remove the camera we just added
		for i, c := range cm.cfg.Cameras {
			if c.ID == cam.ID {
				cm.cfg.Cameras = append(cm.cfg.Cameras[:i], cm.cfg.Cameras[i+1:]...)
				break
			}
		}
		return "", fmt.Errorf("failed to persist config: %w", err)
	}

	// Auto-populate SnapshotURL for ONVIF cameras (non-blocking)
	if cam.Protocol == string(model.ProtoONVIF) && cam.SnapshotURL == "" {
		go cm.autoPopulateSnapshotURL(context.Background(), cam.ID)
	}

	// Reconcile push-out relay targets. Run in a goroutine so it does NOT execute
	// under cm.mu — SetCameraTargets calls back into GetHub which re-locks cm.mu
	// (RLock), and re-entering under a held Lock would self-deadlock (Go's
	// RWMutex is not reentrant).
	if cm.relayMgr != nil {
		cameraID := cam.ID
		targets := append([]config.PushTargetConfig(nil), cam.PushTargets...)
		go cm.relayMgr.SetCameraTargets(cameraID, targets)
	}

	return cam.ID, nil
}

// RemoveCamera removes a camera from the manager, stops its recorder, and removes it from config.
// Does NOT delete the camera record from the database.
func (cm *CameraManager) RemoveCamera(ctx context.Context, cameraID string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	// Find camera index and save original for potential rollback
	idx := -1
	for i, cam := range cm.cfg.Cameras {
		if cam.ID == cameraID {
			idx = i
			break
		}
	}
	if idx == -1 {
		return &model.CameraNotFoundError{CameraID: cameraID}
	}
	savedCam := cm.cfg.Cameras[idx]

	// Stop and remove recorder if running
	if rec, ok := cm.recorders[cameraID]; ok {
		if err := rec.Stop(); err != nil {
			logger.Warn("failed to stop recorder", "camera_id", cameraID, "error", err)
		}
		// Notify health manager of camera removal
		cm.healthMgr.OnCameraRemoved(cameraID, rec)
		delete(cm.recorders, cameraID)
		delete(cm.hubRegistry, cameraID)
		if cm.metrics != nil {
			cm.metrics.ActiveCameras.Dec()
		}
	}
	// Stop all relay targets for this camera.
	if cm.relayMgr != nil {
		cm.relayMgr.RemoveCamera(cameraID)
	}

	// Stop keyframe extractor if running
	if ext, ok := cm.keyframeExtractors[cameraID]; ok {
		delete(cm.keyframeExtractors, cameraID)
		if err := ext.Stop(); err != nil {
			logger.Warn("failed to stop keyframe extractor", "camera_id", cameraID, "error", err)
		}
	}

	// Stop frame poller if running
	cm.stopTimelapseFramePoller(cameraID)

	// Remove from config slice
	cm.cfg.Cameras = append(cm.cfg.Cameras[:idx], cm.cfg.Cameras[idx+1:]...)

	// Persist config to disk (rollback on failure)
	if err := cm.persistConfig(); err != nil {
		// Rollback: re-insert camera at original position
		cm.cfg.Cameras = append(cm.cfg.Cameras[:idx], append([]config.CameraConfig{savedCam}, cm.cfg.Cameras[idx:]...)...)
		return fmt.Errorf("failed to persist config: %w", err)
	}

	return nil
}

// ArchiveCamera archives a camera: stops recorder, merges segments, marks archived in DB,
// marks all recordings archived, and removes from config YAML.
// The camera row and recordings are preserved in the database.
// Merge failure is non-blocking (logged but continues).
func (cm *CameraManager) ArchiveCamera(ctx context.Context, cameraID string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	// Verify camera exists in config
	idx := -1
	for i, cam := range cm.cfg.Cameras {
		if cam.ID == cameraID {
			idx = i
			break
		}
	}
	if idx == -1 {
		return fmt.Errorf("camera %q not found", cameraID)
	}
	savedCam := cm.cfg.Cameras[idx]

	// 1. Stop recorder if running
	if rec, ok := cm.recorders[cameraID]; ok {
		if err := rec.Stop(); err != nil {
			logger.Warn("failed to stop recorder", "camera_id", cameraID, "error", err)
		}
		// Notify health manager of camera removal
		cm.healthMgr.OnCameraRemoved(cameraID, rec)
		delete(cm.recorders, cameraID)
		delete(cm.hubRegistry, cameraID)
		if cm.metrics != nil {
			cm.metrics.ActiveCameras.Dec()
		}
	}

	// Stop keyframe extractor if running
	if ext, ok := cm.keyframeExtractors[cameraID]; ok {
		delete(cm.keyframeExtractors, cameraID)
		if err := ext.Stop(); err != nil {
			logger.Warn("failed to stop keyframe extractor", "camera_id", cameraID, "error", err)
		}
	}

	// Stop frame poller if running
	cm.stopTimelapseFramePoller(cameraID)

	// 2. Merge segments (non-blocking — failure is logged but does not stop archival)
	if cm.mergeMgr != nil {
		if err := cm.mergeMgr.MergeCamera(ctx, cameraID); err != nil {
			logger.Warn("merge before archive failed", "camera_id", cameraID, "error", err)
		}
	}

	// 3. Mark camera archived in DB
	if err := cm.db.ArchiveCameraDB(ctx, cameraID); err != nil {
		return fmt.Errorf("failed to archive camera in DB: %w", err)
	}

	// 4. Mark all recordings archived in DB
	affected, err := cm.db.ArchiveAllRecordings(ctx, cameraID)
	if err != nil {
		logger.Warn("failed to archive recordings", "camera_id", cameraID, "error", err)
	} else {
		logger.Info("archived recordings", "camera_id", cameraID, "count", affected)
	}

	// 5. Remove from in-memory config slice and persist
	cm.cfg.Cameras = append(cm.cfg.Cameras[:idx], cm.cfg.Cameras[idx+1:]...)
	if err := cm.persistConfig(); err != nil {
		// Rollback: re-insert camera at original position
		cm.cfg.Cameras = append(cm.cfg.Cameras[:idx], append([]config.CameraConfig{savedCam}, cm.cfg.Cameras[idx:]...)...)
		return fmt.Errorf("failed to persist config: %w", err)
	}

	logger.Info("archived camera", "camera_id", cameraID)
	return nil
}

// UpdateCamera applies partial updates to an existing camera.
// Returns the updated CameraConfig.
func (cm *CameraManager) UpdateCamera(ctx context.Context, cameraID string, updates CameraUpdate) (*config.CameraConfig, error) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	// Find camera
	idx := -1
	var cam *config.CameraConfig
	for i := range cm.cfg.Cameras {
		if cm.cfg.Cameras[i].ID == cameraID {
			idx = i
			cam = &cm.cfg.Cameras[i]
			break
		}
	}
	if idx == -1 {
		return nil, &model.CameraNotFoundError{CameraID: cameraID}
	}
	// Save original for potential rollback
	savedCam := *cam

	// Determine if recorder needs restart
	needsRestart := false
	onvifEndpointChanged := false
	if updates.URL != nil && *updates.URL != cam.URL {
		needsRestart = true
	}
	if updates.Protocol != nil && *updates.Protocol != cam.Protocol {
		needsRestart = true
	}
	if updates.Username != nil && *updates.Username != cam.Username {
		needsRestart = true
	}
	if updates.Password != nil && *updates.Password != cam.Password {
		needsRestart = true
	}
	if updates.Channel != nil && *updates.Channel != cam.Channel {
		needsRestart = true
	}

	// Apply updates
	if updates.Name != nil {
		cam.Name = *updates.Name
	}
	if updates.URL != nil {
		cam.URL = *updates.URL
	}
	if updates.Protocol != nil {
		cam.Protocol = *updates.Protocol
	}
	if updates.Encoding != nil {
		if *updates.Encoding != cam.Encoding {
			needsRestart = true
		}
		cam.Encoding = *updates.Encoding
	}
	if updates.Username != nil {
		cam.Username = *updates.Username
	}
	if updates.Password != nil {
		cam.Password = *updates.Password
	}
	if updates.ONVIFEndpoint != nil {
		if *updates.ONVIFEndpoint != cam.ONVIFEndpoint {
			onvifEndpointChanged = true
		}
		cam.ONVIFEndpoint = *updates.ONVIFEndpoint
	}
	if updates.ProfileToken != nil {
		cam.ProfileToken = *updates.ProfileToken
	}
	if updates.StreamEncoding != nil {
		if *updates.StreamEncoding != cam.StreamEncoding {
			needsRestart = true
		}
		cam.StreamEncoding = *updates.StreamEncoding
	}

	if updates.Transcoding != nil {
		cam.Transcoding = updates.Transcoding
	}
	if updates.Channel != nil {
		cam.Channel = *updates.Channel
	}
	if updates.AudioEnabled != nil {
		cam.AudioEnabled = *updates.AudioEnabled
	}
	// Push/ingest fields (SRT/RTMP)
	if updates.StreamKey != nil {
		cam.StreamKey = *updates.StreamKey
	}
	if updates.SRTPassphrase != nil {
		cam.SRTPassphrase = *updates.SRTPassphrase
	}
	if updates.SRTStreamID != nil {
		cam.SRTStreamID = *updates.SRTStreamID
	}
	if updates.PushTargets != nil {
		cam.PushTargets = *updates.PushTargets
	}
	if updates.PushRetentionDays != nil {
		cam.PushRetentionDays = updates.PushRetentionDays
	}
	if updates.StableID != nil {
		cam.StableID = *updates.StableID
	}
	if updates.SubnetHints != nil {
		cam.SubnetHints = *updates.SubnetHints
	}
	if updates.DarkFrameFilterEnabled != nil {
		cam.DarkFrameFilterEnabled = *updates.DarkFrameFilterEnabled
	}
	if updates.DarkFrameThreshold != nil {
		cam.DarkFrameThreshold = *updates.DarkFrameThreshold
	}
	if updates.RecordingSchedule != nil {
		cam.RecordingSchedule = updates.RecordingSchedule
	}

	// Persist to database
	if cm.db != nil {
		if err := cm.db.UpsertCamera(ctx, cam.ID, cam.Name, string(cam.Protocol), cam.Encoding, cam.URL, cam.Username, cam.Password, cam.ONVIFEndpoint, cam.ProfileToken, cam.StreamEncoding); err != nil {
			logger.Error("failed to upsert camera record", "camera_id", cam.ID, "error", err)
		}
		// Persist DB-only metadata fields
		if updates.Description != nil || updates.Location != nil || updates.Brand != nil || updates.Model != nil || updates.SerialNumber != nil || updates.RetentionDays != nil {
			desc := strPtrOrEmpty(updates.Description)
			loc := strPtrOrEmpty(updates.Location)
			br := strPtrOrEmpty(updates.Brand)
			mo := strPtrOrEmpty(updates.Model)
			sn := strPtrOrEmpty(updates.SerialNumber)
			rd := intPtrOrZero(updates.RetentionDays)
			if err := cm.db.UpdateCameraMetadata(ctx, cam.ID, desc, loc, br, mo, sn, rd); err != nil {
				logger.Error("failed to update camera metadata", "camera_id", cam.ID, "error", err)
			}
		}
	}

	segDur, err := time.ParseDuration(cm.cfg.Storage.SegmentDuration)
	if err != nil {
		segDur = recorder.DefaultSegmentDur
	}

	// Stop existing recorder if needs restart
	if needsRestart {
		if rec, ok := cm.recorders[cam.ID]; ok {
			if err := rec.Stop(); err != nil {
				logger.Warn("failed to stop recorder", "camera_id", cam.ID, "error", err)
			}
			delete(cm.recorders, cam.ID)
		}
		// Stop keyframe extractor if running
		if ext, ok := cm.keyframeExtractors[cam.ID]; ok {
			delete(cm.keyframeExtractors, cam.ID)
			if err := ext.Stop(); err != nil {
				logger.Warn("failed to stop keyframe extractor", "camera_id", cam.ID, "error", err)
			}
		}
		// Stop frame poller if running
		cm.stopTimelapseFramePoller(cam.ID)
	}

	// Start recorder if protocol changed to a recordable one
	if needsRestart {
		if _, exists := cm.recorders[cam.ID]; !exists {
			if err := cm.startRecorder(ctx, *cam, segDur); err != nil {
				logger.Error("failed to start recorder", "error", err)
			}
		}
	}

	// If ONVIF endpoint changed, close cached client so a fresh one is created
	if onvifEndpointChanged {
		cm.CloseONVIFClient(cam.ID)
	}

	// Persist config to disk (rollback on failure)
	if err := cm.persistConfig(); err != nil {
		// Rollback: restore original camera config
		cm.cfg.Cameras[idx] = savedCam
		return nil, fmt.Errorf("failed to persist config: %w", err)
	}

	// Auto-populate SnapshotURL for ONVIF cameras (non-blocking)
	protocolChangedToOnvif := updates.Protocol != nil && *updates.Protocol == string(model.ProtoONVIF)
	if (protocolChangedToOnvif || onvifEndpointChanged) && cam.SnapshotURL == "" {
		go cm.autoPopulateSnapshotURL(context.Background(), cam.ID)
	}

	// Reconcile push-out relay targets. Run in a goroutine so it does NOT execute
	// under cm.mu — SetCameraTargets calls back into GetHub which re-locks cm.mu,
	// which would self-deadlock under the held Lock (see AddCamera for rationale).
	if cm.relayMgr != nil {
		cameraID := cam.ID
		targets := append([]config.PushTargetConfig(nil), cam.PushTargets...)
		go cm.relayMgr.SetCameraTargets(cameraID, targets)
	}

	// Start or update recording schedule monitor if a schedule is configured.
	if cam.RecordingSchedule != nil && len(cam.RecordingSchedule.TimeRanges) > 0 {
		cm.startRecordingScheduleMonitor(context.Background(), cam.ID)
	}

	return cam, nil
}
