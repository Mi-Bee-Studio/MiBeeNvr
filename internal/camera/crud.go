package camera

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/onvif"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/recorder"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/timelapse"
)

// AddCamera adds a new camera to the manager, starts its recorder, and persists.
// If cam.ID is empty, a new ID is generated automatically.
// Returns the camera ID.
func (cm *CameraManager) AddCamera(ctx context.Context, cam config.CameraConfig) (string, error) {
	if cam.ID == "" {
		cam.ID = GenerateCameraID()
	}

	// Reverse ONVIF lookup: attempt to populate StableID from the device before
	// the Phase 1 dedup check, so StableID-based dedup can catch the same device
	// by hardware serial even when the incoming config has no stable_id yet.
	// Best-effort with 3s timeout; never blocks the add.
	if err := tryFillStableIDFromONVIF(ctx, &cam); err != nil {
		logger.Debug("AddCamera: best-effort ONVIF stable_id lookup failed", "camera_id", cam.ID, "error", err)
	}

	// PHASE 1 — under configMu: dedup check, append to cfg.Cameras, persist DB +
	// disk, republish snapshot. The dedup check covers three identity keys:
	//   - ID (exact): a re-add of the same camera ID
	//   - ONVIFEndpoint: the same physical ONVIF device must not be enrolled
	//     twice even with different IDs (auto-discover generates fresh IDs per
	//     discovery, so ID-level dedup alone cannot catch a re-discovered
	//     device). This is the last line of defense behind the auto-discover
	//     Adder's own existsInDB check.
	//   - StableID (ONVIF serial): catches the same device after a DHCP IP
	//     change (endpoint string differs, hardware identity is the same).
	var dup bool
	var dupID string
	var persistErr error
	cm.configMu.Lock()
	for _, existing := range cm.cfg.Cameras {
		if existing.ID == cam.ID {
			dup = true
			dupID = existing.ID
			break
		}
		// ONVIF endpoint dedup: both must be onvif protocol with a non-empty,
		// equal endpoint. Compared via NormalizeOnvifEndpoint (lowercases
		// scheme/host, elides default port :80/:443, strips trailing slash) so
		// that "http://1.2.3.4/..." and "http://1.2.3.4:80/..." match (#175).
		if cam.Protocol == string(model.ProtoONVIF) && existing.Protocol == string(model.ProtoONVIF) {
			if ep := storage.NormalizeOnvifEndpoint(cam.ONVIFEndpoint); ep != "" {
				if storage.NormalizeOnvifEndpoint(existing.ONVIFEndpoint) == ep {
					dup = true
					dupID = existing.ID
					break
				}
			}
		}
		// StableID (hardware serial) dedup across protocols: same physical
		// device regardless of how it's addressed.
		if cam.StableID != "" && existing.StableID == cam.StableID {
			dup = true
			dupID = existing.ID
			break
		}
	}
	if !dup {
		cm.cfg.Cameras = append(cm.cfg.Cameras, cam)
		// Republish snapshot configs map to include the new camera pointer.
		cur := cm.loadSnapshot().clone()
		cur.configs[cam.ID] = &cm.cfg.Cameras[len(cm.cfg.Cameras)-1]
		cm.snapshot.Store(cur)

		// Persist to database
		if cm.db != nil {
			if err := cm.db.UpsertCamera(ctx, cam.ID, cam.Name, string(cam.Protocol), cam.Encoding, cam.URL, cam.Username, cam.Password, cam.ONVIFEndpoint, cam.ProfileToken, cam.StreamEncoding, cam.StableID); err != nil {
				logger.Error("failed to upsert camera record", "camera_id", cam.ID, "error", err)
			}
			// Persist the activation_state column (UpsertCamera does not write it).
			if cam.ActivationState != "" && cam.ActivationState != "active" {
				if err := cm.db.UpdateCameraActivationState(ctx, cam.ID, cam.ActivationState); err != nil {
					logger.Warn("failed to persist activation_state", "camera_id", cam.ID, "error", err)
				}
			}
		}

		// Persist config to disk (rollback on failure).
		if err := cm.persistConfig(); err != nil {
			persistErr = err
			// Rollback: remove the camera we just added (slice + snapshot).
			for i, c := range cm.cfg.Cameras {
				if c.ID == cam.ID {
					cm.cfg.Cameras = append(cm.cfg.Cameras[:i], cm.cfg.Cameras[i+1:]...)
					break
				}
			}
			cur := cm.loadSnapshot().clone()
			delete(cur.configs, cam.ID)
			cm.snapshot.Store(cur)
		}
	}
	cm.configMu.Unlock()

	if dup {
		// dupID is the EXISTING camera that this add duplicates (by ID, ONVIF
		// endpoint, or stable_id). Log it so auto-discover's "why didn't this
		// add?" is debuggable — AddCamera is the last-line dedup behind the
		// Adder's existsInDB, so reaching here means an upstream dedup gap.
		if dupID != "" && dupID != cam.ID {
			logger.Info("AddCamera deduped: camera already enrolled under a different ID",
				"incoming_id", cam.ID, "existing_id", dupID,
				"endpoint", cam.ONVIFEndpoint, "stable_id", cam.StableID)
		}
		return "", &model.CameraAlreadyExistsError{CameraID: cam.ID}
	}
	if persistErr != nil {
		return "", fmt.Errorf("failed to persist config: %w", persistErr)
	}

	// PHASE 2 — outside configMu: recorder lifecycle + async side effects.
	// startRecorder runs entirely outside any lock (registers via apply).
	needsRecorderStart := cam.ActivationState != "pending_activation"
	segDur := recorder.DefaultSegmentDur
	if d, err := time.ParseDuration(cm.cfg.Storage.SegmentDuration); err == nil {
		segDur = d
	}
	startCam := cam

	if needsRecorderStart {
		if err := cm.startRecorder(ctx, startCam, segDur); err != nil {
			logger.Error("failed to start recorder", "error", err)
		}
	} else {
		logger.Info("camera added in pending_activation state; recorder not started",
			"camera_id", startCam.ID, "name", startCam.Name, "endpoint", startCam.ONVIFEndpoint)
	}

	// Auto-populate SnapshotURL for ONVIF cameras (non-blocking)
	if startCam.Protocol == string(model.ProtoONVIF) && startCam.SnapshotURL == "" {
		go cm.autoPopulateSnapshotURL(context.Background(), startCam.ID)
	}

	// Reconcile push-out relay targets (non-blocking goroutine).
	if cm.relayMgr != nil {
		targets := append([]config.PushTargetConfig(nil), startCam.PushTargets...)
		go cm.relayMgr.SetCameraTargets(startCam.ID, targets)
	}

	// Notify subscribers (auto-discover frontend toast, etc.).
	cm.publishCameraAdded(ctx, startCam)

	return startCam.ID, nil
}

// publishCameraAdded emits a camera.added event if an event bus is configured.
// Publish is non-blocking (ring buffer, drops oldest on overflow).
func (cm *CameraManager) publishCameraAdded(ctx context.Context, cam config.CameraConfig) {
	if cm.eventBus == nil {
		return
	}
	source := "manual"
	if cam.ActivationState == "pending_activation" {
		source = "auto"
	}
	cm.eventBus.Publish(ctx, event.TopicCameraAdded, map[string]any{
		"camera_id":        cam.ID,
		"name":             cam.Name,
		"endpoint":         cam.ONVIFEndpoint,
		"activation_state": cam.ActivationState,
		"source":           source,
	})
}

// tryFillStableIDFromONVIF attempts to fetch the ONVIF device serial number and
// populate cam.StableID from the hardware device. Best-effort: always returns nil;
// logs warning on failure but never blocks camera addition.
// Creates a one-time ONVIF client (not cached) so the manager's client cache is
// not polluted by a camera that may not be added (e.g. deduped).
func tryFillStableIDFromONVIF(ctx context.Context, cam *config.CameraConfig) error {
	// Skip only when a VALID stable_id is already set — a dirty value (IP, URL,
	// all-zero MAC — see #216) should be overwritten by the real serial.
	if config.IsValidStableID(cam.StableID) || cam.Protocol != string(model.ProtoONVIF) || cam.ONVIFEndpoint == "" {
		return nil
	}

	endpoint := cam.ONVIFEndpoint
	client := onvif.NewClient(endpoint, cam.Username, cam.Password)
	return tryFillStableIDFromONVIFWithClient(ctx, cam, client)
}

// tryFillStableIDFromONVIFWithClient is like tryFillStableIDFromONVIF but accepts
// an onvif.DeviceClient directly for testability.
func tryFillStableIDFromONVIFWithClient(ctx context.Context, cam *config.CameraConfig, client onvif.DeviceClient) error {
	lookupCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	if err := client.Connect(lookupCtx); err != nil {
		logger.Warn("reverse ONVIF lookup: connect failed, skipping",
			"endpoint", cam.ONVIFEndpoint, "error", err)
		return nil
	}

	info, err := client.GetDeviceInformation(lookupCtx)
	if err != nil {
		logger.Warn("reverse ONVIF lookup: GetDeviceInformation failed, skipping",
			"endpoint", cam.ONVIFEndpoint, "error", err)
		return nil
	}

	// Only persist a serial that passes IsValidStableID — defends against the
	// firmware glitch (upstream seeed-esp32s3-cam #2) returning garbage that
	// would freeze as the camera's identity and break rediscovery (#216).
	if serial := strings.TrimSpace(info.SerialNumber); config.IsValidStableID(serial) {
		cam.StableID = serial
		logger.Info("reverse ONVIF lookup: populated stable_id",
			"endpoint", cam.ONVIFEndpoint, "stable_id", serial)
	}

	return nil
}

// RemoveCamera removes a camera from the manager, stops its recorder, and removes it from config.
// Does NOT delete the camera record from the database.
func (cm *CameraManager) RemoveCamera(ctx context.Context, cameraID string) error {
	// Snapshot the recorder + hub + aux components (lock-free / auxMu) so we can
	// stop them OUTSIDE any lock after the config/snapshot removal, AND restore
	// them on a persistConfig rollback.
	rec := cm.snapshotRecorder(cameraID)
	hub := cm.snapshotHub(cameraID)
	var ext *timelapse.KeyframeExtractor
	cm.auxMu.Lock()
	if e, ok := cm.keyframeExtractors[cameraID]; ok {
		ext = e
		delete(cm.keyframeExtractors, cameraID)
	}
	cm.auxMu.Unlock()

	// PHASE 1 — under configMu: remove from cfg.Cameras + snapshot, persist.
	var idx int
	var savedCam config.CameraConfig
	var persistErr error
	cm.configMu.Lock()
	idx = cm.cameraIndexInConfig(cameraID)
	if idx == -1 {
		cm.configMu.Unlock()
		return &model.CameraNotFoundError{CameraID: cameraID}
	}
	savedCam = cm.cfg.Cameras[idx]
	cm.cfg.Cameras = append(cm.cfg.Cameras[:idx], cm.cfg.Cameras[idx+1:]...)
	// Republish snapshot without this camera (recorder/hub/config/failedStarts).
	cur := cm.loadSnapshot().clone()
	delete(cur.recorders, cameraID)
	delete(cur.hubs, cameraID)
	delete(cur.configs, cameraID)
	delete(cur.failedStarts, cameraID)
	cm.snapshot.Store(cur)
	if err := cm.persistConfig(); err != nil {
		persistErr = err
		// Rollback: re-insert camera at original position + restore snapshot.
		// Restore all four maps symmetrically with the Phase 1 deletes
		// (recorders/hubs/configs/failedStarts) so a persistConfig failure
		// leaves the camera fully restored, not half-removed.
		cm.cfg.Cameras = append(cm.cfg.Cameras[:idx], append([]config.CameraConfig{savedCam}, cm.cfg.Cameras[idx:]...)...)
		rb := cm.loadSnapshot().clone()
		rb.configs[cameraID] = &cm.cfg.Cameras[idx]
		if rec != nil {
			rb.recorders[cameraID] = rec
		}
		if hub != nil {
			rb.hubs[cameraID] = hub
		}
		cm.snapshot.Store(rb)
	}
	cm.configMu.Unlock()
	if persistErr != nil {
		// Restore the aux component we removed.
		if ext != nil {
			cm.auxMu.Lock()
			cm.keyframeExtractors[cameraID] = ext
			cm.auxMu.Unlock()
		}
		return fmt.Errorf("failed to persist config: %w", persistErr)
	}

	// PHASE 2 — outside configMu: stop recorder + aux components + relay.
	// Drop the per-camera lifecycle mutex entry — the camera is gone, so no
	// future lifecycle op can reference it. Prevents unbounded lifecycleMu
	// growth under add/remove churn.
	cm.lifecycleMu.Delete(cameraID)
	if rec != nil {
		if err := rec.Stop(); err != nil {
			logger.Warn("failed to stop recorder", "camera_id", cameraID, "error", err)
		}
		// Notify health manager of camera removal
		cm.healthMgr.OnCameraRemoved(cameraID, rec)
		if cm.metrics != nil {
			cm.metrics.ActiveCameras.Dec()
		}
	}
	// Stop all relay targets for this camera.
	if cm.relayMgr != nil {
		cm.relayMgr.RemoveCamera(cameraID)
	}
	if ext != nil {
		if err := ext.Stop(); err != nil {
			logger.Warn("failed to stop keyframe extractor", "camera_id", cameraID, "error", err)
		}
	}
	// Stop frame poller if running
	cm.stopTimelapseFramePoller(cameraID)
	// Stop dual-mode timelapse schedule monitor
	cm.stopDualModeTimelapseScheduleMonitor(cameraID)
	// Tear the on-demand sub-stream pull down with the camera (#513).
	if cm.subStreams != nil {
		cm.subStreams.StopCamera(cameraID)
	}

	return nil
}

// ArchiveCamera archives a camera: stops recorder, merges segments, marks archived in DB,
// marks all recordings archived, and removes from config YAML.
// The camera row and recordings are preserved in the database.
// Merge failure is non-blocking (logged but continues).
func (cm *CameraManager) ArchiveCamera(ctx context.Context, cameraID string) error {
	// Snapshot the recorder + hub + aux components to stop OUTSIDE any lock,
	// and to restore on a persistConfig rollback.
	rec := cm.snapshotRecorder(cameraID)
	hub := cm.snapshotHub(cameraID)
	var ext *timelapse.KeyframeExtractor
	cm.auxMu.Lock()
	if e, ok := cm.keyframeExtractors[cameraID]; ok {
		ext = e
		delete(cm.keyframeExtractors, cameraID)
	}
	cm.auxMu.Unlock()

	// PHASE 1 — under configMu: verify, DB archive + recordings archive, remove
	// from cfg.Cameras + snapshot, persist. All DB/disk I/O is under configMu
	// (millisecond-scale); no recorder Stop/Start here.
	var idx int
	var savedCam config.CameraConfig
	var dbErr, persistErr error
	cm.configMu.Lock()
	idx = cm.cameraIndexInConfig(cameraID)
	if idx == -1 {
		cm.configMu.Unlock()
		return fmt.Errorf("camera %q not found", cameraID)
	}
	savedCam = cm.cfg.Cameras[idx]

	// Mark camera archived in DB + archive recordings.
	if err := cm.db.ArchiveCameraDB(ctx, cameraID); err != nil {
		dbErr = fmt.Errorf("failed to archive camera in DB: %w", err)
		cm.configMu.Unlock()
		// Restore aux component we removed.
		if ext != nil {
			cm.auxMu.Lock()
			cm.keyframeExtractors[cameraID] = ext
			cm.auxMu.Unlock()
		}
		return dbErr
	}
	affected, err := cm.db.ArchiveAllRecordings(ctx, cameraID)
	if err != nil {
		logger.Warn("failed to archive recordings", "camera_id", cameraID, "error", err)
	} else {
		logger.Info("archived recordings", "camera_id", cameraID, "count", affected)
	}

	// Remove from in-memory config slice and persist.
	cm.cfg.Cameras = append(cm.cfg.Cameras[:idx], cm.cfg.Cameras[idx+1:]...)
	cur := cm.loadSnapshot().clone()
	delete(cur.recorders, cameraID)
	delete(cur.hubs, cameraID)
	delete(cur.configs, cameraID)
	delete(cur.failedStarts, cameraID)
	cm.snapshot.Store(cur)
	if err := cm.persistConfig(); err != nil {
		persistErr = err
		// Rollback: re-insert camera at original position + restore snapshot.
		// Restore all four maps symmetrically with the deletes above.
		cm.cfg.Cameras = append(cm.cfg.Cameras[:idx], append([]config.CameraConfig{savedCam}, cm.cfg.Cameras[idx:]...)...)
		rb := cm.loadSnapshot().clone()
		rb.configs[cameraID] = &cm.cfg.Cameras[idx]
		if rec != nil {
			rb.recorders[cameraID] = rec
		}
		if hub != nil {
			rb.hubs[cameraID] = hub
		}
		cm.snapshot.Store(rb)
	}
	mergeMgr := cm.mergeMgr
	cm.configMu.Unlock()
	if persistErr != nil {
		if ext != nil {
			cm.auxMu.Lock()
			cm.keyframeExtractors[cameraID] = ext
			cm.auxMu.Unlock()
		}
		return fmt.Errorf("failed to persist config: %w", persistErr)
	}

	// PHASE 2 — outside configMu: stop recorder + aux + async merge.
	// Drop the per-camera lifecycle mutex entry — the camera is archived/gone.
	cm.lifecycleMu.Delete(cameraID)
	if rec != nil {
		if err := rec.Stop(); err != nil {
			logger.Warn("failed to stop recorder", "camera_id", cameraID, "error", err)
		}
		cm.healthMgr.OnCameraRemoved(cameraID, rec)
		if cm.metrics != nil {
			cm.metrics.ActiveCameras.Dec()
		}
	}
	if ext != nil {
		if err := ext.Stop(); err != nil {
			logger.Warn("failed to stop keyframe extractor", "camera_id", cameraID, "error", err)
		}
	}
	cm.stopTimelapseFramePoller(cameraID)
	cm.stopDualModeTimelapseScheduleMonitor(cameraID)

	// Merge segments asynchronously. Uses a detached context (not the request
	// ctx) so the merge survives the HTTP request completing. MergeCamera keys
	// off cameraID in the recordings table, not the in-memory config.
	if mergeMgr != nil {
		go func() {
			mergeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			start := time.Now()
			if err := mergeMgr.MergeCamera(mergeCtx, cameraID); err != nil {
				logger.Warn("async merge after archive failed", "camera_id", cameraID, "error", err, "duration", time.Since(start))
			} else {
				logger.Info("async merge after archive completed", "camera_id", cameraID, "duration", time.Since(start))
			}
		}()
	}

	logger.Info("archived camera", "camera_id", cameraID)
	return nil
}

// UpdateCamera applies partial updates to an existing camera.
// Returns the updated CameraConfig.
func (cm *CameraManager) UpdateCamera(ctx context.Context, cameraID string, updates CameraUpdate) (*config.CameraConfig, error) {
	// PHASE 1 — under configMu: find camera, mutate config, persist to DB + disk.
	// configMu serializes cfg.Cameras mutation + persistConfig. The recorder
	// stop/start in PHASE 2 runs OUTSIDE configMu (startRecorder registers via
	// apply, and rec.Stop runs lock-free) — this is what eliminates the
	// self-deadlock that previously blocked every camera API endpoint.
	cm.configMu.Lock()

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
		cm.configMu.Unlock()
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
	if updates.GB28181 != nil &&
		(updates.GB28181.DeviceID != cam.GB28181.DeviceID || updates.GB28181.ChannelID != cam.GB28181.ChannelID) {
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
	if updates.SubProfileToken != nil {
		cam.SubProfileToken = *updates.SubProfileToken
	}
	if updates.SubStreamURL != nil {
		cam.SubStreamURL = *updates.SubStreamURL
	}
	if updates.StreamEncoding != nil {
		if *updates.StreamEncoding != cam.StreamEncoding {
			needsRestart = true
		}
		cam.StreamEncoding = *updates.StreamEncoding
	}
	if updates.ActivationState != nil {
		// Pending→active does NOT set needsRestart: a pending camera never had a
		// recorder running, so there is nothing to restart. The caller
		// (ActivateCamera) explicitly starts the recorder after this returns.
		// Persisting the state to the DB is done here (under configMu) for atomicity.
		prev := cam.ActivationState
		cam.ActivationState = *updates.ActivationState
		if prev != *updates.ActivationState && cm.db != nil {
			if err := cm.db.UpdateCameraActivationState(ctx, cameraID, *updates.ActivationState); err != nil {
				logger.Warn("failed to persist activation_state in UpdateCamera", "camera_id", cameraID, "error", err)
			}
		}
	}

	if updates.Transcoding != nil {
		cam.Transcoding = updates.Transcoding
	}
	if updates.Channel != nil {
		cam.Channel = *updates.Channel
	}
	if updates.GB28181 != nil {
		cam.GB28181 = *updates.GB28181
	}
	if updates.AudioEnabled != nil {
		// The audio gate is read once at recorder construction (GB28181's
		// passive recorder; RTSP's audio track setup) — toggling it requires
		// a recorder restart to take effect.
		if *updates.AudioEnabled != cam.AudioEnabled {
			needsRestart = true
		}
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
	if updates.RecordingEnabled != nil {
		// Toggling live-only mode requires a recorder restart to take effect
		// (the writeFrames loop reads RecordEnabled once at Start).
		// Compare the EFFECTIVE old value (nil defaults to true = record) against
		// the new value. The previous expression mishandled the nil→false case,
		// so disabling recording on a camera whose recording_enabled was never
		// explicitly persisted left the old recorder writing segments to disk.
		oldEffective := cam.RecordingEnabled == nil || *cam.RecordingEnabled
		if oldEffective != *updates.RecordingEnabled {
			needsRestart = true
		}
		cam.RecordingEnabled = updates.RecordingEnabled
	}
	if updates.CascadeEnabled != nil {
		cam.CascadeEnabled = updates.CascadeEnabled
	}
	if updates.CascadeSubStream != nil {
		cam.CascadeSubStream = *updates.CascadeSubStream
	}
	if updates.RecordingSchedule != nil {
		cam.RecordingSchedule = updates.RecordingSchedule
	}
	if updates.RecordingMode != nil {
		if *updates.RecordingMode != cam.RecordingMode {
			needsRestart = true
		}
		cam.RecordingMode = *updates.RecordingMode
	}
	if updates.Adaptive != nil {
		// Params are read at recorder construction; only a real change needs
		// the restart (the edit form re-sends the whole object on every save).
		if cam.Adaptive == nil || *cam.Adaptive != *updates.Adaptive {
			needsRestart = true
		}
		cam.Adaptive = updates.Adaptive
	}
	if updates.AudioTrigger != nil {
		// Same restart semantics as Adaptive (issue #478): the runtime is
		// armed at recorder construction. {enabled:false} disarms.
		if cam.AudioTrigger == nil || *cam.AudioTrigger != *updates.AudioTrigger {
			needsRestart = true
		}
		cam.AudioTrigger = updates.AudioTrigger
	}

	// Persist to database
	if cm.db != nil {
		if err := cm.db.UpsertCamera(ctx, cam.ID, cam.Name, string(cam.Protocol), cam.Encoding, cam.URL, cam.Username, cam.Password, cam.ONVIFEndpoint, cam.ProfileToken, cam.StreamEncoding, cam.StableID); err != nil {
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

	// If ONVIF endpoint changed, close cached client so a fresh one is created.
	// CloseONVIFClient takes onvifMu + deviceInfoMu (NOT configMu), so it is
	// safe under the held lock.
	if onvifEndpointChanged {
		cm.CloseONVIFClient(cam.ID)
	}

	// Persist config to disk (rollback on failure) while still holding configMu
	// so the on-disk config is consistent with the in-memory mutation.
	if err := cm.persistConfig(); err != nil {
		// Rollback: restore original camera config
		cm.cfg.Cameras[idx] = savedCam
		cm.configMu.Unlock()
		return nil, fmt.Errorf("failed to persist config: %w", err)
	}

	// Snapshot everything the post-lock phase needs, then release configMu
	// before any recorder stop/start (which run lock-free).
	camCopy := *cam
	hasSchedule := cam.RecordingSchedule != nil && len(cam.RecordingSchedule.TimeRanges) > 0
	relayTargets := append([]config.PushTargetConfig(nil), cam.PushTargets...)
	snapshotURLEmpty := cam.SnapshotURL == ""
	protocolChangedToOnvif := updates.Protocol != nil && *updates.Protocol == string(model.ProtoONVIF)
	cm.configMu.Unlock()

	// PHASE 2 — outside configMu: stop/start recorder + async side effects.
	if needsRestart {
		// Snapshot the old recorder + timelapse subcomponents, remove from the
		// registry via apply / auxMu, then stop them OUTSIDE any lock (Stop can
		// join a goroutine / do I/O).
		var oldRec model.Recorder
		if rec := cm.snapshotRecorder(cameraID); rec != nil {
			oldRec = rec
			cm.apply(func(s *snapshot) *snapshot {
				delete(s.recorders, cameraID)
				return s
			})
		}
		var oldExt *timelapse.KeyframeExtractor
		cm.auxMu.Lock()
		if ext, ok := cm.keyframeExtractors[cameraID]; ok {
			oldExt = ext
			delete(cm.keyframeExtractors, cameraID)
		}
		cm.auxMu.Unlock()
		cm.stopTimelapseFramePoller(cameraID)
		cm.stopDualModeTimelapseScheduleMonitor(cameraID)
		if oldRec != nil {
			if err := oldRec.Stop(); err != nil {
				logger.Warn("failed to stop recorder", "camera_id", cameraID, "error", err)
			}
		}
		if oldExt != nil {
			if err := oldExt.Stop(); err != nil {
				logger.Warn("failed to stop keyframe extractor", "camera_id", cameraID, "error", err)
			}
		}

		// startRecorder registers the new recorder via apply (lock-free lifecycle).
		if cm.snapshotRecorder(cameraID) == nil {
			if err := cm.startRecorder(ctx, camCopy, segDur); err != nil {
				logger.Error("failed to start recorder", "error", err)
			}
		}
	}

	// Sub-stream target inputs changed (protocol/credentials/endpoint/sub
	// fields) → recycle the on-demand pull so the next Acquire re-resolves
	// from the new config (#513). Runs for sub-only changes too, which need
	// no recorder restart. Compared against savedCam outside configMu
	// instead of scattering change flags through the apply block above.
	if cm.subStreams != nil &&
		(savedCam.Protocol != camCopy.Protocol || savedCam.Username != camCopy.Username ||
			savedCam.Password != camCopy.Password || savedCam.ONVIFEndpoint != camCopy.ONVIFEndpoint ||
			savedCam.SubStreamURL != camCopy.SubStreamURL || savedCam.SubProfileToken != camCopy.SubProfileToken) {
		cm.subStreams.StopCamera(cameraID)
	}

	// Auto-populate SnapshotURL for ONVIF cameras (non-blocking)
	if (protocolChangedToOnvif || onvifEndpointChanged) && snapshotURLEmpty {
		go cm.autoPopulateSnapshotURL(context.Background(), cameraID)
	}

	// Reconcile push-out relay targets. Run in a goroutine to avoid blocking the
	// API response on relay-engine teardown.
	if cm.relayMgr != nil {
		cameraID := cameraID
		targets := relayTargets
		go cm.relayMgr.SetCameraTargets(cameraID, targets)
	}

	// Start or update recording schedule monitor if a schedule is configured.
	if hasSchedule {
		cm.startRecordingScheduleMonitor(context.Background(), cameraID)
	}

	// Return a snapshot — we no longer hold configMu, so returning the live
	// pointer into cm.cfg.Cameras would race with concurrent mutations.
	return &camCopy, nil
}

// ActivateCamera transitions a "pending_activation" camera to "active": applies
// the supplied credentials, flips the activation state (config + DB), and starts
// the recorder. Used by auto-discover — an authenticated ONVIF device discovered
// without valid credentials is persisted as pending_activation; the user then
// supplies credentials via the "activate" UI, which calls this method.
//
// Idempotent for an already-active camera: credentials are still updated (so the
// same endpoint can fix wrong creds on a live camera) and StartCamera restarts
// the recorder with the new credentials.
func (cm *CameraManager) ActivateCamera(ctx context.Context, cameraID, username, password string) error {
	active := "active"
	updates := CameraUpdate{
		Username:        &username,
		Password:        &password,
		ActivationState: &active,
	}
	if _, err := cm.UpdateCamera(ctx, cameraID, updates); err != nil {
		return err
	}
	logger.Info("camera activated", "camera_id", cameraID)
	// StartCamera is idempotent (returns CameraAlreadyRunningError if running);
	// for a freshly-activated pending camera it starts the recorder for the first
	// time, for an already-active camera it restarts with updated creds.
	return cm.StartCamera(ctx, cameraID)
}
