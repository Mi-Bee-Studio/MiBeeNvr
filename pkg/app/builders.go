package app

// builders.go contains buildAppDeps — the construction phase of RunFree.
// It builds every manager/handler/router in the same order RunFree did
// historically and returns them in an appDeps struct. registerServices (in
// register.go) then registers them as App services in the start/stop order.
//
// The split is purely structural (#138): no logic reordering, no behavioral
// change. Construction order here matches the original "Step N" comments.

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/ai"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/api"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/camera"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/cleanup"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/flv"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/ftp"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/gb28181"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/gb28181/cascade"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/gb28181/sip"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/health"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/hls"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/merge"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	authmw "github.com/Mi-Bee-Studio/MiBeeNvr/internal/middleware"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/middleware/remotelog"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/migration"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/motion"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/mqtt"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/relay"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/rtmp"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/srt"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/timelapse"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/transcoding"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/upload"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/vision"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/webdav"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/webrtc"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/whip"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/wsstream"
)

// buildAppDeps constructs every service dependency and returns it in an
// appDeps struct. The returned cleanup func is the error-path teardown
// (cancel startup-bg goroutines + close the DB) the caller must invoke if it
// bails out before App.Start — mirroring RunFree's historical `return nil, err`
// cleanup at each construction step.
func buildAppDeps(cfg *config.Config, configPath string) (*appDeps, func(), error) {
	deps := &appDeps{cfg: cfg, configPath: configPath}
	ctx := context.Background()

	// Step -1: Stable device identity (#330) — generate + persist the
	// device_id on first startup so LAN clients can anchor on an ID instead
	// of an IP. Best-effort: a read-only config keeps the in-memory ID.
	if err := config.EnsureDeviceIdentity(configPath, cfg); err != nil {
		slog.Warn("failed to persist device identity", "path", configPath, "error", err)
	}

	// Step 0: Ensure the recording root exists. The DB no longer lives here
	// (it stays on the data volume, see dbpath.go), so an unusable root only
	// degrades recording — the app still boots. If the root cannot even be
	// created, fall back to the data volume so the NVR keeps recording
	// SOMEWHERE instead of refusing to start (the "auto-fix" the entrypoint
	// promises). The revert is persisted so the next boot is clean.
	if err := os.MkdirAll(cfg.Storage.RootDir, 0o755); err != nil {
		if dd := dataDir(); dd != "" && dd != cfg.Storage.RootDir {
			slog.Error("recording root unusable — falling back to the data volume",
				"configured_root", cfg.Storage.RootDir, "data_dir", dd, "error", err)
			cfg.Storage.RootDir = dd
			if err := config.Save(configPath, cfg); err != nil {
				slog.Warn("failed to persist root fallback", "error", err)
			}
		} else {
			return nil, nil, fmt.Errorf("create storage dir %s: %w", cfg.Storage.RootDir, err)
		}
	}

	// Step 1: Open database (decoupled from the recording root; adopts a
	// legacy root-bound DB once, see dbpath.go).
	db, err := openDatabase(cfg, configPath)
	if err != nil {
		return nil, nil, err
	}
	deps.db = db

	// Step 2: Metrics
	m := metrics.NewMetrics()
	deps.metrics = m

	// Wire DB observability hooks: query-latency histogram + SQLITE_BUSY counter.
	db.SetMetrics(m)
	storage.SetBusyErrorHook(m.IncSQLiteBusyErrors)

	// Step 2.1: Event bus
	deps.eventBus = event.NewEventBus(64)

	// Step 2.2: Motion-score analyzer (issue #435) — subscribes to
	// SegmentCompleted and scores finished H.264/H.265 segments in the
	// compressed domain (per-frame sizes only, no decode). Constructed early
	// so registerServices can start it before the first segments complete.
	deps.motionAnalyzer = motion.NewAnalyzer(db, deps.eventBus, cfg.Storage.RootDir, motion.DefaultOptions())

	// Step 2.5: Remote log handler (if enabled)
	if cfg.RemoteLog.Enabled {
		var logLevel slog.Level
		switch cfg.Observability.LogLevel {
		case "debug":
			logLevel = slog.LevelDebug
		case "warn":
			logLevel = slog.LevelWarn
		case "error":
			logLevel = slog.LevelError
		default:
			logLevel = slog.LevelInfo
		}
		rh := remotelog.New(cfg.RemoteLog.Endpoint, cfg.RemoteLog.Format, logLevel, m)
		deps.remoteLogH = rh
		// Wrap slog.Default() with multi-handler to fan out to both stdout and remote
		if current := slog.Default(); current.Handler() != nil {
			slog.SetDefault(slog.New(remotelog.MultiHandler(current.Handler(), rh)))
		} else {
			slog.SetDefault(slog.New(rh))
		}
	}

	// Step 3: Storage manager
	store, err := storage.NewManager(cfg.Storage.RootDir, m)
	if err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("storage: %w", err)
	}
	deps.store = store

	// Step 3.5: Background storage migrator (idle-time, rate-limited; the
	// runtime config getters keep rate/window live without restart).
	// Seed per-camera storage overrides from the persisted config — the
	// manager map is runtime state, the yaml is the source of truth.
	for camID, camRoot := range cfg.Storage.CameraRoots {
		store.SetCameraRoot(camID, camRoot)
	}
	deps.migrationMgr = migration.New(db, store,
		func() int { return cfg.Storage.MigrationRateMB * 1024 * 1024 },
		func() string { return cfg.Storage.MigrationWindow })

	// Cleanup temp files from previous crash. Run in background — on large
	// storage trees (100k+ files) the walk can take 20+ seconds, and leftover
	// .tmp files are harmless to delay (each new segment uses a unique uuid).
	// CleanupIncomplete below is a single SQL DELETE (ms-scale) and stays sync.
	//
	// These two goroutines are tracked by startupBgWG + observe startupBgCtx so
	// that the startup-bg service's Stop (registered below) can cancel them and
	// join before App returns. Previously they used context.Background() with
	// no tracking, leaking past App.Stop / t.TempDir cleanup — root cause of
	// the #143 TempDir flake for TestRunFree_DoesNotBlockOnStorageScan.
	startupBgCtx, startupBgCancel := context.WithCancel(ctx)
	var startupBgWG sync.WaitGroup
	deps.startupBgCancel = startupBgCancel
	deps.startupBgWG = &startupBgWG
	startupBgWG.Add(1)
	go func() {
		defer startupBgWG.Done()
		start := time.Now()
		if err := store.CleanupTempFiles(); err != nil {
			if startupBgCtx.Err() == nil {
				slog.Warn("background temp cleanup", "error", err)
			}
			return
		}
		slog.Info("background temp cleanup done", "duration", time.Since(start))
	}()
	if err := db.CleanupIncomplete(ctx); err != nil {
		slog.Warn("incomplete cleanup", "error", err)
	}

	// Reconcile orphaned recording files (exists on disk but not in DB). Run in
	// background — on USB HDD with 100k+ legacy flat-layout files this scan
	// takes 3+ minutes (measured), blocking service availability the whole time.
	// New recordings write their DB row on CloseSegment regardless, so delaying
	// reconciliation of historical orphans has no runtime impact.
	cameraIDs := make(map[string]bool)
	for _, cam := range cfg.Cameras {
		cameraIDs[cam.ID] = true
	}
	startupBgWG.Add(1)
	go func() {
		defer startupBgWG.Done()
		start := time.Now()
		reconciled, err := store.ReconcileOrphanedFiles(startupBgCtx, db, cameraIDs)
		if err != nil {
			if startupBgCtx.Err() == nil {
				slog.Error("background orphan reconciliation failed", "error", err)
			}
			return
		}
		slog.Info("background orphan reconciliation done",
			"reconciled", reconciled, "duration", time.Since(start))
	}()

	// Step 4: Auth middleware
	authmw.SetAuthMetrics(m)
	authMW, effectiveHash := authmw.NewAuthMiddleware(authmw.AuthProvider{
		GetUsername: func() string { return cfg.Auth.Username },
		GetHash:     func() string { return cfg.Auth.PasswordHash },
	}, cfg.Auth.Password, authmw.AuthRateLimitConfig{
		Enabled:       cfg.Auth.RateLimit.Enabled != nil && *cfg.Auth.RateLimit.Enabled,
		MaxFailures:   cfg.Auth.RateLimit.MaxFailures,
		WindowMinutes: cfg.Auth.RateLimit.WindowMinutes,
	})
	if effectiveHash != "" && cfg.Auth.PasswordHash == "" && cfg.Auth.Password != "" {
		slog.Info("persisting auto-hashed password to config", "component", "main")
		cfg.Auth.PasswordHash = effectiveHash
		cfg.Auth.Password = ""
		if err := config.Save(configPath, cfg); err != nil {
			slog.Error("failed to save config after auto-hash", "error", err)
		}
	}
	deps.authMW = authMW

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
		func() []config.CameraConfig { return cfg.Cameras },
		m,
		deps.eventBus,
	)

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

	// Step 5.6: Vision push coordinator (NVR → MiBeeVision active push).
	// Subscribes to segment.completed; pushes segment info to Vision when healthy.
	// Only active when [vision].enabled = true AND Vision sends heartbeats.
	if cfg.Vision.Enabled {
		deps.visionMgr = vision.NewCoordinator(
			func() config.VisionConfig { return cfg.Vision },
			func() string { return cfg.Storage.RootDir },
			deps.eventBus,
			db,
		)
		slog.Info("Vision push integration enabled",
			"url", cfg.Vision.URL,
			"push_mode", cfg.Vision.PushMode)
	}

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
			periodicMergeManagers[cam.ID] = timelapse.NewPeriodicMergeManager(
				db, db, timelapse.NewGoMerger(), fps, periodicMergeDir, dur, appLoc,
				timelapse.WithRecordingEnabledProvider(func(cameraID string) bool {
					cam := camMgr.GetCameraConfig(cameraID)
					if cam == nil || cam.RecordingEnabled == nil {
						return true // nil = default true (recording enabled)
					}
					return *cam.RecordingEnabled
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
			)
			mergeScheduler.AddOrUpdate(cam.ID, dur)
			slog.Info(
				"merge scheduler: configured camera",
				"camera_id", cam.ID,
				"duration", dur.String(),
			)
		}
	}
	mergeScheduler.SetRunFunc(func(ctx context.Context, cameraID string, refTime time.Time) error {
		manager, ok := periodicMergeManagers[cameraID]
		if !ok {
			return fmt.Errorf("merge scheduler: no manager for camera %s", cameraID)
		}
		return manager.Run(ctx, cameraID, refTime)
	})

	// Step 7: HLS manager
	// HLS is a transient live-stream cache — keep it on the data volume so
	// the recordings volume holds only recordings (and survives root switches).
	hlsDataDir := filepath.Join(cfg.Storage.RootDir, "hls")
	if dd := dataDir(); dd != "" {
		hlsDataDir = filepath.Join(dd, "hls")
	}
	hlsMgr := hls.NewManagerWithOpts(context.Background(), hlsDataDir, cfg.HLS.WriteBufferSize, cfg.HLS.SegmentMaxSizeMB*1024*1024, cfg.HLS.SegmentCount, m)
	// Low-Latency HLS is always enabled — the muxer supports fMP4 LL mode
	// unconditionally. Whether a given browser can play a given codec over
	// LL-HLS is a frontend concern (same browser-probe as HLS/FLV).
	partDur, _ := time.ParseDuration(cfg.HLS.PartMinDuration)
	hlsMgr.SetLowLatency(true, partDur)
	deps.hlsMgr = hlsMgr

	// Step 7.5: WebRTC manager (H.264 only)
	if cfg.Streaming.WebRTC.Enabled != nil && *cfg.Streaming.WebRTC.Enabled {
		idleTimeout, _ := time.ParseDuration(cfg.Streaming.WebRTC.IdleTimeout)
		deps.webrtcMgr = webrtc.NewManager(
			webrtc.WithMaxPeers(cfg.Streaming.WebRTC.MaxViewers),
			webrtc.WithIdleTimeout(idleTimeout),
			webrtc.WithMetrics(m),
			webrtc.WithICEServers(webrtcICEServers(cfg.Streaming.WebRTC.ICEServers)),
		)
		slog.Info(
			"WebRTC manager initialized",
			"max_viewers", cfg.Streaming.WebRTC.MaxViewers,
			"ice_servers", len(cfg.Streaming.WebRTC.ICEServers),
		)
	}

	// Step 7.6: FLV manager (constructed for the stream registry; not registered as a service)
	var flvMgr *flv.Manager
	if cfg.Streaming.FLV.Enabled != nil && *cfg.Streaming.FLV.Enabled {
		flvMgr = flv.NewManager(
			flv.WithMaxViewers(cfg.Streaming.FLV.MaxViewers),
			flv.WithMetrics(m),
		)
		slog.Info("FLV manager initialized", "max_viewers", cfg.Streaming.FLV.MaxViewers)
	}
	// NOTE: flvMgr is intentionally NOT stored in appDeps — it is only consumed
	// by the stream registry below and never registered as a service. Keeping
	// it local preserves the original behavior (FLV has no Stop lifecycle).

	// Step 7.7: WebSocket stream manager (always available)
	wsMgr := wsstream.NewManager(
		wsstream.WithMaxViewers(cfg.WebSocket.MaxViewers),
		wsstream.WithWriteBufSize(cfg.WebSocket.WriteBufSize),
		wsstream.WithIdleTimeout(cfg.WebSocket.IdleTimeout),
	)
	slog.Info("WebSocket stream manager initialized", "max_viewers", cfg.WebSocket.MaxViewers, "write_buf_size", cfg.WebSocket.WriteBufSize, "idle_timeout", cfg.WebSocket.IdleTimeout)
	deps.wsMgr = wsMgr

	// Step 7.6b: Relay (push-out) manager. Always constructed when cameras may
	// have push_targets — it's nil-safe and only runs goroutines for cameras
	// that actually have enabled targets. Wired to the camera manager so Add/
	// Update/Remove reconcile targets automatically.
	relayMgr := relay.NewManager(camMgr.GetHub, camMgr.GetSPS)
	camMgr.SetRelayManager(relayMgr)

	// Wire transcoding dependencies for relay targets (H.265→H.264 transcode).
	// These must be set before Start so targets can resolve presets and hardware caps.
	relayFFmpegPath := cfg.Transcoding.FFmpegPath
	relayHwCap := transcoding.ProbeHardwareCapabilities(relayFFmpegPath)
	slog.Info(
		"relay: hardware capabilities",
		"arch", relayHwCap.Arch,
		"h264_encoder", relayHwCap.H264EncoderType,
		"ffmpeg_available", relayHwCap.FFmpegAvailable,
	)

	// Warn about software-only encoding on ARM (H.265→H.264 transcode will be slow).
	if (relayHwCap.Arch == "arm" || relayHwCap.Arch == "arm64") &&
		relayHwCap.H264EncoderType == transcoding.EncoderSoftware {
		slog.Warn("relay: ARM architecture with software-only H.264 encoder — H.265→H.264 transcode will be very slow",
			"arch", relayHwCap.Arch)
	}
	relayMgr.SetFFmpegPath(relayFFmpegPath)
	relayMgr.SetHardwareCap(relayHwCap)
	// Wire the source-URL resolver used by FFmpeg relay mode. Without this,
	// connectViaFFmpeg() sees an empty provider, cannot resolve the camera's
	// RTSP URL, and returns errPermanent on every retry (the 'permanent relay
	// error (no retry)' log spam). Native (gortmplib) relay is unaffected — it
	// subscribes to the StreamHub directly and never needs the source URL.
	relayMgr.SetStreamURLProvider(camMgr.GetStreamURL)

	// Load optional relay preset overrides from deploy/relay-presets.yaml.
	// Falls back to built-in defaults on any error (missing file, invalid YAML).
	relayPresets := relay.NewPresetRegistry()
	if err := relayPresets.Load("deploy/relay-presets.yaml"); err != nil {
		slog.Warn("relay: cannot load preset overrides, using built-in defaults", "error", err)
	}
	relayMgr.SetPresetRegistry(relayPresets)
	deps.relayMgr = relayMgr

	// Step 7.7: RTMP server (optional)
	if cfg.RTMP.Enabled != nil && *cfg.RTMP.Enabled {
		deps.rtmpServer = rtmp.NewServer(
			rtmp.Config{Addr: fmt.Sprintf(":%d", cfg.RTMP.Port)},
			// StreamKeyResolver: LIVE lookup (reflects cameras added at runtime,
			// not just those present at startup).
			camMgr.ResolveStreamKey,
			// CameraHubProvider: hand the publisher the SAME hub the recorder owns.
			camMgr.GetOrCreateHub,
			// OnPublisherConnect: mark the IngestRecorder as streaming.
			func(cameraID string, _ *model.StreamHub) {
				if ir := camMgr.GetIngestRecorder(cameraID); ir != nil {
					ir.WriteConnected()
				}
			},
			// OnPublisherDisconnect: close in-flight segment, return to Idle.
			func(cameraID string) {
				if ir := camMgr.GetIngestRecorder(cameraID); ir != nil {
					ir.OnDisconnect()
				}
			},
		)
		// NALUProvider: forward each access unit to the IngestRecorder for MP4 recording.
		deps.rtmpServer.NALUProvider = func(cameraID string) rtmp.NALUCallback {
			ir := camMgr.GetIngestRecorder(cameraID)
			if ir == nil {
				return nil
			}
			return func(au [][]byte, ptsTicks int64, isIDR bool) {
				ir.WriteNALU(au, ptsTicks, isIDR)
			}
		}
		slog.Info("RTMP server configured", "port", cfg.RTMP.Port)
	}

	// Step 7.7b: WHIP push-in ingest over the main HTTP listener (#369).
	// Mirrors the RTMP wiring: stream key → camera, same IngestRecorder
	// lifecycle hooks. Opus audio reaches the recorder via SetAudioFormat +
	// WriteAudio (SRT/RTMP have no audio path today).
	if cfg.WHIP.Enabled != nil && *cfg.WHIP.Enabled {
		deps.whipServer = whip.NewServer(
			camMgr.ResolveWHIPKey,
			camMgr.GetOrCreateHub,
			func(cameraID string, _ *model.StreamHub) {
				if ir := camMgr.GetIngestRecorder(cameraID); ir != nil {
					ir.WriteConnected()
				}
			},
			func(cameraID string) {
				if ir := camMgr.GetIngestRecorder(cameraID); ir != nil {
					ir.OnDisconnect()
				}
			},
			nil,
		)
		deps.whipServer.NALUProvider = func(cameraID string) whip.NALUCallback {
			ir := camMgr.GetIngestRecorder(cameraID)
			if ir == nil {
				return nil
			}
			return func(au [][]byte, ptsTicks int64, isIDR bool) {
				ir.WriteNALU(au, ptsTicks, isIDR)
			}
		}
		deps.whipServer.AudioFormatter = func(cameraID string, codec string, sampleRate, channels int) {
			if ir := camMgr.GetIngestRecorder(cameraID); ir != nil {
				ir.SetAudioFormat(codec, sampleRate, channels)
			}
		}
		deps.whipServer.AudioProvider = func(cameraID string) whip.AudioCallback {
			ir := camMgr.GetIngestRecorder(cameraID)
			if ir == nil {
				return nil
			}
			return func(codec string, ptsTicks int64, data []byte, dur time.Duration) {
				ir.WriteAudio(codec, ptsTicks, data, dur)
			}
		}
		slog.Info("WHIP ingest endpoint enabled", "path", "/whip/{streamKey}")
	}

	// Step 7.8: SRT listener (optional)
	if cfg.SRT.Enabled != nil && *cfg.SRT.Enabled {
		// Merge per-camera SRT push params into cfg.SRT.Streams so the listener's
		// passphrase/streamid lookup covers both configuration styles.
		for _, sc := range camMgr.SRTStreamConfigs() {
			found := false
			for i := range cfg.SRT.Streams {
				if cfg.SRT.Streams[i].CameraID == sc.CameraID {
					if sc.Passphrase != "" {
						cfg.SRT.Streams[i].Passphrase = sc.Passphrase
					}
					if sc.StreamID != "" {
						cfg.SRT.Streams[i].StreamID = sc.StreamID
					}
					found = true
					break
				}
			}
			if !found {
				cfg.SRT.Streams = append(cfg.SRT.Streams, sc)
			}
		}
		deps.srtListener = srt.NewListener(cfg.SRT)
		deps.srtListener.HubProvider = camMgr.GetOrCreateHub
		deps.srtListener.OnConnect = func(cameraID string, _ *model.StreamHub) {
			if ir := camMgr.GetIngestRecorder(cameraID); ir != nil {
				ir.WriteConnected()
			}
		}
		deps.srtListener.OnDisconnect = func(cameraID string) {
			if ir := camMgr.GetIngestRecorder(cameraID); ir != nil {
				ir.OnDisconnect()
			}
		}
		deps.srtListener.NALUProvider = func(cameraID string) func(au [][]byte, ptsTicks int64, isIDR bool) {
			ir := camMgr.GetIngestRecorder(cameraID)
			if ir == nil {
				return nil
			}
			return func(au [][]byte, ptsTicks int64, isIDR bool) {
				ir.WriteNALU(au, ptsTicks, isIDR)
			}
		}
		slog.Info("SRT listener configured", "port", cfg.SRT.Port)
	}
	// Step 7.9: GB28181 SIP platform server (optional). Constructed after the
	// ingest listeners (SRT) and before cleanup; registered as the "gb28181"
	// service between srt and ws. The DeviceManager heartbeat checker is owned
	// by the SIP server's service lifecycle.
	if cfg.GB28181.Enabled {
		heartbeatInterval, err := time.ParseDuration(cfg.GB28181.HeartbeatInterval)
		if err != nil {
			heartbeatInterval = gb28181.DefaultHeartbeatInterval
		}
		deps.gb28181DevMgr = gb28181.NewDeviceManager(heartbeatInterval)
		deps.gb28181DevMgr.SetOfflineCallback(func(id string) {
			if err := db.MarkDeviceOffline(context.Background(), id); err != nil {
				slog.Warn("gb28181: failed to mark device offline in DB", "device", id, "error", err)
			}
			// Tear down the device's media sessions and flip its cameras'
			// recorders to Reconnecting (they recover on the next re-REGISTER).
			deps.gb28181Server.OnDeviceOffline(id)
		})
		deps.gb28181SessionMgr = newGB28181SessionManager(cfg.GB28181)
		deps.gb28181Server = sip.NewServer(cfg.GB28181, deps.gb28181DevMgr, deps.gb28181SessionMgr, deps.db)
		// Alarm notifications surface on the event bus (SSE /api/events).
		deps.gb28181Server.SetEventBus(deps.eventBus)
		slog.Info("GB28181 SIP server configured", "sip_listen", cfg.GB28181.SIPListen)
	}

	// Step 7.95: GB28181 cascade client (optional, #364). The NVR registers
	// to the configured upper platform as a lower-level device; cameras are
	// aggregated into its catalog and forwarded (PS mux) on INVITE.
	if cfg.GB28181Cascade.Enabled {
		deps.gb28181Cascade = cascade.New(cfg.GB28181Cascade, camera.NewCascadeSource(camMgr), db)
		slog.Info("GB28181 cascade client configured",
			"upper", cfg.GB28181Cascade.ServerAddr, "device", cfg.GB28181Cascade.LocalDeviceID)
	}

	// Step 8: Cleanup manager
	cleanupMgr, err := cleanup.NewCleanupManager(db, store, cfg.Cleanup, m)
	if err != nil {
		startupBgCancel()
		db.Close()
		return nil, nil, fmt.Errorf("cleanup: %w", err)
	}
	cleanupMgr.SetEventBus(deps.eventBus)
	// Wire the live yaml camera set so directory-scanning cleanup (orphan /
	// stale-record) skips dirs belonging to cameras that were removed from the
	// config but whose rows/files linger — avoids recurring O(N) stat
	// scans over orphan dirs on slow USB HDD storage. Retention/disk-threshold
	// cleanup intentionally stays DB-driven so recordings of removed cameras
	// still age out via SQL. Mirrors the provider pattern used by the merge
	// coordinators above.
	cleanupMgr.SetActiveCameraProvider(func() []config.CameraConfig { return cfg.Cameras })
	if cfg.Health.Enabled {
		healthRetention, err := time.ParseDuration(cfg.Health.EventsRetention)
		if err != nil {
			slog.Warn("invalid health events_retention, disabling health cleanup", "error", err)
		} else {
			cleanupMgr.SetHealthConfig(true, healthRetention)
		}
	}

	// Wire transcode orphan cleanup into periodic cleanup
	if transcodeMgr != nil {
		dataDir := cfg.Storage.RootDir
		cleanupMgr.SetTranscodeOrphanCleanup(func(ctx context.Context) error {
			return transcoding.CleanOrphanedTranscodes(ctx, dataDir, db)
		})
	}
	// Wire transcode history retention cleanup
	if cfg.Transcoding.HistoryRetention != "" {
		if hr, err := time.ParseDuration(cfg.Transcoding.HistoryRetention); err == nil {
			cleanupMgr.SetTranscodeHistoryRetention(hr)
		}
	}
	deps.cleanupMgr = cleanupMgr
	deps.archiveDeleter = cleanup.NewArchiveDeleter(db, store)

	// Step 9: Optional MQTT client
	if cfg.MQTT.Enabled {
		deps.mqttClient = mqtt.NewClient(cfg.MQTT.Broker, cfg.MQTT.ClientID, cfg.MQTT.Topic, cfg.MQTT.Username, cfg.MQTT.Password, nil)
	}

	// Wire MQTT client into health manager for event publishing
	if healthMgr != nil && deps.mqttClient != nil {
		healthMgr.SetMQTTClient(deps.mqttClient)
	}

	// Step 10: Optional FTP server
	if cfg.FTP.Enabled != nil && *cfg.FTP.Enabled {
		ftpAddr := fmt.Sprintf(":%d", cfg.FTP.Port)
		deps.ftpServer = ftp.NewServer(ftpAddr, cfg.FTP.PassivePortRange, cfg.Auth.Username, cfg.Auth.Password, store, db)
	}

	// ---- Build HTTP router ----
	cloudProxy := api.NewLocalXiaomiAuth(cfg)
	handler := api.NewHandler(db, store, authMW, cfg, camMgr, hlsMgr, configPath, deps.mergeMgr, cloudProxy, mergeScheduler, deps.gb28181DevMgr, deps.gb28181SessionMgr)
	handler.SetStorageMigrator(deps.migrationMgr)

	// Live API key store: seeded from config, updated in place by the
	// generate/revoke handlers so key changes apply without restart (#335).
	apiKeyStore := authmw.NewAPIKeyStore()
	apiKeyStore.SetKeys(validAPIKeysFromConfig(cfg))
	handler.SetAPIKeyStore(apiKeyStore)

	if deps.whipServer != nil {
		handler.SetWHIPServer(deps.whipServer)
	}
	// Wire streaming managers
	handler.SetWebRTCManager(deps.webrtcMgr)
	handler.SetFLVManager(flvMgr)
	handler.SetWSManager(wsMgr)
	handler.SetHealthManager(healthMgr)
	handler.SetStabilityProvider(healthMgr)
	handler.SetEventBus(deps.eventBus)
	api.SetAPIMetrics(m)
	if deps.rollingMergeMgr != nil {
		handler.SetTimelapseMergeMgr(deps.rollingMergeMgr)
	}
	handler.SetRollingMergeMgr(deps.recordRollingMergeMgr)
	if deps.visionMgr != nil {
		handler.SetVisionCoordinator(deps.visionMgr)
	}
	// Wire AI handler (config + zones only, no backend inference)
	aiMgr := ai.NewManager(aiConfigFromConfig(cfg.AI), deps.eventBus)
	ah := api.NewAIHandler(aiMgr, cfg, configPath)
	handler.SetAIHandler(ah)
	handler.SetRelayManager(relayMgr)
	// Wire GB28181 PTZ controller (sends DeviceControl via the SIP server) when
	// the GB28181 platform server is enabled.
	var gbPTZController *gb28181.PTZController
	if deps.gb28181Server != nil {
		gbPTZController = gb28181.NewPTZController(deps.gb28181DevMgr, deps.gb28181Server)
		handler.SetGB28181PTZ(gbPTZController)
		handler.SetGB28181Catalog(gb28181.NewCatalogController(deps.gb28181DevMgr, deps.gb28181Server))
		handler.SetGB28181Inviter(deps.gb28181Server)
		handler.SetGB28181ByeSender(deps.gb28181Server)
		handler.SetGB28181DeviceMedia(deps.gb28181Server)
		handler.SetGB28181Timezone(appLoc)
		// Auto-create cameras when GB28181 devices register, matching ONVIF auto-add.
		deps.gb28181Server.SetCameraEnroller(camMgr)
		// Auto-INVITE when GB28181 recorders start (pull media on camera creation).
		camMgr.SetGB28181Inviter(deps.gb28181Server)
		camMgr.SetGB28181SessionEnder(deps.gb28181Server)
		// Naive GB device-clock timestamps follow the app timezone — hosts in
		// a different zone than the devices (UTC container, CST cameras) would
		// otherwise skew every record window by the TZ offset.
		deps.gb28181Server.SetGBTimezone(appLoc)
	}
	// Wire the cascade client: registration status surfaced in Settings, and
	// upper-platform PTZ DeviceControl commands bridged to the local camera's
	// native PTZ (ONVIF ContinuousMove / Xiaomi motor / local GB channel).
	if deps.gb28181Cascade != nil {
		handler.SetGB28181Cascade(deps.gb28181Cascade)
		deps.gb28181Cascade.SetGBTimezone(appLoc)
		var gbSend func(channelID, direction string, speed byte) error
		if gbPTZController != nil {
			gbSend = gbPTZController.SendPTZ
		}
		deps.gb28181Cascade.SetPTZForwarder(func(cameraID, direction string, speed byte) error {
			return camera.ForwardPTZ(context.Background(), camMgr, gbSend, cameraID, direction, speed)
		})
	}
	// Create and populate StreamRegistry for protocol discovery
	reg := api.NewStreamRegistry()
	reg.Register(&api.HLSStreamHandler{Mgr: hlsMgr})
	// LL-HLS is always available (low-latency fMP4 muxer always enabled).
	reg.Register(&api.LLHLSStreamHandler{
		HLSStreamHandler: api.HLSStreamHandler{Mgr: hlsMgr},
	})
	if deps.webrtcMgr != nil {
		reg.Register(&api.WebRTCStreamHandler{})
	}
	if flvMgr != nil {
		reg.Register(&api.FLVStreamHandler{})
	}
	// WebSocket stream handler is always available
	reg.Register(&api.WSStreamHandler{})
	// MJPEG stream handler for JPEG/MJPEG cameras (proxy on-demand)
	reg.Register(&api.MJPEGStreamHandler{})
	handler.SetStreamRegistry(reg)

	// Wire FFmpeg downloader for transcoding status/download APIs
	if transcodeMgr != nil {
		handler.SetDownloader(transcodeMgr.Downloader())
		handler.SetTranscodeManager(transcodeMgr)
	} else {
		// Always provide a downloader so FFmpeg status APIs work even when transcoding is disabled
		handler.SetDownloader(transcoding.NewDownloader(cfg.Storage.RootDir, nil))
	}

	// WebDAV
	var davHandler http.Handler
	if cfg.WebDAV.Enabled != nil && *cfg.WebDAV.Enabled {
		davSrv := webdav.NewServer(store, cfg.WebDAV.PathPrefix, authMW, db, cfg.WebDAV.ReadWrite)
		davHandler = davSrv.Handler()
	}

	// Upload handler
	uploadHandler := upload.NewHandler(store, db, 100<<20) // 100MB max

	// Register WebDAV methods with chi so it doesn't reject them as 405.
	chi.RegisterMethod("PROPFIND")
	chi.RegisterMethod("MKCOL")
	chi.RegisterMethod("LOCK")
	chi.RegisterMethod("UNLOCK")
	chi.RegisterMethod("COPY")
	chi.RegisterMethod("MOVE")

	// ---- Build HTTP router ----
	r, err := buildRouter(cfg, authMW, handler, m, davHandler, uploadHandler, apiKeyStore)
	if err != nil {
		startupBgCancel()
		return nil, nil, err
	}
	deps.handler = handler
	deps.router = r

	deps.httpServer = &http.Server{
		Addr:              cfg.Server.Listen,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		// WriteTimeout is intentionally not set: SSE endpoints, large file
		// downloads (ServeFile), and video streaming need long-lived connections.
		// Setting it would kill legitimate long responses.
	}

	cleanup := func() {
		startupBgCancel()
		db.Close()
	}
	return deps, cleanup, nil
}

// newGB28181SessionManager builds the media SessionManager from config. The
// port pool is parsed from PortRange ("start-end"); on parse failure the
// default pool 30000-30050 is used (config validation already rejects bad
// ranges, so this is defensive only).
func newGB28181SessionManager(cfg config.GB28181ServerConfig) *gb28181.SessionManager {
	start, end := uint16(30000), uint16(30050)
	if parts := strings.SplitN(cfg.PortRange, "-", 2); len(parts) == 2 {
		if s, err := strconv.Atoi(strings.TrimSpace(parts[0])); err == nil {
			start = uint16(s)
		}
		if e, err := strconv.Atoi(strings.TrimSpace(parts[1])); err == nil {
			end = uint16(e)
		}
	}
	return gb28181.NewSessionManager(gb28181.NewPortManager(start, end), cfg.ServerID)
}

// validAPIKeysFromConfig extracts the non-revoked API keys (token → name)
// from the config, for seeding the live APIKeyStore.
func validAPIKeysFromConfig(cfg *config.Config) map[string]string {
	valid := make(map[string]string)
	for _, k := range cfg.APIKeys {
		if !k.Revoked && k.Key != "" {
			valid[k.Key] = k.Name
		}
	}
	return valid
}
