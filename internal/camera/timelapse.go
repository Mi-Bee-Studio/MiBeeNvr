package camera

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/recorder"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/timelapse"
)

// deriveProtocolForSnapshot returns the effective protocol to use for snapshot URL
// derivation. For timelapse protocol, we derive from the URL scheme.
func deriveProtocolForSnapshot(cam config.CameraConfig) string {
	if cam.Protocol != "timelapse" {
		return cam.Protocol
	}
	// Extract protocol from URL scheme
	if strings.HasPrefix(cam.URL, "rtsp://") || strings.HasPrefix(cam.URL, "rtsp:") {
		return "rtsp"
	}
	if strings.HasPrefix(cam.URL, "http://") || strings.HasPrefix(cam.URL, "https://") {
		return "http"
	}
	return cam.Protocol
}

// createTimelapseSnapshotRecorder creates a SnapshotCapturer for snapshot frame source.
func (cm *CameraManager) createTimelapseSnapshotRecorder(cam config.CameraConfig, segDur time.Duration) model.Recorder {
	interval := 30 * time.Second
	if d, err := time.ParseDuration(cam.Timelapse.Interval); err == nil && d >= time.Millisecond {
		interval = d
	}

	deriveProto := deriveProtocolForSnapshot(cam)

	snapCfg := timelapse.SnapshotCapturerConfig{
		CameraID:    cam.ID,
		SnapshotURL: cam.Timelapse.SnapshotURL,
		Interval:    interval,
		SegmentDur:  segDur,
		Username:    cam.Username,
		Password:    cam.Password,
		DB:          cm.db,
		Store:       cm.store,
		Metrics:     cm.metrics,
		MergeMgr:    cm.timelapseMergeMgr,
		Protocol:    deriveProto,
		StreamURL:   cam.URL,
	}
	// Auto-derive SnapshotURL if empty
	if snapCfg.SnapshotURL == "" && snapCfg.StreamURL != "" {
		if derived := timelapse.DeriveSnapshotURL(snapCfg.StreamURL, snapCfg.Protocol); derived != "" {
			snapCfg.SnapshotURL = derived
			logger.Info("auto-derived snapshot URL for timelapse",
				"camera_id", cam.ID,
				"url", derived)
		}
	}
	if snapCfg.SnapshotURL == "" {
		logger.Warn("no snapshot URL available for snapshot frame source", "camera_id", cam.ID)
		return nil
	}
	logger.Info("creating SnapshotCapturer for timelapse", "camera_id", cam.ID, "url", snapCfg.SnapshotURL)
	return timelapse.NewSnapshotCapturer(snapCfg, cm.store, cm.metrics)
}

// createTimelapseMJPEGRecorder creates a TimelapseRecorder for MJPEG stream or auto source.
func (cm *CameraManager) createTimelapseMJPEGRecorder(cam config.CameraConfig, segDur time.Duration) model.Recorder {
	tlCfg := recorder.TimelapseRecorderConfig{
		CameraID: cam.ID,
		URL:      cam.URL,
		Username: cam.Username,
		Password: cam.Password,
		DataDir:  cm.cfg.Storage.RootDir,
		DB:       cm.db,
		Metrics:  cm.metrics,
	}
	if cam.Timelapse != nil {
		if d, err := time.ParseDuration(cam.Timelapse.Interval); err == nil && d >= time.Millisecond {
			tlCfg.Interval = d
		}
	}
	if cm.timelapseMergeMgr != nil {
		tlCfg.MergeMgr = cm.timelapseMergeMgr
	}
	logger.Info("creating TimelapseRecorder for timelapse", "camera_id", cam.ID, "url", cam.URL)
	return recorder.NewTimelapseRecorder(tlCfg, cm.store)
}

// startTimelapseScheduleMonitor starts a goroutine that checks the scheduler
// every minute and starts/stops the timelapse recorder accordingly.
func (cm *CameraManager) startTimelapseScheduleMonitor(ctx context.Context, cameraID string, rec model.Recorder, tlCfg config.CameraTimelapseConfig) {
	ctx, cancel := context.WithCancel(ctx)
	cm.mu.Lock()
	if existing, ok := cm.scheduleMonitors[cameraID]; ok {
		existing()
	}
	cm.scheduleMonitors[cameraID] = cancel
	cm.mu.Unlock()

	go func() {
		defer func() {
			cm.mu.Lock()
			delete(cm.scheduleMonitors, cameraID)
			cm.mu.Unlock()
		}()

		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()

		// Initial check: stop if not recording time
		if !cm.scheduler.IsRecordingTime(tlCfg) {
			logger.Info("timelapse schedule: not recording time, stopping", "camera_id", cameraID)
			if err := rec.Stop(); err != nil {
				logger.Warn("failed to stop timelapse recorder per schedule", "camera_id", cameraID, "error", err)
			}
		}

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				isRecordingTime := cm.scheduler.IsRecordingTime(tlCfg)
				status := rec.Status()
				if isRecordingTime && (status == model.StatusStopped || status == model.StatusError) {
					logger.Info("timelapse schedule: recording time, starting", "camera_id", cameraID)
					if err := rec.Start(ctx); err != nil {
						logger.Warn("failed to start timelapse recorder per schedule", "camera_id", cameraID, "error", err)
					}
				} else if !isRecordingTime && (status == model.StatusRecording || status == model.StatusReconnecting) {
					logger.Info("timelapse schedule: not recording time, stopping", "camera_id", cameraID)
					if err := rec.Stop(); err != nil {
						logger.Warn("failed to stop timelapse recorder per schedule", "camera_id", cameraID, "error", err)
					}
				}
			}
		}
	}()
}

// stopTimelapseScheduleMonitor cancels the schedule monitor goroutine for the given camera.
func (cm *CameraManager) stopTimelapseScheduleMonitor(cameraID string) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	if cancel, ok := cm.scheduleMonitors[cameraID]; ok {
		cancel()
		delete(cm.scheduleMonitors, cameraID)
	}
}

// startTimelapseKeyframeExtractor creates and starts a KeyframeExtractor for the given camera,
// subscribing it to the provided StreamHub. The extractor is stored in the manager for lifecycle management.
func (cm *CameraManager) startTimelapseKeyframeExtractor(cameraID string, cam config.CameraConfig, hub *model.StreamHub) error {
	if cam.Timelapse == nil || !cam.Timelapse.Enabled || cam.Timelapse.FrameSource != "rtsp_keyframe" {
		return nil
	}

	interval := 5 * time.Second
	if d, err := time.ParseDuration(cam.Timelapse.Interval); err == nil && d >= time.Millisecond {
		interval = d
	}

	isH265 := cam.Encoding == "h265"

	extractor := timelapse.NewKeyframeExtractor(timelapse.KeyframeExtractorConfig{
		CameraID:   cameraID,
		Interval:   interval,
		SegmentDur: 10 * time.Minute,
		IsH265:     isH265,
		Store:      cm.store,
		DB:         cm.db,
	})

	ctx := context.Background()
	if err := extractor.Start(ctx, hub); err != nil {
		return err
	}

	cm.mu.Lock()
	cm.keyframeExtractors[cameraID] = extractor
	cm.mu.Unlock()

	logger.Info("started timelapse keyframe extractor", "camera_id", cameraID, "interval", interval)
	return nil
}

// stopTimelapseKeyframeExtractor stops and removes the keyframe extractor for the given camera.
func (cm *CameraManager) stopTimelapseKeyframeExtractor(cameraID string) {
	cm.mu.Lock()
	extractor, ok := cm.keyframeExtractors[cameraID]
	if ok {
		delete(cm.keyframeExtractors, cameraID)
	}
	cm.mu.Unlock()

	if ok && extractor != nil {
		if err := extractor.Stop(); err != nil {
			logger.Warn("failed to stop keyframe extractor", "camera_id", cameraID, "error", err)
		}
		logger.Info("stopped timelapse keyframe extractor", "camera_id", cameraID)
	}
}

// stopAllTimelapseKeyframeExtractors stops all keyframe extractors (used during manager Stop).
func (cm *CameraManager) stopAllTimelapseKeyframeExtractors() {
	cm.mu.Lock()
	extractors := make([]*timelapse.KeyframeExtractor, 0, len(cm.keyframeExtractors))
	for _, ext := range cm.keyframeExtractors {
		extractors = append(extractors, ext)
	}
	cm.keyframeExtractors = make(map[string]*timelapse.KeyframeExtractor)
	cm.mu.Unlock()

	for _, ext := range extractors {
		if err := ext.Stop(); err != nil {
			logger.Warn("failed to stop keyframe extractor", "error", err)
		}
	}
}

// PauseTimelapse stops the timelapse recorder for the given camera.
// The caller is responsible for updating the config Paused flag and persisting.
func (cm *CameraManager) PauseTimelapse(ctx context.Context, cameraID string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	rec, ok := cm.recorders[cameraID]
	if !ok {
		return nil // No recorder running, nothing to stop
	}

	if err := rec.Stop(); err != nil {
		logger.Warn("failed to stop timelapse recorder on pause", "camera_id", cameraID, "error", err)
	}
	delete(cm.recorders, cameraID)
	if cm.metrics != nil {
		cm.metrics.ActiveCameras.Dec()
	}
	logger.Info("timelapse recorder paused", "camera_id", cameraID)
	return nil
}

// ResumeTimelapse starts the timelapse recorder for the given camera if schedule allows.
// The caller is responsible for updating the config Paused flag and persisting.
func (cm *CameraManager) ResumeTimelapse(ctx context.Context, cameraID string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	// Check if already running
	if _, ok := cm.recorders[cameraID]; ok {
		return nil
	}

	// Find camera config
	var cam *config.CameraConfig
	for i := range cm.cfg.Cameras {
		if cm.cfg.Cameras[i].ID == cameraID {
			cam = &cm.cfg.Cameras[i]
			break
		}
	}
	if cam == nil {
		return &model.CameraNotFoundError{CameraID: cameraID}
	}
	if cam.Timelapse == nil {
		return fmt.Errorf("camera %q has no timelapse configuration", cameraID)
	}

	// Check schedule (Paused should already be false since caller sets it)
	if !cm.scheduler.IsRecordingTime(*cam.Timelapse) {
		logger.Info("timelapse resume: not recording time per schedule", "camera_id", cameraID)
		return nil
	}

	segDur, err := time.ParseDuration(cm.cfg.Storage.SegmentDuration)
	if err != nil {
		segDur = recorder.DefaultSegmentDur
	}

	rec := cm.createRecorder(*cam, segDur)
	if rec == nil {
		return nil
	}

	cm.recorders[cameraID] = rec
	if err := rec.Start(ctx); err != nil {
		delete(cm.recorders, cameraID)
		if cm.metrics != nil {
			cm.metrics.ActiveCameras.Dec()
		}
		return fmt.Errorf("failed to start timelapse recorder on resume: %w", err)
	}

	if cm.metrics != nil {
		cm.metrics.ActiveCameras.Inc()
	}
	logger.Info("timelapse recorder resumed", "camera_id", cameraID)
	return nil
}
