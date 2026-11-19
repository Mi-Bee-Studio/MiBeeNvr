package app

// register.go contains registerServices — the registration phase of RunFree.
// It registers every constructed manager as an App service in the exact
// start/stop order. Start order = registration order; Stop is the reverse.
// This order is load-bearing and verified by TestRunFree_ServiceOrder.
//
// registerServices calls the domain group functions below in the historical
// sequence; within each group the registration order is unchanged. The split
// is purely structural (#877): the closures are identical to the historical
// body, only reading from deps.* instead of local vars.

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/autodiscover"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/camera"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/health"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/motion"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/pixgate"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/streamhub"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/substream"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/tierrec"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/timelapse"
)

// registerServices registers all services on the App in start/stop order,
// reading constructed managers from deps. Returns an error if any registration
// fails (the caller invokes deps' cleanup func on failure).
func registerServices(a *App, deps *appDeps) error {
	if err := registerCoreServices(a, deps); err != nil {
		return err
	}
	if err := registerMediaServices(a, deps); err != nil {
		return err
	}
	if err := registerMaintenanceServices(a, deps); err != nil {
		return err
	}
	if err := registerProtocolServices(a, deps); err != nil {
		return err
	}
	return registerEgressServices(a, deps)
}

// registerCoreServices — storage-migrator, db, startup-bg, camera, health,
// recording-auditor, autodiscover.
func registerCoreServices(a *App, deps *appDeps) error {
	// 0. storage migrator — background worker, stopped early
	if err := a.Register(&serviceFunc{
		name: "storage-migrator",
		startFunc: func(ctx context.Context) error {
			deps.migrationMgr.Start(ctx)
			return nil
		},
		stopFunc: func() error {
			deps.migrationMgr.Stop()
			return nil
		},
	}); err != nil {
		return err
	}

	// 0b. thermal readout poller — republishes device-side temperature
	// readings on the SSE bus (dual-spectrum live chip). No-op without
	// configured thermal_readout sources.
	if err := a.Register(newThermalReadoutService(deps.cfg, deps.eventBus)); err != nil {
		return err
	}

	// 0c. thermal history recorder — subscribes to the same topic and
	// persists minute-aggregated extremes for the P4 playback curve (#38
	// Tier 2). No-op without a db/bus.
	if err := a.Register(newThermalHistoryService(deps.db, deps.eventBus)); err != nil {
		return err
	}

	// 1. db — registered first so it stops last. Start also launches the
	// idle-window WAL checkpoint loop (#752); it owns a cancel derived from
	// the start ctx because App.Stop does not cancel it — the service's Stop
	// cancels first, then closes the DB (checkpoint+remove -wal happens on
	// last-connection close).
	if err := a.Register(&serviceFunc{
		name: "db",
		startFunc: func(ctx context.Context) error {
			walCtx, walCancel := context.WithCancel(ctx)
			deps.dbStopWAL = walCancel
			deps.db.StartWALMaintenance(walCtx)
			return nil
		},
		stopFunc: func() error {
			if deps.dbStopWAL != nil {
				deps.dbStopWAL()
			}
			deps.db.Close()
			return nil
		},
	}); err != nil {
		return fmt.Errorf("register db: %w", err)
	}

	// 2. startup-bg — join the two background goroutines launched in buildAppDeps
	// (CleanupTempFiles + ReconcileOrphanedFiles). Registered right after db so
	// its Stop runs just before db.Close in the reverse-order teardown: cancel
	// their ctx + wait for them to exit, so they're not still walking/writing
	// the storage tree (t.TempDir) when cleanup proceeds. See #143 / #125.
	if err := a.Register(&serviceFunc{
		name: "startup-bg",
		startFunc: func(_ context.Context) error {
			return nil
		},
		stopFunc: func() error {
			deps.startupBgCancel()
			deps.startupBgWG.Wait()
			return nil
		},
	}); err != nil {
		return fmt.Errorf("register startup-bg: %w", err)
	}

	// 3. camera — relay folded in (relay starts before camera, stops before camera)
	if err := a.Register(&serviceFunc{
		name: "camera",
		startFunc: func(ctx context.Context) error {
			if deps.relayMgr != nil {
				deps.relayMgr.Start(ctx)
			}
			go func() {
				if err := deps.camMgr.Start(ctx); err != nil {
					slog.Error("camera manager", "error", err)
				}
			}()
			return nil
		},
		stopFunc: func() error {
			if deps.relayMgr != nil {
				deps.relayMgr.Stop()
			}
			if err := deps.camMgr.Stop(); err != nil {
				slog.Warn("camera manager stop error", "error", err)
			}
			return nil
		},
	}); err != nil {
		return fmt.Errorf("register camera: %w", err)
	}

	// 3.1 time-sync (#time-sync): camera clock measurement/auto-correction
	// plus the manual API operations. Registered after camera (consumes its
	// config accessors); stops before camera — in-flight corrections finish
	// before the manager tears down.
	if deps.timeSyncSvc != nil {
		if err := a.Register(&serviceFunc{
			name: "time-sync",
			startFunc: func(ctx context.Context) error {
				return deps.timeSyncSvc.Start(ctx)
			},
			stopFunc: func() error {
				return deps.timeSyncSvc.Stop()
			},
		}); err != nil {
			return fmt.Errorf("register time-sync: %w", err)
		}
	}

	// 3.2 credprobe (#credprobe): default-credential testing for newly
	// auto-discovered cameras. Registered BEFORE autodiscover so it stops
	// AFTER it — enroll-time probe offers stay valid while discovery winds
	// down; its context cancellation gates late arrivals.
	if deps.credProbeSvc != nil {
		if err := a.Register(deps.credProbeSvc); err != nil {
			return fmt.Errorf("register credprobe: %w", err)
		}
	}

	// 3. health (always present)
	if err := a.Register(&serviceFunc{
		name: "health",
		startFunc: func(ctx context.Context) error {
			if err := deps.healthMgr.Start(ctx); err != nil {
				slog.Error("health manager", "error", err)
			}
			return nil
		},
		stopFunc: func() error {
			deps.healthMgr.Stop()
			return nil
		},
	}); err != nil {
		return fmt.Errorf("register health: %w", err)
	}

	// 3.1. recording integrity auditor (#469 + #489 deep check): rate-limited
	// mediaprobe sampling of closed segments, plus an hourly-per-camera ffmpeg
	// decode-level deep check when a binary is available (FFmpeg stays
	// OPTIONAL — no binary, no deep check). Nil-safe when bus/metrics are
	// absent; stops before health in reverse order (no ordering dependency —
	// it only reads files).
	auditor := health.NewRecordingAuditor(deps.eventBus, deps.metrics,
		health.WithFFmpegPath(resolveFFmpegPath(deps.cfg.Transcoding.FFmpegPath)))
	if err := a.Register(&serviceFunc{
		name: "recording-auditor",
		startFunc: func(ctx context.Context) error {
			return auditor.Start(ctx)
		},
		stopFunc: func() error {
			return auditor.Stop()
		},
	}); err != nil {
		return fmt.Errorf("register recording-auditor: %w", err)
	}

	// 3.5. auto-discover (optional, default OFF). Continuously discovers ONVIF
	// devices (passive Hello listener + periodic Probe sweep) and auto-enrolls
	// them. Registered after camera+health so camMgr.AddCamera is available, and
	// stops before camera on shutdown (reverse order) so it can't add a camera
	// while the manager is tearing down.
	if deps.cfg.AutoDiscover.AutoDiscoverEnabled() {
		adSvc := autodiscover.New(&deps.cfg.AutoDiscover, deps.camMgr, deps.db, deps.eventBus)
		if deps.credProbeSvc != nil {
			adSvc.SetCredentialProber(deps.credProbeSvc)
		}
		if err := a.Register(adSvc); err != nil {
			return fmt.Errorf("register autodiscover: %w", err)
		}
	}

	return nil
}

// registerMediaServices — merge, rolling-merge, vision-push, motion-score,
// pixgate, tiered-recording, transcode, mergeScheduler.
func registerMediaServices(a *App, deps *appDeps) error {
	// 4. merge — run in background goroutine with its own cancel
	{
		var mergeCancel context.CancelFunc
		var mergeDone chan struct{}
		if err := a.Register(&serviceFunc{
			name: "merge",
			startFunc: func(ctx context.Context) error {
				var runCtx context.Context
				runCtx, mergeCancel = context.WithCancel(ctx)
				mergeDone = make(chan struct{})
				go func() {
					defer close(mergeDone)
					if deps.cfg.Merge.Enabled {
						deps.mergeMgr.Run(runCtx)
						slog.Info("merge-manager stopped")
					}
				}()
				return nil
			},
			stopFunc: func() error {
				if mergeCancel != nil {
					mergeCancel()
				}
				// Join the Run goroutine so it's not still walking/writing the
				// storage tree after App.Stop returns (#143 / #125 class).
				if mergeDone != nil {
					<-mergeDone
				}
				return nil
			},
		}); err != nil {
			return fmt.Errorf("register merge: %w", err)
		}
	}

	// 4.1. rolling-merge — event-driven quasi-real-time merge
	{
		var rollingCancel context.CancelFunc
		if err := a.Register(&serviceFunc{
			name: "rolling-merge",
			startFunc: func(ctx context.Context) error {
				var runCtx context.Context
				runCtx, rollingCancel = context.WithCancel(ctx)
				return deps.recordRollingMergeMgr.Start(runCtx)
			},
			stopFunc: func() error {
				if rollingCancel != nil {
					rollingCancel()
				}
				deps.recordRollingMergeMgr.Stop()
				return nil
			},
		}); err != nil {
			return fmt.Errorf("register rolling-merge: %w", err)
		}
	}

	// 4.2. vision-push — NVR→Vision segment push coordinator (optional)
	if deps.visionMgr != nil {
		var visionCancel context.CancelFunc
		if err := a.Register(&serviceFunc{
			name: "vision-push",
			startFunc: func(ctx context.Context) error {
				var runCtx context.Context
				runCtx, visionCancel = context.WithCancel(ctx)
				return deps.visionMgr.Start(runCtx)
			},
			stopFunc: func() error {
				if visionCancel != nil {
					visionCancel()
				}
				deps.visionMgr.Stop()
				return nil
			},
		}); err != nil {
			return fmt.Errorf("register vision-push: %w", err)
		}
	}

	// 4.3. motion-score — offline compressed-domain activity scoring
	// (issue #435). Subscribes to SegmentCompleted; started before cleanup so
	// scores exist by the time disk-threshold ordering consults them, and
	// stopped (reverse order) before the merge services it observes.
	if deps.motionAnalyzer != nil {
		if err := a.Register(deps.motionAnalyzer); err != nil {
			return fmt.Errorf("register motion-score: %w", err)
		}
	}

	// 4.3b. pixgate — pixel-domain fine gate (#636): classic CV over a ~1fps
	// sampled decode of enabled cameras' sub-streams; confirmed activity
	// fires the recorder pixel trigger (TL exit + GOP flush + hold). ffmpeg
	// is optional — without it the service start is a no-op WARN.
	//
	// The sampler prefers the SHARED sub-stream hub (#643): ffmpeg reads
	// sampled keyframes over stdin from the same refcounted pull the live
	// quality=sub egress uses — no duplicate camera connection, decode
	// bounded to the sample rate instead of the whole stream. Direct RTSP
	// stays the fallback (xiaomi / push ingest / MJPEG subs).
	if deps.camMgr != nil {
		cams := pixgateCameras(deps.cfg)
		if len(cams) > 0 {
			m := pixgate.NewManager(pixgate.Config{
				FFmpegPath: resolveFFmpegPath(deps.cfg.Transcoding.FFmpegPath),
				HubResolver: func(ctx context.Context, cameraID string) (pixgate.HubSource, bool, error) {
					src, err := deps.camMgr.AcquireSubStream(ctx, cameraID)
					if err != nil {
						return nil, false, err
					}
					return &subStreamHubSource{src: src, camMgr: deps.camMgr, cameraID: cameraID}, true, nil
				},
				Resolver: func(ctx context.Context, cameraID string) (pixgate.Target, bool, error) {
					t, ok, err := deps.camMgr.ResolveSubStreamTarget(ctx, cameraID)
					if err != nil || !ok {
						return pixgate.Target{}, ok, err
					}
					return pixgate.Target{URL: t.URL, Username: t.Username, Password: t.Password, Kind: t.Kind}, true, nil
				},
				Trigger: func(cameraID string, hold time.Duration) error {
					rec := deps.camMgr.GetRecorder(cameraID)
					if rec == nil {
						return fmt.Errorf("camera %s has no recorder", cameraID)
					}
					trig, ok := rec.(interface {
						PixelTriggerEvent(at time.Time, hold time.Duration) error
					})
					if !ok {
						return fmt.Errorf("camera %s does not support pixel triggers", cameraID)
					}
					return trig.PixelTriggerEvent(time.Now(), hold)
				},
				Bus:     deps.eventBus,
				Metrics: deps.metrics,
				Cameras: cams,
			})
			deps.pixgateMgr = m
			// Pixel-preferred activity scoring (2026-09-01): the motion
			// analyzer prefers the pixgate FG time series over byte spikes
			// whenever the window is well covered; PTZ commands blind the
			// gate so a moving camera never reads as activity.
			deps.motionAnalyzer.SetPixelSource(pixgateFGSource{m})
			if deps.handler != nil {
				deps.handler.SetPTZSuppressor(func(cameraID string, window time.Duration) {
					m.Suppress(cameraID, window)
				})
			}
			if err := a.Register(m); err != nil {
				return fmt.Errorf("register pixgate: %w", err)
			}
		}
	}

	// 4.3c. tiered-recording — continuous sub-stream channel (#637). One
	// recorder per recording_tier: tiered camera; rows land as layer=1 and
	// stay out of the default list/merge/push filters. Tier changes are read
	// here at boot (restart to apply).
	if deps.camMgr != nil {
		// Compile-time guard: the storage manager must keep satisfying the
		// tierrec temp-protection interface, or in-flight tierrec segments
		// lose their cleanup-scan protection (2026-09-08 incident class).
		var _ tierrec.TempRegistry = (*storage.Manager)(nil)
		var tiered []string
		for _, cam := range deps.cfg.Cameras {
			if cam.RecordingTier == "tiered" {
				tiered = append(tiered, cam.ID)
			}
		}
		if len(tiered) > 0 {
			tm := tierrec.NewManager(tierrec.Config{
				Provider:     deps.camMgr,
				Store:        deps.db,
				Bus:          deps.eventBus,
				StorageRoot:  deps.cfg.Storage.RootDir,
				TempRegistry: deps.store,
				// Per-camera layer-1 rotation window
				// (cameras[].tier_segment_duration); read live at each
				// rotation so the value applies without re-resolving runs.
				SegDurFor: func(cameraID string) time.Duration {
					cam := deps.camMgr.GetCameraConfig(cameraID)
					if cam == nil || cam.TierSegmentDuration == "" {
						return 0
					}
					d, err := time.ParseDuration(cam.TierSegmentDuration)
					if err != nil || d <= 0 {
						return 0
					}
					return d
				},
			})
			tm.SetCameras(tiered)
			if err := a.Register(tm); err != nil {
				return fmt.Errorf("register tiered-recording: %w", err)
			}
		}
	}

	// 5. transcode (optional)
	if deps.transcodeMgr != nil {
		if err := a.Register(&serviceFunc{
			name: "transcode",
			startFunc: func(ctx context.Context) error {
				go deps.transcodeMgr.Run(ctx)
				return nil
			},
			stopFunc: func() error {
				deps.transcodeMgr.Stop()
				return nil
			},
		}); err != nil {
			return fmt.Errorf("register transcode: %w", err)
		}
	}

	// 6. mergeScheduler
	if err := a.Register(&serviceFunc{
		name: "mergeScheduler",
		startFunc: func(ctx context.Context) error {
			deps.mergeScheduler.Start(ctx)
			// Startup catch-up: re-merge windows whose scheduled run was
			// interrupted by a service stop (deploy restart, crash, power
			// loss). Discovery is cheap (one indexed list per camera); the
			// re-runs fold+delete sources exactly like the scheduled path.
			// Runs until the backlog is drained; ctx cancellation (app
			// shutdown) stops it between windows.
			go func() {
				if deps.mergeRunFunc == nil || len(deps.mergeCatchUpSpecs) == 0 {
					return
				}
				timelapse.RunCatchUp(ctx, time.Now(), deps.appLoc, deps.mergeCatchUpSpecs,
					timelapse.CatchUpProbes{
						CompletedWindowStarts: func(ctx context.Context, cameraID, durationLabel string, from, to time.Time) (map[int64]struct{}, error) {
							rows, err := deps.db.ListTimelapseMerges(ctx, storage.TimelapseMergeFilter{
								CameraID:      cameraID,
								DurationLabel: durationLabel,
								Status:        model.TimelapseMergeStatusCompleted,
								StartTime:     from,
								EndTime:       to,
							})
							if err != nil {
								return nil, err
							}
							out := make(map[int64]struct{}, len(rows))
							for _, r := range rows {
								out[r.WindowStart.UnixNano()] = struct{}{}
							}
							return out, nil
						},
						HasSources: func(ctx context.Context, cameraID string, start, end time.Time) (bool, error) {
							recs, err := deps.db.ListRecordings(ctx, model.RecordingFilter{
								CameraID:  cameraID,
								StartTime: start,
								EndTime:   end,
								Limit:     1,
							})
							if err != nil {
								return false, err
							}
							return len(recs) > 0, nil
						},
					},
					deps.mergeRunFunc)
			}()
			return nil
		},
		stopFunc: func() error {
			deps.mergeScheduler.Stop()
			return nil
		},
	}); err != nil {
		return fmt.Errorf("register mergeScheduler: %w", err)
	}

	return nil
}

// registerMaintenanceServices — cleanup, archive-deleter, mqtt, mqtt-status,
// ftp.
func registerMaintenanceServices(a *App, deps *appDeps) error {
	// 7. cleanup — run in background goroutine with its own cancel
	{
		var cleanupCancel context.CancelFunc
		var cleanupDone chan struct{}
		if err := a.Register(&serviceFunc{
			name: "cleanup",
			startFunc: func(ctx context.Context) error {
				var runCtx context.Context
				runCtx, cleanupCancel = context.WithCancel(ctx)
				cleanupDone = make(chan struct{})
				go func() {
					defer close(cleanupDone)
					deps.cleanupMgr.Run(runCtx)
				}()
				return nil
			},
			stopFunc: func() error {
				if cleanupCancel != nil {
					cleanupCancel()
				}
				// Join the Run goroutine so it's not still deleting files under
				// the storage tree after App.Stop returns (#143 / #125 class).
				if cleanupDone != nil {
					<-cleanupDone
				}
				return nil
			},
		}); err != nil {
			return fmt.Errorf("register cleanup: %w", err)
		}
	}

	// 7.5. archive-deleter — background cleanup of deleted archived cameras
	if err := a.Register(&serviceFunc{
		name: "archive-deleter",
		startFunc: func(ctx context.Context) error {
			return deps.archiveDeleter.Start(ctx)
		},
		stopFunc: func() error {
			return deps.archiveDeleter.Stop()
		},
	}); err != nil {
		return fmt.Errorf("register archive-deleter: %w", err)
	}

	// 7.6. offload (optional, issue #874) — S3-compatible remote upload of
	// merged recordings. Registered after cleanup so its Stop (reverse
	// order) halts uploads before the DB closes; it never holds resources
	// the streaming services need.
	if deps.offloadMgr != nil {
		if err := a.Register(deps.offloadMgr); err != nil {
			return fmt.Errorf("register offload: %w", err)
		}
	}

	// 8. mqtt (optional)
	if deps.mqttClient != nil {
		if err := a.Register(&serviceFunc{
			name: "mqtt",
			startFunc: func(ctx context.Context) error {
				go func() {
					if err := deps.mqttClient.Start(ctx); err != nil {
						slog.Error("mqtt", "error", err)
					}
				}()
				return nil
			},
			stopFunc: func() error {
				if err := deps.mqttClient.Stop(); err != nil {
					slog.Warn("MQTT stop error", "error", err)
				}
				return nil
			},
		}); err != nil {
			return fmt.Errorf("register mqtt: %w", err)
		}
	}

	// 8.5 mqtt-status (optional): event-bus → MQTT forwarding
	if deps.mqttStatusPub != nil {
		if err := a.Register(&serviceFunc{
			name: "mqtt-status",
			startFunc: func(ctx context.Context) error {
				return deps.mqttStatusPub.Start(ctx)
			},
			stopFunc: func() error {
				return deps.mqttStatusPub.Stop()
			},
		}); err != nil {
			return fmt.Errorf("register mqtt-status: %w", err)
		}
	}

	// 8.6 notifications (optional): outbound alert webhook forwarding
	if deps.notifySvc != nil {
		if err := a.Register(&serviceFunc{
			name: "notifications",
			startFunc: func(ctx context.Context) error {
				return deps.notifySvc.Start(ctx)
			},
			stopFunc: func() error {
				return deps.notifySvc.Stop()
			},
		}); err != nil {
			return fmt.Errorf("register notifications: %w", err)
		}
	}

	// 9. ftp (optional)
	if deps.ftpServer != nil {
		if err := a.Register(&serviceFunc{
			name: "ftp",
			startFunc: func(ctx context.Context) error {
				go func() {
					if err := deps.ftpServer.Start(ctx); err != nil {
						slog.Error("ftp", "error", err)
					}
				}()
				return nil
			},
			stopFunc: func() error {
				deps.ftpServer.Close()
				return nil
			},
		}); err != nil {
			return fmt.Errorf("register ftp: %w", err)
		}
	}

	// 9b. dlna (optional) — SSDP announcements start with the server; bind
	// failures log and disable DLNA, never abort startup (same best-effort
	// posture as the discovery responder).
	if deps.dlnaServer != nil {
		if err := a.Register(&serviceFunc{
			name: "dlna",
			startFunc: func(ctx context.Context) error {
				go func() {
					if err := deps.dlnaServer.Start(ctx); err != nil {
						slog.Error("dlna: media server disabled", "error", err)
					}
				}()
				return nil
			},
			stopFunc: func() error {
				return deps.dlnaServer.Stop()
			},
		}); err != nil {
			return fmt.Errorf("register dlna: %w", err)
		}
	}

	// 9b2. zero-touch camera onboarding (#1120) — optional, default off.
	// Start/Stop wrap the periodic sweep lifecycle; a disabled config leaves
	// deps.onboarding nil and nothing registers.
	if deps.onboarding != nil {
		if err := a.Register(&serviceFunc{
			name: "camera-onboarding",
			startFunc: func(ctx context.Context) error {
				return deps.onboarding.Start(ctx)
			},
			stopFunc: func() error {
				return deps.onboarding.Stop()
			},
		}); err != nil {
			return fmt.Errorf("register camera-onboarding: %w", err)
		}
	}

	// 9c. doorbell (optional) — SIP ingress; bind failures log and disable,
	// never abort startup.
	if deps.doorbellServer != nil {
		if err := a.Register(&serviceFunc{
			name: "doorbell",
			startFunc: func(ctx context.Context) error {
				go func() {
					if err := deps.doorbellServer.Start(ctx); err != nil {
						slog.Error("doorbell: ingress disabled", "error", err)
					}
				}()
				return nil
			},
			stopFunc: func() error {
				deps.doorbellServer.Stop()
				return nil
			},
		}); err != nil {
			return fmt.Errorf("register doorbell: %w", err)
		}
	}

	return nil
}

// registerValues exposes typed handles for in-module extensions via a.Value(...)
// (concrete types — there is no out-of-module consumer since the P2P module
// merged into internal/p2psdk, #repo-governance 4.3 removed the pkg/ mirrors).
func registerValues(a *App, deps *appDeps) error {
	if err := a.RegisterValue("camera-manager", deps.camMgr); err != nil {
		return fmt.Errorf("register camera-manager value: %w", err)
	}
	if err := a.RegisterValue("relay-manager", deps.relayMgr); err != nil {
		return fmt.Errorf("register relay-manager value: %w", err)
	}
	if err := a.RegisterValue("eventbus", deps.eventBus); err != nil {
		return fmt.Errorf("register eventbus value: %w", err)
	}
	if err := a.RegisterValue("config", deps.cfg); err != nil {
		return fmt.Errorf("register config value: %w", err)
	}
	if err := a.RegisterValue("http-router", deps.router); err != nil {
		return fmt.Errorf("register http-router value: %w", err)
	}
	if err := a.RegisterValue("http-server", deps.httpServer); err != nil {
		return fmt.Errorf("register http-server value: %w", err)
	}
	return nil
}

// resolveFFmpegPath returns the configured ffmpeg binary, falls back to PATH,
// or "" (deep check disabled) when ffmpeg is absent — FFmpeg is optional.
func resolveFFmpegPath(configured string) string {
	if configured != "" {
		return configured
	}
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		return p
	}
	return ""
}

// pixgateCameras extracts the resolved pixgate config per enabled camera
// (#636). Hold is parsed here (config layer validated the range).
func pixgateCameras(cfg *config.Config) map[string]pixgate.CameraConfig {
	if cfg == nil {
		return nil
	}
	cams := map[string]pixgate.CameraConfig{}
	for _, cam := range cfg.Cameras {
		p := cam.Pixgate
		if p == nil || !p.Enabled {
			continue
		}
		hold := 30 * time.Second
		if p.Hold != "" {
			if d, err := time.ParseDuration(p.Hold); err == nil {
				hold = d
			}
		}
		var masks []pixgate.Mask
		for _, m := range p.Masks {
			masks = append(masks, pixgate.Mask(m.Points))
		}
		cams[cam.ID] = pixgate.CameraConfig{
			SampleFPS:  p.SampleFPS,
			MinAreaPct: p.MinAreaPct,
			Persist:    p.Persist,
			Hold:       hold,
			GhostSecs:  p.GhostSecs,
			Masks:      masks,
		}
	}
	return cams
}

// pixgateFGSource adapts *pixgate.Manager onto motion.PixelSource.
type pixgateFGSource struct {
	m *pixgate.Manager
}

func (p pixgateFGSource) SegmentFG(cameraID string, start, end time.Time) (motion.FGWindow, bool) {
	s, ok := p.m.SegmentStats(cameraID, start, end)
	if !ok {
		return motion.FGWindow{}, false
	}
	return motion.FGWindow{
		Valid:       s.Valid,
		MeanAreaPct: s.MeanAreaPct,
		DutyCycle:   s.DutyCycle,
		TrigAreaPct: s.TrigAreaPct,
		Coverage:    s.Coverage,
	}, true
}

// subStreamHubSource adapts the refcounted substream.Source onto
// pixgate.HubSource: Release drops the camera-manager reference so the shared
// pull stops after its idle timeout when nobody else holds it (#643).
type subStreamHubSource struct {
	src      *substream.Source
	camMgr   *camera.CameraManager
	cameraID string
}

func (s *subStreamHubSource) Hub() *streamhub.StreamHub { return s.src.Hub() }
func (s *subStreamHubSource) Release()                  { s.camMgr.ReleaseSubStream(s.cameraID) }
