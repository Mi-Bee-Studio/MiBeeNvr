package camera

// This file holds the camera lifecycle entry points: Start (all cameras at
// startup), Stop (graceful shutdown), and the per-camera manual controls
// (StartCamera / StopCamera / RestartRecorder) plus the protocol-disable path.
// All of these serialize per-camera via withCameraLifecycle (registry.go) and
// perform slow I/O (rec.Start/Stop, ONVIF handshakes) OUTSIDE any registry
// lock; the snapshot is mutated only through the short apply() swap.
//
// Extracted from manager.go (#225). The struct definition, constructor, and
// type declarations remain in manager.go.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/recorder"
)

// Start creates and starts recorders for all enabled cameras in the config.
// If a single camera fails to start, it logs the error and continues with the rest.
func (cm *CameraManager) Start(ctx context.Context) error {
	// Serialized against Stop: app wiring launches Start detached, so a Stop
	// racing an in-flight Start would tear down state the registration is
	// still writing and leak the recorders it hasn't applied yet.
	cm.startStopMu.Lock()
	defer cm.startStopMu.Unlock()

	segDur, err := time.ParseDuration(cm.cfg.Storage.SegmentDuration)
	if err != nil {
		return fmt.Errorf("camera manager: invalid segment duration %q: %w", cm.cfg.Storage.SegmentDuration, err)
	}

	for _, cam := range cm.cfg.Cameras {
		// Insert camera record into database
		if err := cm.db.UpsertCamera(ctx, cam.ID, cam.Name, string(cam.Protocol), cam.Encoding, cam.URL, cam.Username, cam.Password, cam.ONVIFEndpoint, cam.ProfileToken, cam.StreamEncoding, cam.StableID); err != nil {
			logger.Error("failed to insert camera record", "camera_id", cam.ID, "error", err)
		} else {
			logger.Info("inserted camera record", "camera_id", cam.ID)
		}

		switch cam.Protocol {
		case string(model.ProtoRTSP), string(model.ProtoHTTP), string(model.ProtoGB28181):
			rec := cm.createRecorder(cam, segDur)
			if rec != nil {
				cm.apply(func(s *snapshot) *snapshot {
					s.recorders[cam.ID] = rec
					return s
				})
				if err := rec.Start(ctx); err != nil {
					logger.Error("failed to start recorder", "camera_id", cam.ID, "error", err)
					if cm.metrics != nil {
						cm.metrics.CameraConnectionErrorsTotal.WithLabelValues(cam.ID, classifyError(err)).Inc()
					}
				} else {
					logger.Info("started recorder", "camera_id", cam.ID, "protocol", cam.Protocol, "encoding", cam.Encoding)
					// GB28181 recorders are passive — pull media with a SIP
					// INVITE now that the recorder exists. This also heals a
					// session wired before startup finished (catalog-driven
					// INVITE racing camera-manager boot): the recycle inside
					// replaces the orphan-hub binding with this recorder.
					cm.autoInviteGB28181(cam)
					// Notify health manager of new camera with per-camera overrides
					var hOverrides *config.ResolvedHealthOverrides
					if cm.cfg.Health.Enabled {
						resolved := config.ResolveHealthOverrides(cm.cfg.Health, cam.HealthOverrides)
						hOverrides = &resolved
					}
					cm.healthMgr.OnCameraAdded(cam.ID, rec, hOverrides)
					// Check if recording is enabled (nil = default true).
					// When recording_enabled=true, timelapse frames come from recorded
					// segments via PeriodicMergeManager — skip starting the dedicated
					// capturer (keyframe extractor or frame poller) and rolling merge.
					recordingEnabled := cam.RecordingEnabled == nil || *cam.RecordingEnabled
					if recordingEnabled && cam.Timelapse != nil && cam.Timelapse.Enabled {
						logger.Info("skipping timelapse capturer + rolling merge: recording_enabled=true",
							"camera_id", cam.ID)
					} else {
						// Start keyframe extractor if camera has rtsp_keyframe timelapse config
						if effectiveDualModeFrameSource(cam) == "rtsp_keyframe" {
							// Runtime override: an ONVIF camera with empty encoding may have
							// resolved to rtsp_keyframe statically but actually be a JPEG device
							// (e.g. ESP32 MiBeeCam auto-detected as HTTPJPEG delegate). In that
							// case, use a frame poller instead.
							if isRecorderJPEG(rec) {
								if poller, perr := cm.startTimelapseFramePoller(cam.ID, cam, rec); perr != nil {
									logger.Error("failed to start timelapse frame poller", "camera_id", cam.ID, "error", perr)
								} else if poller != nil {
									cm.setFramePoller(cam.ID, poller)
								}
							} else if hub := getRecorderHub(rec); hub != nil {
								if err := cm.startTimelapseKeyframeExtractor(cam.ID, cam, hub, rec); err != nil {
									logger.Error("failed to start keyframe extractor", "camera_id", cam.ID, "error", err)
								}
							}
						} else if effectiveDualModeFrameSource(cam) == "latest_frame" {
							if poller, perr := cm.startTimelapseFramePoller(cam.ID, cam, rec); perr != nil {
								logger.Error("failed to start timelapse frame poller", "camera_id", cam.ID, "error", perr)
							} else if poller != nil {
								cm.setFramePoller(cam.ID, poller)
							}
						}
					}
					// Enforce timelapse schedule for dual-mode cameras (start/stop
					// the keyframe extractor or frame poller based on time-of-day).
					cm.startDualModeTimelapseScheduleMonitorForCamera(ctx, cam.ID, cam, rec)
				}
			}
		case string(model.ProtoONVIF):
			if err := cm.startRecorder(ctx, cam, segDur); err != nil {
				logger.Error("failed to start ONVIF recorder", "camera_id", cam.ID, "error", err)
			} else {
				logger.Info("started ONVIF recorder", "camera_id", cam.ID)
			}
		case string(model.ProtoSRT), string(model.ProtoRTMP):
			// Push/ingest cameras: the recorder waits for an incoming publisher
			// (it does not dial out). Its hub is registered in hubRegistry so the
			// SRT listener / RTMP server can find it.
			if err := cm.startRecorder(ctx, cam, segDur); err != nil {
				logger.Error("failed to start ingest recorder", "camera_id", cam.ID, "protocol", cam.Protocol, "error", err)
			} else {
				logger.Info("started ingest recorder, awaiting publisher",
					"camera_id", cam.ID, "protocol", cam.Protocol)
			}
		default:
			// Try plugin-registered protocols (e.g. xiaomi)
			if err := cm.startRecorder(ctx, cam, segDur); err != nil {
				logger.Warn("camera has unknown protocol, skipping", "camera_id", cam.ID, "protocol", cam.Protocol)
			} else {
				logger.Info("started plugin recorder", "camera_id", cam.ID, "protocol", cam.Protocol)
			}
		}
	}
	// Start recording schedule monitors for cameras with a recording_schedule configured.
	for _, cam := range cm.cfg.Cameras {
		if cam.RecordingSchedule != nil && len(cam.RecordingSchedule.TimeRanges) > 0 {
			cm.startRecordingScheduleMonitor(ctx, cam.ID)
		}
	}
	// Replay push-out relay targets for cameras loaded from config. Add/Update
	// reconcile targets at runtime, but Start() loads cameras directly from cfg —
	// without this, every service restart silently drops all push_targets
	// (push-status returns [] and no "relay target started" is ever logged).
	// Run in a goroutine per camera so Start isn't blocked on relay-engine init.
	if cm.relayMgr != nil {
		for _, cam := range cm.cfg.Cameras {
			if len(cam.PushTargets) == 0 {
				continue
			}
			cameraID := cam.ID
			targets := append([]config.PushTargetConfig(nil), cam.PushTargets...)
			go cm.relayMgr.SetCameraTargets(cameraID, targets)
		}
	}
	// Backfill stable_id from YAML config to DB for cameras that have a
	// YAML stable_id but no DB stable_id yet (e.g. pre-migration cameras).
	// For ONVIF cameras without stable_id, attempt to discover via ONVIF.
	// Non-blocking: runs in a goroutine and must not delay Start().
	if cm.db != nil {
		cm.backfillWg.Add(1)
		go func() {
			defer cm.backfillWg.Done()
			cm.backfillStableIDs(ctx)
		}()
	}
	// Backfill encoding from YAML config to DB (one-way sync for cameras whose
	// YAML has an encoding but DB doesn't — e.g. after a partial migration, or
	// when the column was added). Cameras with empty YAML encoding are handled
	// at runtime by ensureEncoding (triggered from startRecorderLocked when the
	// recorder probes the real codec). See issue #112.
	if cm.db != nil {
		cm.backfillWg.Add(1)
		go func() {
			defer cm.backfillWg.Done()
			cm.backfillEncoding(ctx)
		}()
	}
	return nil
}

// Stop stops all running recorders and waits for them to complete.
func (cm *CameraManager) Stop() error {
	// Blocks until an in-flight Start has finished registering recorders, so
	// the snapshot below always sees them (see startStopMu on Start).
	cm.startStopMu.Lock()
	defer cm.startStopMu.Unlock()

	// Snapshot the recorders (lock-free) then stop each OUTSIDE any lock —
	// rec.Stop can join a goroutine / do I/O and must not hold configMu.
	s := cm.loadSnapshot()
	recs := make([]model.Recorder, 0, len(s.recorders))
	for _, rec := range s.recorders {
		recs = append(recs, rec)
	}

	var errs []error
	for _, rec := range recs {
		if err := rec.Stop(); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("camera manager: %d recorder(s) failed to stop", len(errs))
	}

	cm.closeAllONVIFClients()

	// Stop all timelapse schedule monitors
	cm.auxMu.Lock()
	for _, cancel := range cm.scheduleMonitors {
		cancel()
	}
	cm.scheduleMonitors = make(map[string]context.CancelFunc)
	cm.auxMu.Unlock()

	// Stop all timelapse keyframe extractors
	cm.stopAllTimelapseKeyframeExtractors()

	// Stop all timelapse frame pollers
	cm.stopAllTimelapseFramePollers()

	// Wait for the startup stable_id backfill goroutine to exit. It holds the
	// db handle; without this wait it can race with resource teardown and crash
	// with "sql: database is closed". The goroutine checks ctx between cameras
	// so this returns promptly once the caller cancels the start context.
	cm.backfillWg.Wait()

	// Wait for any in-flight per-recorder ONVIF ensure* goroutines
	// (ensureStableID / ensureProfileToken / ensureEncoding). Each does a
	// configMu.Lock + persistConfig + DB write with a 15s timeout; joining
	// them here prevents those writes from racing the teardown performed by
	// Stop / the caller above (#163). Bounded by the ensure* 15s timeout.
	cm.onvifEnsureWg.Wait()

	return nil
}

// RestartRecorder stops and recreates the recorder for the given camera.
// The camera must be enabled. The stop+start is serialized per-camera via
// withCameraLifecycle so it can't race a concurrent restart/stop (which would
// leak a recorder or interleave with a health auto-remediation restart).
func (cm *CameraManager) RestartRecorder(ctx context.Context, cameraID string) error {
	cam := cm.snapshotConfig(cameraID)
	if cam == nil {
		return &model.CameraNotFoundError{CameraID: cameraID}
	}
	camCopy := *cam
	segDur, err := time.ParseDuration(cm.cfg.Storage.SegmentDuration)
	if err != nil {
		segDur = recorder.DefaultSegmentDur
	}
	return cm.withCameraLifecycle(cameraID, func() error {
		// Stop existing recorder (snapshot it, remove from registry via apply,
		// then Stop — rec.Stop can join a goroutine, runs under the per-camera
		// guard but NOT under any registry lock).
		if rec := cm.snapshotRecorder(cameraID); rec != nil {
			cm.apply(func(s *snapshot) *snapshot {
				delete(s.recorders, cameraID)
				return s
			})
			if err := rec.Stop(); err != nil {
				logger.Warn("failed to stop recorder", "camera_id", cameraID, "error", err)
			}
		}
		// Record reconnect attempt
		if cm.metrics != nil {
			cm.metrics.CameraReconnectAttemptsTotal.WithLabelValues(cameraID).Inc()
		}
		// startRecorderLocked registers via apply; safe to call under the guard
		// (it does not re-enter withCameraLifecycle).
		return cm.startRecorderLocked(ctx, camCopy, segDur)
	})
}

// StartCamera manually starts the recorder for the given camera. Serialized
// per-camera so it can't race a concurrent restart/stop.
func (cm *CameraManager) StartCamera(ctx context.Context, cameraID string) error {
	cam := cm.snapshotConfig(cameraID)
	if cam == nil {
		return &model.CameraNotFoundError{CameraID: cameraID}
	}
	camCopy := *cam
	segDur, err := time.ParseDuration(cm.cfg.Storage.SegmentDuration)
	if err != nil {
		segDur = recorder.DefaultSegmentDur
	}
	return cm.withCameraLifecycle(cameraID, func() error {
		// Check if already running — stale recorders (error/stopped) can be restarted
		if rec := cm.snapshotRecorder(cameraID); rec != nil {
			status := rec.Status()
			if status == model.StatusRecording || status == model.StatusReconnecting {
				return &model.CameraAlreadyRunningError{CameraID: cameraID}
			}
			// Stale recorder — stop and remove so we can start fresh
			cm.apply(func(s *snapshot) *snapshot {
				delete(s.recorders, cameraID)
				return s
			})
			if err := rec.Stop(); err != nil {
				logger.Warn("failed to stop stale recorder", "camera_id", cameraID, "error", err)
			}
			if cm.metrics != nil {
				cm.metrics.ActiveCameras.Dec()
			}
		}
		return cm.startRecorderLocked(ctx, camCopy, segDur)
	})
}

// StopCamera manually stops the recorder for the given camera. Serialized
// per-camera so it can't race a concurrent start/restart.
func (cm *CameraManager) StopCamera(_ context.Context, cameraID string) error {
	return cm.withCameraLifecycle(cameraID, func() error {
		rec := cm.snapshotRecorder(cameraID)
		if rec == nil {
			return fmt.Errorf("camera %q not found", cameraID)
		}

		// Remove from registry first (apply), then Stop (under the per-camera
		// guard, not under any registry lock).
		cm.apply(func(s *snapshot) *snapshot {
			delete(s.recorders, cameraID)
			delete(s.hubs, cameraID)
			return s
		})
		if err := rec.Stop(); err != nil {
			logger.Warn("failed to stop recorder", "camera_id", cameraID, "error", err)
		}
		if cm.metrics != nil {
			cm.metrics.ActiveCameras.Dec()
		}
		logger.Info("stopped recorder for camera", "camera_id", cameraID)

		// Stop keyframe extractor if running (snapshot under auxMu, stop outside)
		cm.auxMu.Lock()
		ext, hasExt := cm.keyframeExtractors[cameraID]
		if hasExt {
			delete(cm.keyframeExtractors, cameraID)
		}
		cm.auxMu.Unlock()
		if hasExt && ext != nil {
			if err := ext.Stop(); err != nil {
				logger.Warn("failed to stop keyframe extractor", "camera_id", cameraID, "error", err)
			}
		}

		// Stop frame poller if running
		cm.stopTimelapseFramePoller(cameraID)

		// Stop dual-mode timelapse schedule monitor if running
		cm.stopDualModeTimelapseScheduleMonitor(cameraID)

		return nil
	})
}

// SetProtocolEnabled enables or disables a protocol.
// When disabling, stops all cameras using that protocol.
// When enabling, no auto-start (user starts cameras manually).
func (cm *CameraManager) SetProtocolEnabled(protocol string, enabled bool) {
	if !enabled {
		cm.stopCamerasByProtocol(protocol)
	}
}

func (cm *CameraManager) stopCamerasByProtocol(protocol string) {
	// Snapshot the matching recorders (lock-free), remove each from the
	// registry via apply, then Stop OUTSIDE any lock.
	s := cm.loadSnapshot()
	type stopTarget struct {
		id  string
		rec model.Recorder
	}
	var targets []stopTarget
	for id, rec := range s.recorders {
		if c := s.configs[id]; c != nil && c.Protocol == protocol {
			targets = append(targets, stopTarget{id, rec})
		}
	}
	for _, t := range targets {
		cm.apply(func(st *snapshot) *snapshot {
			delete(st.recorders, t.id)
			return st
		})
		if err := t.rec.Stop(); err != nil {
			logger.Warn("failed to stop recorder", "camera_id", t.id, "error", err)
		}
		if cm.metrics != nil {
			cm.metrics.ActiveCameras.Dec()
		}
		// Stop keyframe extractor if running
		cm.auxMu.Lock()
		ext, ok := cm.keyframeExtractors[t.id]
		if ok {
			delete(cm.keyframeExtractors, t.id)
		}
		cm.auxMu.Unlock()
		if ok && ext != nil {
			if err := ext.Stop(); err != nil {
				logger.Warn("failed to stop keyframe extractor", "camera_id", t.id, "error", err)
			}
		}
		// Stop frame poller if running
		cm.stopTimelapseFramePoller(t.id)
		// Stop dual-mode timelapse schedule monitor
		cm.stopDualModeTimelapseScheduleMonitor(t.id)
	}
}

// getRecorderHub safely extracts the StreamHub from a recorder using type assertion.
// Returns nil if the recorder does not implement the hubber interface.
func getRecorderHub(rec model.Recorder) *model.StreamHub {
	type hubber interface {
		GetHub() *model.StreamHub
	}
	if h, ok := rec.(hubber); ok {
		return h.GetHub()
	}
	return nil
}

// classifyError categorizes a connection error into a Prometheus label value.
// Values: "timeout", "auth", "network", "unknown".
func classifyError(err error) string {
	if err == nil {
		return "unknown"
	}
	msg := err.Error()
	// Check for common error patterns
	switch {
	case strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline"):
		return "timeout"
	case strings.Contains(msg, "401") || strings.Contains(msg, "403") || strings.Contains(msg, "unauthorized") || strings.Contains(msg, "auth"):
		return "auth"
	case strings.Contains(msg, "connection refused") || strings.Contains(msg, "network") || strings.Contains(msg, "dial") || strings.Contains(msg, "no such host"):
		return "network"
	default:
		return "unknown"
	}
}
