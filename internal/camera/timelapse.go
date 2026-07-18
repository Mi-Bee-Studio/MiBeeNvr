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

// effectiveDualModeFrameSource resolves the effective frame source for dual-mode
// timelapse (regular recording camera with timelapse enabled alongside).
// "auto" is resolved to:
//   - "rtsp_keyframe" for RTSP/ONVIF cameras with h264/h265/empty encoding
//     (empty may be H.264 or H.265; resolved at runtime; if the recorder turns
//     out to be JPEG, the caller starts a frame poller instead).
//   - "latest_frame" for HTTP cameras (always JPEG) and RTSP MJPEG cameras.
//
// For ONVIF cameras with empty encoding, this returns "rtsp_keyframe" as the
// static guess, but the caller must verify at runtime via isRecorderJPEG and
// fall back to a frame poller if the recorder is actually a JPEG/MJPEG recorder.
func effectiveDualModeFrameSource(cam config.CameraConfig) string {
	if cam.Timelapse == nil || !cam.Timelapse.Enabled {
		return ""
	}
	fs := cam.Timelapse.FrameSource
	if fs == "" || fs == "auto" {
		switch cam.Protocol {
		case "http":
			// HTTP protocol cameras are always JPEG — poll LatestFrame().
			return "latest_frame"
		case "rtsp", "onvif":
			switch cam.Encoding {
			case "h264", "h265", "":
				return "rtsp_keyframe"
			case "mjpeg", "jpeg":
				return "latest_frame"
			}
		case "xiaomi", string(model.ProtoSRT), string(model.ProtoRTMP):
			// Xiaomi (P2P H.264), SRT, and RTMP ingest cameras all broadcast
			// H.264 NALUs to their StreamHub (via Hub.Broadcast). The keyframe
			// extractor subscribes to the same hub. These protocols don't
			// support LatestFrame() (no JPEG frame cache), so rtsp_keyframe
			// is the only viable path.
			return "rtsp_keyframe"
		}
	}
	return fs
}

// resolveTimelapseMergeMgr returns a timelapse rolling merge manager for the
// given camera, honoring per-camera merge_enabled and delete_original config.
//
// Resolution logic:
//   - If merge_enabled is explicitly false → return nil (skip rolling merge).
//   - If delete_original is true and differs from the global manager's setting,
//     create a per-camera manager with deleteOriginal=true.
//   - Otherwise, reuse the global manager (if set) or create a local fallback.
func (cm *CameraManager) resolveTimelapseMergeMgr(cam config.CameraConfig, interval time.Duration) *timelapse.RollingMergeManager {
	// merge_enabled: nil = auto (enabled), false = disabled, true = enabled
	if cam.Timelapse != nil && cam.Timelapse.MergeEnabled != nil && !*cam.Timelapse.MergeEnabled {
		return nil
	}

	deleteOriginal := false
	if cam.Timelapse != nil && cam.Timelapse.DeleteOriginal {
		deleteOriginal = true
	}

	// Reuse the global manager when it matches the desired deleteOriginal setting.
	// The global manager always has deleteOriginal=false (run.go constructs it that way),
	// so only reuse it when the camera also wants false.
	if cm.timelapseMergeMgr != nil && !deleteOriginal {
		return cm.timelapseMergeMgr
	}

	fps := 10
	if interval > 0 {
		fps = int(time.Second / interval)
		if fps < 1 {
			fps = 1
		}
	}
	return timelapse.NewRollingMergeManager(timelapse.NewAutoDetectMerger(), cm.db, fps, deleteOriginal)
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
		MergeMgr:    cm.resolveTimelapseMergeMgr(cam, interval),
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
	tlCfg.MergeMgr = cm.resolveTimelapseMergeMgr(cam, tlCfg.Interval)
	logger.Info("creating TimelapseRecorder for timelapse", "camera_id", cam.ID, "url", cam.URL)
	return recorder.NewTimelapseRecorder(tlCfg, cm.store)
}

// startTimelapseScheduleMonitor starts a goroutine that checks the scheduler
// every minute and starts/stops the timelapse recorder accordingly.
func (cm *CameraManager) startTimelapseScheduleMonitor(ctx context.Context, cameraID string, rec model.Recorder, tlCfg config.CameraTimelapseConfig) {
	ctx, cancel := context.WithCancel(ctx)
	cm.auxMu.Lock()
	if existing, ok := cm.scheduleMonitors[cameraID]; ok {
		existing()
	}
	cm.scheduleMonitors[cameraID] = cancel
	cm.auxMu.Unlock()

	go func() {
		defer func() {
			cm.auxMu.Lock()
			delete(cm.scheduleMonitors, cameraID)
			cm.auxMu.Unlock()
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
	cm.auxMu.Lock()
	defer cm.auxMu.Unlock()
	if cancel, ok := cm.scheduleMonitors[cameraID]; ok {
		cancel()
		delete(cm.scheduleMonitors, cameraID)
	}
}

// stopDualModeTimelapseScheduleMonitor cancels the dual-mode schedule monitor
// goroutine for the given camera (keyed as "tl-"+cameraID). Self-locking.
func (cm *CameraManager) stopDualModeTimelapseScheduleMonitor(cameraID string) {
	cm.auxMu.Lock()
	if cancel, ok := cm.scheduleMonitors["tl-"+cameraID]; ok {
		cancel()
		delete(cm.scheduleMonitors, "tl-"+cameraID)
	}
	cm.auxMu.Unlock()
}

// startDualModeTimelapseScheduleMonitor starts a goroutine that enforces the
// timelapse schedule for dual-mode cameras (regular recording + timelapse).
// Unlike the dedicated timelapse recorder schedule monitor (which stops the
// entire recorder), this only starts/stops the keyframe extractor or frame
// poller while keeping the regular recorder running.
//
// If no schedule is configured (nil), the extractor/poller runs 24/7 and no
// monitor goroutine is started.
func (cm *CameraManager) startDualModeTimelapseScheduleMonitor(
	ctx context.Context,
	cameraID string,
	cam config.CameraConfig,
	startFn func(),
	stopFn func(),
) {
	// No schedule = 24/7, no monitor needed.
	if cam.Timelapse == nil || cam.Timelapse.Schedule == nil || cam.Timelapse.Paused {
		return
	}

	ctx, cancel := context.WithCancel(ctx)
	cm.auxMu.Lock()
	if existing, ok := cm.scheduleMonitors["tl-"+cameraID]; ok {
		existing()
	}
	cm.scheduleMonitors["tl-"+cameraID] = cancel
	cm.auxMu.Unlock()

	go func() {
		defer func() {
			cm.auxMu.Lock()
			delete(cm.scheduleMonitors, "tl-"+cameraID)
			cm.auxMu.Unlock()
		}()

		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()

		isActive := cm.scheduler.IsRecordingTime(*cam.Timelapse)
		// Initial state: if not recording time, stop immediately.
		if !isActive {
			logger.Info("dual-mode timelapse schedule: outside recording hours, stopping timelapse component",
				"camera_id", cameraID)
			stopFn()
		}

		wasActive := isActive
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				isActive := cm.scheduler.IsRecordingTime(*cam.Timelapse)
				if isActive && !wasActive {
					logger.Info("dual-mode timelapse schedule: entering recording hours, starting timelapse component",
						"camera_id", cameraID)
					startFn()
				} else if !isActive && wasActive {
					logger.Info("dual-mode timelapse schedule: leaving recording hours, stopping timelapse component",
						"camera_id", cameraID)
					stopFn()
				}
				wasActive = isActive
			}
		}
	}()
}

// startDualModeTimelapseScheduleMonitorForCamera is a convenience wrapper that
// determines whether the camera uses a keyframe extractor or frame poller, then
// starts the dual-mode schedule monitor with the appropriate start/stop callbacks.
// The callbacks reference the camera config and recorder to re-create the
// extractor/poller when entering recording hours.
func (cm *CameraManager) startDualModeTimelapseScheduleMonitorForCamera(
	ctx context.Context,
	cameraID string,
	cam config.CameraConfig,
	rec model.Recorder,
) {
	if cam.Timelapse == nil || cam.Timelapse.Schedule == nil || cam.Timelapse.Paused {
		return
	}

	isJPEG := isRecorderJPEG(rec)
	usesKeyframe := effectiveDualModeFrameSource(cam) == "rtsp_keyframe" && !isJPEG

	startFn := func() {
		if usesKeyframe {
			rec := cm.snapshotRecorder(cameraID)
			if hub := getRecorderHub(rec); hub != nil {
				if err := cm.startTimelapseKeyframeExtractor(cameraID, cam, hub, rec); err != nil {
					logger.Warn("dual-mode schedule: failed to start keyframe extractor", "camera_id", cameraID, "error", err)
				}
			}
		} else {
			if rec := cm.snapshotRecorder(cameraID); rec != nil {
				if poller, err := cm.startTimelapseFramePoller(cameraID, cam, rec); err != nil {
					logger.Warn("dual-mode schedule: failed to start frame poller", "camera_id", cameraID, "error", err)
				} else if poller != nil {
					cm.setFramePoller(cameraID, poller)
				}
			}
		}
	}
	stopFn := func() {
		if usesKeyframe {
			cm.stopTimelapseKeyframeExtractor(cameraID)
		} else {
			cm.stopTimelapseFramePoller(cameraID)
		}
	}

	cm.startDualModeTimelapseScheduleMonitor(ctx, cameraID, cam, startFn, stopFn)
}

// startRecordingScheduleMonitor starts a goroutine that starts/stops a camera's
// recorder based on its recording_schedule config (e.g. daytime-only recording).
// This reuses the timelapse scheduler's IsScheduleActive for time-range evaluation.
// The monitor checks every minute and transitions the recorder state accordingly.
func (cm *CameraManager) startRecordingScheduleMonitor(ctx context.Context, cameraID string) {
	cm.auxMu.Lock()
	// Cancel any existing monitor for this camera.
	if cancel, ok := cm.scheduleMonitors["rec-"+cameraID]; ok {
		cancel()
	}
	monitorCtx, cancel := context.WithCancel(ctx)
	cm.scheduleMonitors["rec-"+cameraID] = cancel
	cm.auxMu.Unlock()

	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()

		// Initial check: stop if not recording time.
		var schedule *config.ScheduleConfig
		for _, c := range cm.cfg.Cameras {
			if c.ID == cameraID {
				schedule = c.RecordingSchedule
				break
			}
		}

		if schedule == nil {
			return
		}

		if !cm.scheduler.IsScheduleActive(schedule) {
			logger.Info("recording schedule: outside active hours, stopping recorder", "camera_id", cameraID)
			if rec := cm.snapshotRecorder(cameraID); rec != nil {
				if err := rec.Stop(); err != nil {
					logger.Warn("failed to stop recorder per recording schedule", "camera_id", cameraID, "error", err)
				}
			}
		}

		for {
			select {
			case <-monitorCtx.Done():
				return
			case <-ticker.C:
				isActive := cm.scheduler.IsScheduleActive(schedule)
				rec := cm.snapshotRecorder(cameraID)
				if rec == nil {
					continue
				}
				status := rec.Status()
				if isActive && (status == model.StatusStopped || status == model.StatusError) {
					logger.Info("recording schedule: active hours, starting recorder", "camera_id", cameraID)
					if err := rec.Start(monitorCtx); err != nil {
						logger.Warn("failed to start recorder per recording schedule", "camera_id", cameraID, "error", err)
					}
				} else if !isActive && (status == model.StatusRecording || status == model.StatusReconnecting) {
					logger.Info("recording schedule: outside active hours, stopping recorder", "camera_id", cameraID)
					if err := rec.Stop(); err != nil {
						logger.Warn("failed to stop recorder per recording schedule", "camera_id", cameraID, "error", err)
					}
				}
			}
		}
	}()
}

// startTimelapseKeyframeExtractor creates and starts a KeyframeExtractor for the given camera,
// subscribing it to the provided StreamHub. The extractor is stored in the manager for lifecycle management.
func (cm *CameraManager) startTimelapseKeyframeExtractor(cameraID string, cam config.CameraConfig, hub *model.StreamHub, rec model.Recorder) error {
	if effectiveDualModeFrameSource(cam) != "rtsp_keyframe" {
		return nil
	}

	interval := 5 * time.Second
	if d, err := time.ParseDuration(cam.Timelapse.Interval); err == nil && d >= time.Millisecond {
		interval = d
	}

	// Determine H.265 from config OR from the actual recorder type.
	// ONVIF cameras auto-detect encoding at runtime via RTSP DESCRIBE,
	// so cam.Encoding may be empty even though the stream is H.265.
	isH265 := cam.Encoding == "h265" || cam.StreamEncoding == "H265" || isRecorderH265(rec)

	mergeMgr := cm.resolveTimelapseMergeMgr(cam, interval)

	// CodecParamsProvider: fetches SPS/PPS/VPS from the live recorder. Used as a
	// fallback when the camera sends parameter sets out-of-band (not inline with
	// frame AUs) — prevents "frames missing SPS" merge failures.
	var codecProvider func() (sps, pps, vps []byte)
	if hlsProv, ok := rec.(model.HLSProvider); ok {
		codecProvider = func() ([]byte, []byte, []byte) {
			_, sps, pps, vps := hlsProv.CodecParams()
			return sps, pps, vps
		}
	}

	extractor := timelapse.NewKeyframeExtractor(timelapse.KeyframeExtractorConfig{
		CameraID:            cameraID,
		Interval:            interval,
		SegmentDur:          10 * time.Minute,
		IsH265:              isH265,
		Store:               cm.store,
		DB:                  cm.db,
		MergeMgr:            mergeMgr,
		CodecParamsProvider: codecProvider,
	})

	ctx := context.Background()
	if err := extractor.Start(ctx, hub); err != nil {
		return err
	}

	cm.auxMu.Lock()
	cm.keyframeExtractors[cameraID] = extractor
	cm.auxMu.Unlock()

	logger.Info("started timelapse keyframe extractor", "camera_id", cameraID, "interval", interval)
	return nil
}

// stopTimelapseKeyframeExtractor stops and removes the keyframe extractor for the given camera.
func (cm *CameraManager) stopTimelapseKeyframeExtractor(cameraID string) {
	cm.auxMu.Lock()
	extractor, ok := cm.keyframeExtractors[cameraID]
	if ok {
		delete(cm.keyframeExtractors, cameraID)
	}
	cm.auxMu.Unlock()

	if ok && extractor != nil {
		if err := extractor.Stop(); err != nil {
			logger.Warn("failed to stop keyframe extractor", "camera_id", cameraID, "error", err)
		}
		logger.Info("stopped timelapse keyframe extractor", "camera_id", cameraID)
	}
}

// stopTimelapseFramePoller stops and removes the frame poller for the given
// camera. Self-locking (takes cm.auxMu to snapshot the poller, then stops it
// outside the lock since Stop can do I/O).
func (cm *CameraManager) stopTimelapseFramePoller(cameraID string) {
	cm.auxMu.Lock()
	poller, ok := cm.framePollers[cameraID]
	if ok {
		delete(cm.framePollers, cameraID)
	}
	cm.auxMu.Unlock()
	if ok && poller != nil {
		if err := poller.Stop(); err != nil {
			logger.Warn("failed to stop timelapse frame poller", "camera_id", cameraID, "error", err)
		}
		logger.Info("stopped timelapse frame poller", "camera_id", cameraID)
	}
}

// setFramePoller stores a frame poller for the camera (replacing any existing).
func (cm *CameraManager) setFramePoller(cameraID string, poller *timelapse.SnapshotCapturer) {
	cm.auxMu.Lock()
	cm.framePollers[cameraID] = poller
	cm.auxMu.Unlock()
}

// stopAllTimelapseKeyframeExtractors stops all keyframe extractors (used during manager Stop).
func (cm *CameraManager) stopAllTimelapseKeyframeExtractors() {
	cm.auxMu.Lock()
	extractors := make([]*timelapse.KeyframeExtractor, 0, len(cm.keyframeExtractors))
	for _, ext := range cm.keyframeExtractors {
		extractors = append(extractors, ext)
	}
	cm.keyframeExtractors = make(map[string]*timelapse.KeyframeExtractor)
	cm.auxMu.Unlock()

	for _, ext := range extractors {
		if err := ext.Stop(); err != nil {
			logger.Warn("failed to stop keyframe extractor", "error", err)
		}
	}
}

// stopAllTimelapseFramePollers stops all frame pollers (used during manager Stop).
func (cm *CameraManager) stopAllTimelapseFramePollers() {
	cm.auxMu.Lock()
	pollers := make([]*timelapse.SnapshotCapturer, 0, len(cm.framePollers))
	for _, p := range cm.framePollers {
		pollers = append(pollers, p)
	}
	cm.framePollers = make(map[string]*timelapse.SnapshotCapturer)
	cm.auxMu.Unlock()

	for _, p := range pollers {
		if err := p.Stop(); err != nil {
			logger.Warn("failed to stop timelapse frame poller", "error", err)
		}
	}
}

// PauseTimelapse stops the timelapse recorder for the given camera.
// The caller is responsible for updating the config Paused flag and persisting.
// Serialized per-camera via withCameraLifecycle so it can't race a concurrent
// ResumeTimelapse/restart (which would leak a recorder or leave one running
// after a pause).
func (cm *CameraManager) PauseTimelapse(ctx context.Context, cameraID string) error {
	return cm.withCameraLifecycle(cameraID, func() error {
		// Snapshot the recorder (lock-free), remove from registry via apply, then
		// Stop OUTSIDE any registry lock (Stop can join a goroutine). Safe under
		// the per-camera guard — no concurrent resume/restart can interleave.
		rec := cm.snapshotRecorder(cameraID)
		if rec == nil {
			return nil // No recorder running, nothing to stop
		}
		cm.apply(func(s *snapshot) *snapshot {
			delete(s.recorders, cameraID)
			return s
		})
		if err := rec.Stop(); err != nil {
			logger.Warn("failed to stop timelapse recorder on pause", "camera_id", cameraID, "error", err)
		}
		if cm.metrics != nil {
			cm.metrics.ActiveCameras.Dec()
		}
		logger.Info("timelapse recorder paused", "camera_id", cameraID)
		return nil
	})
}

// ResumeTimelapse starts the timelapse recorder for the given camera if schedule allows.
// The caller is responsible for updating the config Paused flag and persisting.
// Serialized per-camera via withCameraLifecycle so two concurrent resume calls
// (or resume vs pause/restart) can't both construct a recorder and leak the loser.
func (cm *CameraManager) ResumeTimelapse(ctx context.Context, cameraID string) error {
	// Find camera config + parse segDur up front (no guard needed for reads).
	cam := cm.snapshotConfig(cameraID)
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
	camCopy := *cam

	return cm.withCameraLifecycle(cameraID, func() error {
		// Re-check under the guard: another resume/restart may have started a
		// recorder while we were waiting for the per-camera mutex.
		if rec := cm.snapshotRecorder(cameraID); rec != nil {
			return nil
		}

		// createRecorder + rec.Start run OUTSIDE any registry lock (createRecorder
		// registers its hub via apply; Start is a network dial). The recorder is
		// registered in the snapshot via apply after a successful Start. Safe under
		// the per-camera guard — no concurrent pause/restart can interleave.
		rec := cm.createRecorder(camCopy, segDur)
		if rec == nil {
			return nil
		}

		if err := rec.Start(ctx); err != nil {
			if cm.metrics != nil {
				cm.metrics.ActiveCameras.Dec()
			}
			return fmt.Errorf("failed to start timelapse recorder on resume: %w", err)
		}
		cm.apply(func(s *snapshot) *snapshot {
			s.recorders[cameraID] = rec
			return s
		})

		if cm.metrics != nil {
			cm.metrics.ActiveCameras.Inc()
		}
		logger.Info("timelapse recorder resumed", "camera_id", cameraID)
		return nil
	})
}

// isRecorderH265 checks if the given recorder (or its delegate for ONVIF recorders)
// is actually an H.265 recorder. This is needed because ONVIF cameras auto-detect
// encoding at runtime via RTSP DESCRIBE, so cam.Encoding may be empty even though
// the stream is H.265.
func isRecorderH265(rec model.Recorder) bool {
	// Check for ONVIF recorder with delegate
	type delegater interface {
		Delegate() model.Recorder
	}
	if d, ok := rec.(delegater); ok {
		if delegate := d.Delegate(); delegate != nil {
			rec = delegate
		}
	}
	// Check by type name to avoid importing recorder package.
	// H265Recorder from internal/recorder will match.
	typeName := fmt.Sprintf("%T", rec)
	return strings.Contains(typeName, "H265Recorder")
}

// isRecorderJPEG checks if the given recorder (or its delegate for ONVIF recorders)
// is a JPEG/MJPEG recorder that can provide frames via a LatestFrame() method.
// This is the runtime complement to effectiveDualModeFrameSource: an ONVIF camera
// with empty encoding may statically resolve to "rtsp_keyframe" but actually be a
// JPEG device (e.g. ESP32 MiBeeCam auto-detected as HTTPJPEG delegate).
func isRecorderJPEG(rec model.Recorder) bool {
	type delegater interface {
		Delegate() model.Recorder
	}
	if d, ok := rec.(delegater); ok {
		if delegate := d.Delegate(); delegate != nil {
			rec = delegate
		}
	}
	typeName := fmt.Sprintf("%T", rec)
	return strings.Contains(typeName, "HTTPJPEGRecorder") || strings.Contains(typeName, "MJPEGRecorder")
}

// latestFramer is implemented by recorders that cache the latest JPEG frame.
// Both HTTPJPEGRecorder and MJPEGRecorder (after the LatestFrame addition) satisfy it.
type latestFramer interface {
	LatestFrame() []byte
}

// resolveLatestFramer extracts a LatestFrame provider from the recorder, following
// the ONVIF delegate chain. Returns nil if the recorder does not expose frames.
func resolveLatestFramer(rec model.Recorder) func() []byte {
	type delegater interface {
		Delegate() model.Recorder
	}
	if d, ok := rec.(delegater); ok {
		if delegate := d.Delegate(); delegate != nil {
			rec = delegate
		}
	}
	lr, ok := rec.(latestFramer)
	if !ok {
		return nil
	}
	return lr.LatestFrame
}

// startTimelapseFramePoller creates and starts a SnapshotCapturer configured to
// poll the running recorder's LatestFrame() in-memory cache instead of opening
// a second HTTP connection. Used for dual-mode MJPEG/JPEG timelapse.
func (cm *CameraManager) startTimelapseFramePoller(cameraID string, cam config.CameraConfig, rec model.Recorder) (*timelapse.SnapshotCapturer, error) {
	frameProvider := resolveLatestFramer(rec)
	if frameProvider == nil {
		logger.Warn("recorder does not support LatestFrame for timelapse frame polling",
			"camera_id", cameraID)
		return nil, nil //nolint:nilnil // TODO(#storage-overhaul): callers check for nil poller to skip dual-mode frame polling.
	}

	interval := 30 * time.Second
	if d, err := time.ParseDuration(cam.Timelapse.Interval); err == nil && d >= time.Millisecond {
		interval = d
	}

	segDur := 10 * time.Minute
	if d, err := time.ParseDuration(cm.cfg.Storage.SegmentDuration); err == nil && d >= time.Minute {
		segDur = d
	}

	mergeMgr := cm.resolveTimelapseMergeMgr(cam, interval)

	snapCfg := timelapse.SnapshotCapturerConfig{
		CameraID:      cameraID,
		Interval:      interval,
		SegmentDur:    segDur,
		DB:            cm.db,
		Store:         cm.store,
		Metrics:       cm.metrics,
		MergeMgr:      mergeMgr,
		FrameProvider: frameProvider,
	}
	capturer := timelapse.NewSnapshotCapturer(snapCfg, cm.store, cm.metrics)

	ctx := context.Background()
	if err := capturer.Start(ctx); err != nil {
		return nil, err
	}

	logger.Info("started timelapse frame poller",
		"camera_id", cameraID,
		"interval", interval,
		"frame_source", "latest_frame")
	return capturer, nil
}
