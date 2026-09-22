package app

// builders_recording.go — buildAppDeps phase 2: merge coordinators, transcode
// manager, timezone, timelapse rolling merge, camera manager, vision push,
// health manager and the periodic-merge scheduler.

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/camera"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/health"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/merge"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/timelapse"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/transcoding"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/vision"
)

// buildRecordingDeps mutates deps with the recording/merge domain. No failure
// path: every optional manager here degrades to nil or a WARN.
func buildRecordingDeps(deps *appDeps) {
	cfg, configPath := deps.cfg, deps.configPath
	db := deps.db
	store := deps.store
	m := deps.metrics

	// Step 5: Merge manager (created before camera manager so ArchiveCamera can use it)
	deps.mergeMgr = merge.NewMergeManager(
		db, store,
		func() config.MergeConfig { return cfg.Merge },
		func(cameraID string) *config.MergeConfig {
			for _, c := range cfg.Cameras {
				if c.ID == cameraID {
					return c.Merge
				}
			}
			return nil
		},
		func(cameraID string) *config.AdaptiveRecordingConfig {
			for _, c := range cfg.Cameras {
				if c.ID == cameraID {
					return c.Adaptive
				}
			}
			return nil
		},
		func() []config.CameraConfig { return cfg.Cameras },
		m,
	)

	// Step 5.1: Rolling merge coordinator (quasi-real-time, event-driven).
	// Subscribes to SegmentCompleted and merges segments into per-camera window
	// buckets within seconds. Independent of the periodic MergeManager above.
	deps.recordRollingMergeMgr = merge.NewRollingMergeCoordinator(
		db, store,
		func() config.MergeConfig { return cfg.Merge },
		func(cameraID string) *config.MergeConfig {
			for _, c := range cfg.Cameras {
				if c.ID == cameraID {
					return c.Merge
				}
			}
			return nil
		},
		func(cameraID string) *config.AdaptiveRecordingConfig {
			for _, c := range cfg.Cameras {
				if c.ID == cameraID {
					return c.Adaptive
				}
			}
			return nil
		},
		func() []config.CameraConfig { return cfg.Cameras },
		m,
		deps.eventBus,
	)
	// #817 follow-up (M5 2026-09-16): the age rail must resolve transcode
	// enablement EXACTLY like the task creator (global + per-camera merge).
	// The constructor's per-camera-block-only lookup misclassified DB-managed
	// cameras (absent from the yaml snapshot, resolved against the global
	// switch) and silently disabled the rail while their tasks kept flowing.
	deps.recordRollingMergeMgr.SetCameraTranscodeEnabled(func(cameraID string) bool {
		return cfg.ResolveTranscodingConfig(cameraID).Enabled
	})

	// Step 5.5: Transcode manager (after merge, before camera)
	var transcodeMgr *transcoding.TranscodeManager
	if cfg.Transcoding.Enabled {
		ffmpegPath := cfg.Transcoding.FFmpegPath
		// Leave empty to let probe auto-detect via exec.LookPath
		// Only override when user explicitly configured a custom path
		mgr, err := transcoding.NewTranscodeManager(db, transcoding.ManagerConfig{
			Transcoding:     cfg.Transcoding,
			DataDir:         cfg.Storage.RootDir,
			FFmpegPath:      ffmpegPath,
			MaxWorkers:      cfg.Transcoding.MaxWorkers,
			ReplaceOriginal: true,
			EventBus:        deps.eventBus,
			Config:          cfg,
		}, m)
		if err != nil {
			slog.Warn("Transcoding disabled — FFmpeg is an OPTIONAL dependency; all other features (recording, playback, live streaming, relay, timelapse, merge) work without it. To enable transcoding, install ffmpeg/ffprobe or use the in-app downloader.",
				"error", err)
			transcoding.SetDisabledReason(err.Error())
		} else {
			transcodeMgr = mgr
			slog.Info("Transcoding enabled", "workers", cfg.Transcoding.MaxWorkers)
		}
	}
	deps.transcodeMgr = transcodeMgr

	// Step 5.6: Vision push coordinator — moved below the camera manager
	// construction (the sub-layer analysis tier needs it as its source
	// provider, #514).

	// Load display timezone for merge window alignment and camera scheduling.
	appLoc := time.Local // Default: use server's local timezone
	if cfg.Timezone != "" && cfg.Timezone != "Local" {
		if loc, err := time.LoadLocation(cfg.Timezone); err == nil {
			appLoc = loc
			slog.Info("using configured timezone", "timezone", cfg.Timezone)
		} else {
			slog.Warn("invalid timezone, falling back to server local time", "timezone", cfg.Timezone, "error", err)
			appLoc = time.Local
		}
	} else if cfg.Timezone == "Local" {
		slog.Info("using server local timezone")
	}
	deps.appLoc = appLoc

	// Step 5.6: Timelapse rolling merge manager (shared between camera manager and API)
	mergeMerger := timelapse.NewAutoDetectMerger()
	deps.rollingMergeMgr = timelapse.NewRollingMergeManager(mergeMerger, db, 10, false)

	camMgr := camera.NewCameraManager(cfg, store, db, configPath, m, deps.mergeMgr, transcodeMgr, deps.rollingMergeMgr, appLoc, deps.eventBus)
	deps.camMgr = camMgr

	// Step 5.6b: Vision push coordinator (NVR → MiBeeVision active push).
	// Subscribes to segment.completed; pushes segment info to Vision when healthy.
	// Only active when [vision].enabled = true AND Vision sends heartbeats.
	// Constructed AFTER the camera manager — the sub-layer analysis tier
	// (#514) acquires its on-demand sub-stream sources through it.
	if cfg.Vision.Enabled {
		deps.visionMgr = vision.NewCoordinator(
			func() config.VisionConfig { return cfg.Vision },
			func() string { return cfg.Storage.RootDir },
			deps.eventBus,
			db,
			camMgr,
		)
		// 多实例路由:相机的 vision_targets(空 = 全部启用实例)。
		deps.visionMgr.SetCameraTargets(func(cameraID string) []string {
			if cam := camMgr.GetCameraConfig(cameraID); cam != nil {
				return cam.VisionTargets
			}
			return nil
		})
		slog.Info("Vision push integration enabled",
			"url", cfg.Vision.URL,
			"instances", len(cfg.Vision.EffectiveInstances()),
			"push_mode", cfg.Vision.PushMode,
			"sub_layer_cameras", len(cfg.Vision.SubLayerCameras))
	}

	// Step 6.5: Health manager (after camera manager, before streaming)
	healthMgr := health.NewManager(cfg.Health, db)
	if healthMgr != nil {
		camMgr.SetHealthManager(healthMgr)
		// Inject metrics into the stream stats collector so that
		// nvr_stream_fps / nvr_stream_bitrate_kbps / nvr_stream_idr_interval_seconds
		// gauges are actually written.
		healthMgr.SetMetrics(m)
	}
	// Wire auto-remediation into health manager
	if healthMgr != nil && camMgr != nil {
		healthMgr.SetRestarter(camMgr.RestartRecorder)
		healthMgr.SetCameraEnabledFn(func(cameraID string) bool {
			return camMgr.GetCameraConfig(cameraID) != nil
		})
		// Wire IP self-healing: when a camera is blacklisted after persistent
		// reconnection failure, attempt to relocate it by its ONVIF serial number
		// (cameras that roam across per-subnet-DHCP APs get new IPs). The manager
		// decides per-camera whether rediscovery applies (ONVIF + has stable_id).
		if cfg.Health.Rediscovery.RediscoveryEnabled() {
			healthMgr.SetRediscoverer(func(ctx context.Context, cameraID string) (bool, error) {
				return camMgr.RediscoverAndReconnect(ctx, cameraID)
			})
		}
	}
	deps.healthMgr = healthMgr

	periodicMergeDir := filepath.Join(cfg.Storage.RootDir, "periodic-merge")
	mergeScheduler := timelapse.NewMergeScheduler(appLoc)
	deps.mergeScheduler = mergeScheduler
	// Pre-create per-camera merge managers and register them in the scheduler
	periodicMergeManagers := make(map[string]*timelapse.PeriodicMergeManager)
	for _, cam := range cfg.Cameras {
		if cam.Timelapse != nil {
			dur := 24 * time.Hour
			if cam.Timelapse.MergeDuration != "" {
				if parsed, err := config.ParseMergeDuration(cam.Timelapse.MergeDuration); err == nil {
					dur = parsed
				} else {
					slog.Warn(
						"merge scheduler: invalid merge duration, defaulting to 24h",
						"camera_id", cam.ID,
						"merge_duration", cam.Timelapse.MergeDuration,
						"error", err,
					)
				}
			}
			// Use per-camera MergeOutputFPS (default 30 via ApplyDefaults), fallback to 10.
			fps := 10
			if cam.Timelapse.MergeOutputFPS > 0 {
				fps = cam.Timelapse.MergeOutputFPS
			}
			// Frame-sampling interval for recording→timelapse extraction
			// (timelapse.interval, default 30s via ApplyDefaults). This is the
			// timelapse compression knob, independent of the output fps above.
			extractInterval := 30 * time.Second
			if d, err := time.ParseDuration(cam.Timelapse.Interval); err == nil && d > 0 {
				extractInterval = d
			}
			periodicMergeManagers[cam.ID] = timelapse.NewPeriodicMergeManager(
				db, db, timelapse.NewGoMerger(), fps, periodicMergeDir, dur, appLoc,
				timelapse.WithRecordingEnabledProvider(func(cameraID string) bool {
					cam := camMgr.GetCameraConfig(cameraID)
					if cam == nil {
						return true
					}
					return cfg.RecordingGate(cam.RecordingEnabled)
				}),
				// Persist periodic-merge outputs to the timelapse_merges table so
				// the frontend can discover / play / delete long-window videos.
				timelapse.WithMergeStore(db),
				// Preserve the user-facing label so DB rows record "natural-day"
				// rather than "24h0m0s".
				timelapse.WithDurationLabel(cam.Timelapse.MergeDuration),
				// Prune per-segment rolling-merge .mp4 outputs after the periodic
				// merge folds them in, unless the camera opts to retain them.
				timelapse.WithRetainIntermediateMP4(cam.Timelapse.RetainIntermediateMP4Value()),
				timelapse.WithIntermediateMP4Pruner(db),
				timelapse.WithExtractionInterval(extractInterval),
				timelapse.WithDeleteRecordingsAfterMerge(cam.Timelapse.DeleteRecordingsAfterMerge),
				// Temp-dir sweep grace (storage.periodic_temp_grace_s, #797
				// review): one global value for every manager — the temp base
				// is shared, per-camera granularity would let the strictest
				// camera silently win.
				timelapse.WithTempDirGrace(time.Duration(cfg.Storage.PeriodicTempGraceS)*time.Second),
			)
			mergeScheduler.AddOrUpdate(cam.ID, dur)
			slog.Info(
				"merge scheduler: configured camera",
				"camera_id", cam.ID,
				"duration", dur.String(),
			)
		}
	}
	deps.periodicMergeManagers = periodicMergeManagers
	mergeScheduler.SetRunFunc(func(ctx context.Context, cameraID string, refTime time.Time) error {
		manager, ok := periodicMergeManagers[cameraID]
		if !ok {
			return fmt.Errorf("merge scheduler: no manager for camera %s", cameraID)
		}
		return manager.Run(ctx, cameraID, refTime)
	})
}
