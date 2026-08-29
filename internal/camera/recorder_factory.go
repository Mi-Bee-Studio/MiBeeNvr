package camera

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/streamhub"
)

// createRecorder creates a recorder for the given camera config by looking
// the protocol up in recorderBuilders. Returns nil for unknown protocols or
// unsupported protocol/encoding pairs.
func (cm *CameraManager) createRecorder(cam config.CameraConfig, segDur time.Duration) model.Recorder {
	build, ok := recorderBuilders[cam.Protocol]
	if !ok {
		return nil
	}
	rec := build(cm, cam, segDur)
	if rec == nil {
		return nil
	}

	// Initialize StreamHub for frame fan-out on all recorders
	initStreamHub(rec, cam.ID, cm.metrics)
	// Register the recorder's hub in the central registry so that push ingest
	// servers (SRT listener / RTMP server) share the SAME hub object and their
	// frames reach the live consumers (HLS/WebRTC/FLV/WS) attached on demand.
	// Published via apply() (configMu held only for the map swap) — fixes the
	// previous contract gap where createRecorder wrote hubRegistry with no lock.
	if hub := getRecorderHub(rec); hub != nil {
		hubToRegister := hub
		cm.apply(func(s *snapshot) *snapshot {
			s.hubs[cam.ID] = hubToRegister
			return s
		})
	}
	return rec
}

// initStreamHub sets a new StreamHub on the recorder via the streamhub.HubHost
// interface. It sets the cameraID for structured logging, labels the hub
// source for the flow-path view (from the recorder's own HubSource), and
// wires the standard observability callbacks (shared with push hubs via
// wireHubMetrics). Recorders without a hub (none today) are skipped.
func initStreamHub(rec model.Recorder, cameraID string, m *metrics.Metrics) {
	host, ok := rec.(streamhub.HubHost)
	if !ok {
		return
	}
	hub := streamhub.New()
	host.SetHub(hub)
	hub.SetCameraID(cameraID)
	hub.SetSource(host.HubSource())
	wireHubMetrics(hub, cameraID, m)
}

// autoInviteGB28181 recycles any live session for the camera's channel and
// re-INVITEs so the session's AU callback binds THIS recorder instance — GB
// recorders are passive and produce no media without a SIP INVITE. Fired on
// every GB28181 recorder start, including camera-manager boot: a lower
// platform hammering re-REGISTER mid-boot can get its catalog-driven INVITE
// in before the recorder exists, and that session's orphan-hub wiring would
// stick forever (the receiver keeps draining, so no watchdog ever recycles
// it). No-op for non-GB cameras or when the inviter isn't wired.
func (cm *CameraManager) autoInviteGB28181(cam config.CameraConfig) {
	if cam.Protocol != string(model.ProtoGB28181) || cm.gb28181Inviter == nil {
		return
	}
	deviceID, channelID := cam.GB28181.DeviceID, cam.GB28181.ChannelID
	if channelID == "" {
		return // no channel binding to invite on
	}
	cameraID := cam.ID
	inviter := cm.gb28181Inviter
	ender := cm.gb28181SessionEnder
	// Run detached: INVITE involves network I/O (and Bye + re-INVITE churn)
	// that must not delay recorder startup.
	go func() {
		// A recorder restart replaces the recorder instance, but a live
		// session's AU callback still feeds the OLD one — recycle the session
		// first so the INVITE below binds the new recorder.
		if ender != nil {
			_ = ender.ByeChannelByID(channelID)
		}
		if err := inviter.InviteChannel(deviceID, channelID); err != nil {
			logger.Warn("gb28181: auto-INVITE failed", "camera_id", cameraID, "error", err)
		}
	}()
}

// startRecorder creates and starts a recorder for the given camera config.
//
// Concurrency: safe to call from any goroutine. All registry mutations go
// through apply() (configMu, nanosecond map swap); rec.Start (network dial) and
// the timelapse sub-helpers run OUTSIDE any lock. The recorder is registered in
// the snapshot via apply; on failure it is removed via apply. Serialized
// per-camera via withCameraLifecycle so two concurrent startRecorder calls for
// the same camera (e.g. manual restart + health auto-remediation) can't both
// construct a recorder and leak the loser.
func (cm *CameraManager) startRecorder(ctx context.Context, cam config.CameraConfig, segDur time.Duration) error {
	return cm.withCameraLifecycle(cam.ID, func() error {
		return cm.startRecorderLocked(ctx, cam, segDur)
	})
}

// startRecorderLocked is the body of startRecorder, called under the per-camera
// lifecycle guard.
func (cm *CameraManager) startRecorderLocked(ctx context.Context, cam config.CameraConfig, segDur time.Duration) error {
	rec := cm.createRecorder(cam, segDur)
	if rec == nil {
		return fmt.Errorf("camera %q: protocol %q does not support recording", cam.ID, cam.Protocol)
	}
	// Register the recorder in the snapshot before Start so concurrent readers
	// (statusSnapshot, GetRecorder) observe it immediately.
	cm.apply(func(s *snapshot) *snapshot {
		s.recorders[cam.ID] = rec
		return s
	})

	// For timelapse recorders, check scheduler before starting
	if cam.Protocol == "timelapse" && cam.Timelapse != nil {
		if !cm.scheduler.IsRecordingTime(*cam.Timelapse) {
			logger.Info("timelapse schedule: not recording time, delaying start", "camera_id", cam.ID)
			// Start schedule monitor anyway so it can start the recorder when the schedule says so
			cm.startTimelapseScheduleMonitor(ctx, cam.ID, rec, *cam.Timelapse)
			return nil
		}
	}

	// Detach the recorder's lifecycle from the caller's context. Recorders run
	// long-lived background goroutines (reconnect loop, frame watchdog) that must
	// outlive short-lived callers — notably the HTTP request ctx from POST
	// /api/cameras/{id}/start, whose cancellation on response return would
	// silently kill the recorder (the run loop exits via ctx.Err() with no error
	// log, so the camera appears to "start" then immediately stops). Each
	// recorder derives its own cancellable child ctx from the one passed to
	// Start and stores the cancel func for its Stop() — so passing
	// context.Background() means the recorder's lifetime is governed solely by
	// its own Stop(), which is what CameraManager.Stop / StopCamera /
	// RestartRecorder use to drive shutdown.
	if err := rec.Start(context.Background()); err != nil {
		cm.apply(func(s *snapshot) *snapshot {
			delete(s.recorders, cam.ID)
			return s
		})
		// Record connection error metric
		if cm.metrics != nil {
			cm.metrics.CameraConnectionErrorsTotal.WithLabelValues(cam.ID, classifyError(err)).Inc()
		}
		// Track this camera as failed-to-start so the health manager's status
		// loop can see it (it's no longer in the snapshot) and drive the
		// auto-remediation → IP rediscovery self-healing chain. Without this,
		// a camera whose IP changed would be silently stuck forever.
		cm.markStartFailed(cam.ID, err)
		return fmt.Errorf("camera %q: failed to start recorder: %w", cam.ID, err)
	}

	// Start schedule monitor for timelapse recorders
	if cam.Protocol == "timelapse" && cam.Timelapse != nil {
		cm.startTimelapseScheduleMonitor(ctx, cam.ID, rec, *cam.Timelapse)
	}

	// Start keyframe extractor for recorders with rtsp_keyframe timelapse config.
	// When recording is enabled (nil or true), timelapse frames are extracted from
	// recorded segments by PeriodicMergeManager (RecordingFrameExtractor) — no
	// dedicated live capturer is needed. Starting the KeyframeExtractor here in
	// that case is redundant and, for cameras that send parameter sets only inline
	// with IDR frames (e.g. Xiaomi H.264), produces frame files without SPS/PPS
	// that the merger later rejects as "frames missing SPS" (issue #90).
	if effectiveDualModeFrameSource(cam) == "rtsp_keyframe" {
		recordingEnabled := cam.RecordingEnabled == nil || *cam.RecordingEnabled
		// Runtime override: an ONVIF camera with empty encoding may have resolved
		// to rtsp_keyframe statically but actually be a JPEG device. Use a frame
		// poller instead.
		if isRecorderJPEG(rec) && !recordingEnabled {
			if poller, perr := cm.startTimelapseFramePoller(cam.ID, cam, rec); perr != nil {
				logger.Error("failed to start timelapse frame poller", "camera_id", cam.ID, "error", perr)
			} else if poller != nil {
				cm.setFramePoller(cam.ID, poller)
			}
		} else if !recordingEnabled {
			if hub := getRecorderHub(rec); hub != nil {
				if err := cm.startTimelapseKeyframeExtractor(cam.ID, cam, hub, rec); err != nil {
					logger.Error("failed to start keyframe extractor", "camera_id", cam.ID, "error", err)
				}
			}
		} else {
			logger.Debug("timelapse keyframe extractor skipped: recording enabled (frames from PeriodicMergeManager)",
				"camera_id", cam.ID)
		}
	} else if effectiveDualModeFrameSource(cam) == "latest_frame" {
		if poller, perr := cm.startTimelapseFramePoller(cam.ID, cam, rec); perr != nil {
			logger.Error("failed to start timelapse frame poller", "camera_id", cam.ID, "error", perr)
		} else if poller != nil {
			cm.setFramePoller(cam.ID, poller)
		}
	}

	// Enforce timelapse schedule for dual-mode cameras.
	cm.startDualModeTimelapseScheduleMonitorForCamera(ctx, cam.ID, cam, rec)

	cm.errorDetailsMu.Lock()
	cm.errorDetails[cam.ID] = nil
	cm.errorDetailsMu.Unlock()
	// The recorder started successfully — clear any prior failed-start tracking
	// so statusFunc stops reporting it as StatusError (it now has a real recorder
	// whose status is the source of truth). This closes the self-healing loop.
	cm.clearStartFailed(cam.ID)
	if cm.metrics != nil {
		cm.metrics.ActiveCameras.Inc()
	}
	// Notify health manager of new camera with per-camera overrides
	var overrides *config.ResolvedHealthOverrides
	if cm.cfg.Health.Enabled {
		resolved := config.ResolveHealthOverrides(cm.cfg.Health, cam.HealthOverrides)
		overrides = &resolved
	}
	cm.healthMgr.OnCameraAdded(cam.ID, rec, overrides)
	logger.Info("started recorder for camera", "camera_id", cam.ID)

	cm.autoInviteGB28181(cam)

	// For ONVIF cameras without a valid stable_id yet, auto-populate it
	// asynchronously so IP self-healing (internal/rediscovery) can later
	// re-acquire the camera. The guard uses IsValidStableID (not just non-empty)
	// so a dirty value frozen in YAML (IP/URL/all-zero — see #216) triggers a
	// fresh ONVIF lookup that overwrites it with the real serial. Best-effort
	// and non-blocking: failures are logged and ignored. Tracked by
	// onvifEnsureWg so Stop can join these goroutines and avoid a race between
	// their configMu.Lock + persistConfig + DB write and the teardown Stop
	// initiates (#163).
	if (cam.Protocol == "onvif" || cam.Protocol == string(model.ProtoONVIF)) && !config.IsValidStableID(cam.StableID) {
		cm.launchTrackedEnsure(cm.ensureStableID, cam.ID)
	}
	// For ONVIF cameras without a profile_token, persist the auto-selected one
	// after Start resolves it — avoids re-running GetProfiles on every restart.
	if (cam.Protocol == "onvif" || cam.Protocol == string(model.ProtoONVIF)) && strings.TrimSpace(cam.ProfileToken) == "" {
		cm.launchTrackedEnsure(cm.ensureProfileToken, cam.ID)
	}
	// Sub-stream auto-discovery (#512): resolve and persist the ONVIF
	// secondary profile once. Fill-once — a manual sub_profile_token is
	// never overwritten.
	if cam.Protocol == "onvif" || cam.Protocol == string(model.ProtoONVIF) {
		cm.launchTrackedEnsure(cm.ensureSubProfileToken, cam.ID)
	}
	// For ONVIF cameras without a resolved encoding, persist the probe result
	// (RTSP DESCRIBE / ONVIF profile) so a later device outage doesn't leave
	// encoding="" — which makes the frontend lose the codec and storm through
	// the protocol chain. Mirrors ensureStableID/ensureProfileToken. See #112.
	if (cam.Protocol == "onvif" || cam.Protocol == string(model.ProtoONVIF)) && strings.TrimSpace(cam.Encoding) == "" {
		cm.launchTrackedEnsure(cm.ensureEncoding, cam.ID)
	}
	return nil
}

// launchTrackedEnsure runs a best-effort ONVIF ensure* pass for a camera in a
// goroutine tracked by onvifEnsureWg, so Stop can join it and prevent the
// ensure write (configMu.Lock + persistConfig + DB write) from racing the
// teardown Stop initiates (#163). Each ensure* uses its own 15s timeout ctx,
// so worst-case a tracked goroutine blocks Stop for ≤15s.
func (cm *CameraManager) launchTrackedEnsure(fn func(string), cameraID string) {
	cm.onvifEnsureWg.Add(1)
	go func() {
		defer cm.onvifEnsureWg.Done()
		fn(cameraID)
	}()
}

// persistConfig saves the current config to disk if configPath is set.
func (cm *CameraManager) persistConfig() error {
	if cm.configPath != "" {
		if err := config.Save(cm.configPath, cm.cfg); err != nil {
			return fmt.Errorf("camera manager: failed to save config: %w", err)
		}
	}
	return nil
}
