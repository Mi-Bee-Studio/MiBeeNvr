package app

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	_ "net/http/pprof"
	_ "time/tzdata" // embed timezone database for minimal containers (scratch/alpine)

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/ai"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/api"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/camera"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/cleanup"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/flv"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/ftp"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/health"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/hls"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/merge"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	authmw "github.com/Mi-Bee-Studio/MiBeeNvr/internal/middleware"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/middleware/remotelog"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/mqtt"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/relay"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/rtmp"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/srt"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/timelapse"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/transcoding"
	ui "github.com/Mi-Bee-Studio/MiBeeNvr/internal/ui"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/upload"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/webdav"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/webrtc"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/wsstream"

	_ "github.com/Mi-Bee-Studio/MiBeeNvr/internal/xiaomi"
)

// serviceFunc wraps a pair of start/stop functions as a Service.
type serviceFunc struct {
	name      string
	startFunc func(ctx context.Context) error
	stopFunc  func() error
}

func (s *serviceFunc) Name() string                    { return s.name }
func (s *serviceFunc) Start(ctx context.Context) error { return s.startFunc(ctx) }
func (s *serviceFunc) Stop() error                     { return s.stopFunc() }

// aiConfigFromConfig converts the public AIConfig type to the internal ai.Config.
func aiConfigFromConfig(cfg config.AIConfig) ai.Config {
	return ai.Config{
		Enabled:             cfg.Enabled,
		EnabledCameras:      cfg.EnabledCameras,
		ModelURL:            cfg.ModelURL,
		Zones:               cfg.Zones,
		FrameSkipRate:       cfg.FrameSkipRate,
		ConfidenceThreshold: cfg.ConfidenceThreshold,
	}
}

// validateMergedRecordings scans all recordings marked as 'merged' and resets
// any whose merged output file is missing on disk. This prevents the playback
// path from hitting a dead-end 404 when the DB claims a merge succeeded but
// the file was lost (crash, manual deletion, or corrupt merge that reported
// success without producing a valid file).
func validateMergedRecordings(ctx context.Context, db *storage.DB, rootDir string) {
	recordings, err := db.ListMergedRecordingsForValidation(ctx)
	if err != nil {
		slog.Warn("startup: failed to list merged recordings for validation", "error", err)
		return
	}
	if len(recordings) == 0 {
		return
	}

	resetCount := 0
	for _, rec := range recordings {
		if rec.MergePath == "" {
			continue
		}
		if info, err := os.Stat(rec.MergePath); err != nil || info.Size() == 0 {
			slog.Warn("startup: resetting stale merge status (file missing or empty)",
				"recording_id", rec.ID,
				"camera_id", rec.CameraID,
				"merge_path", rec.MergePath,
				"file_path", rec.FilePath)
			if resetErr := db.ResetMergeStatus(ctx, rec.ID); resetErr != nil {
				slog.Warn("startup: failed to reset merge status",
					"recording_id", rec.ID, "error", resetErr)
			} else {
				resetCount++
			}
		}
	}
	if resetCount > 0 {
		slog.Info("startup: reset stale merge statuses",
			"reset_count", resetCount,
			"total_checked", len(recordings))
	}
}

// buildRouter constructs the chi HTTP router with all middleware, routes, mounts,
// and the SPA static file handler. Called by RunFree.
func buildRouter(
	cfg *config.Config,
	authMW func(http.Handler) http.Handler,
	handler *api.Handler,
	metrics *metrics.Metrics,
	davHandler http.Handler,
	uploadHandler *upload.Handler,
) (http.Handler, error) {
	r := chi.NewRouter()
	r.Use(authmw.RequestLogger(slog.Default(), "/api/health", "/api/readyz"))
	r.Use(chimiddleware.Recoverer)
	r.Use(authmw.SecurityHeaders)
	r.Use(authmw.COOPHeaders)

	// API Key middleware — validates Bearer mbv_* tokens for MiBeeVision.
	// Runs before authMW: if the request has an API Key Bearer token, it's
	// authenticated here; otherwise it falls through to BasicAuth.
	if len(cfg.APIKeys) > 0 {
		validKeys := make(map[string]string)
		for _, k := range cfg.APIKeys {
			if !k.Revoked && k.Key != "" {
				validKeys[k.Key] = k.Name
			}
		}
		if len(validKeys) > 0 {
			r.Use(func(next http.Handler) http.Handler {
				return authmw.APIKeyAuthMiddleware(validKeys, next)
			})
			slog.Info("API Key authentication enabled", "keys", len(validKeys))
		}
	}

	// Prometheus metrics — independent auth when configured, public otherwise
	if cfg.MetricsAuth.IsConfigured() {
		metricsAuthMW, _ := authmw.NewAuthMiddleware(authmw.AuthProvider{
			GetUsername: func() string { return cfg.MetricsAuth.Username },
			GetHash:     func() string { return cfg.MetricsAuth.PasswordHash },
		}, cfg.MetricsAuth.Password, authmw.AuthRateLimitConfig{})
		r.With(metricsAuthMW).Handle("/metrics", promhttp.HandlerFor(metrics.Registry, promhttp.HandlerOpts{ErrorHandling: promhttp.ContinueOnError}))
	} else {
		r.Handle("/metrics", promhttp.HandlerFor(metrics.Registry, promhttp.HandlerOpts{ErrorHandling: promhttp.ContinueOnError}))
	}

	r.Mount("/", handler.Routes())

	// WebDAV
	if davHandler != nil {
		r.Mount(cfg.WebDAV.PathPrefix, davHandler)
	}

	// Upload routes (authenticated)
	r.Group(func(r chi.Router) {
		r.Use(authMW)
		uploadHandler.RegisterRoutes(r)
	})

	// Static UI — serve from embedded filesystem
	staticContent, err := fs.Sub(ui.StaticFS, "static")
	if err != nil {
		return nil, fmt.Errorf("static fs: %w", err)
	}
	fileServer := http.FileServer(http.FS(staticContent))
	// Static files served without auth — SPA handles login flow client-side.
	// All sensitive data is protected via API endpoints in handler.Routes().
	// Cache: index.html must not be cached (always fresh after deploy).
	// Assets have content-hash filenames — safe to cache long-term.
	r.NotFound(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/" || path == "/index.html" {
			w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		}
		fileServer.ServeHTTP(w, r)
	}))

	return r, nil
}

// RunFree constructs and returns a configured *App with all open-source services
// registered in the correct start/stop order.
//
// The returned App has Start/Stop lifecycle management built in. Callers can
// Register() additional services (e.g. Pro/P2P extensions) before Start().
//
// Example:
//
//	a, err := app.RunFree(cfg, configPath)
//	if err != nil { return err }
//	if err := a.Start(ctx); err != nil { return err }
func RunFree(cfg *config.Config, configPath string) (*App, error) {
	// Step 0: Ensure storage root directory exists
	if err := os.MkdirAll(cfg.Storage.RootDir, 0o755); err != nil {
		return nil, fmt.Errorf("create storage dir %s: %w", cfg.Storage.RootDir, err)
	}

	// Step 1: Open database
	dbPath := filepath.Join(cfg.Storage.RootDir, "mibee-nvr.db")
	db, err := storage.New(dbPath)
	if err != nil {
		return nil, fmt.Errorf("db open: %w", err)
	}

	ctx := context.Background()
	if err := db.Init(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("db init: %w", err)
	}

	// Startup health check: verify that recordings marked as 'merged' still have
	// their merged output files on disk. Stale entries (from past server crashes,
	// manual deletion, or merge failures that left the DB in an inconsistent state)
	// are reset to their unmerged state so playback can fall back to original frames
	// or segments instead of serving a 404.
	validateMergedRecordings(ctx, db, cfg.Storage.RootDir)

	// Step 2: Metrics
	metrics := metrics.NewMetrics()

	// Wire DB observability hooks: query-latency histogram + SQLITE_BUSY counter.
	db.SetMetrics(metrics)
	storage.SetBusyErrorHook(metrics.IncSQLiteBusyErrors)

	// Step 2.1: Event bus
	eventBus := event.NewEventBus(64)

	// Step 2.5: Remote log handler (if enabled)
	var remoteLogHandler *remotelog.Handler
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
		rh := remotelog.New(cfg.RemoteLog.Endpoint, cfg.RemoteLog.Format, logLevel, metrics)
		remoteLogHandler = rh
		// Wrap slog.Default() with multi-handler to fan out to both stdout and remote
		if current := slog.Default(); current.Handler() != nil {
			slog.SetDefault(slog.New(remotelog.MultiHandler(current.Handler(), rh)))
		} else {
			slog.SetDefault(slog.New(rh))
		}
	}

	// Step 3: Storage manager
	store, err := storage.NewManager(cfg.Storage.RootDir, metrics)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("storage: %w", err)
	}

	// Cleanup temp files from previous crash
	if err := store.CleanupTempFiles(); err != nil {
		slog.Warn("temp cleanup", "error", err)
	}
	if err := db.CleanupIncomplete(ctx); err != nil {
		slog.Warn("incomplete cleanup", "error", err)
	}

	// Reconcile orphaned recording files (exists on disk but not in DB)
	cameraIDs := make(map[string]bool)
	for _, cam := range cfg.Cameras {
		cameraIDs[cam.ID] = true
	}
	reconciled, err := store.ReconcileOrphanedFiles(ctx, db, cameraIDs)
	if err != nil {
		slog.Error("failed to reconcile orphaned files", "error", err)
	} else if reconciled > 0 {
		slog.Info("reconciled orphaned recording files", "count", reconciled)
	}

	// Step 4: Auth middleware
	authmw.SetAuthMetrics(metrics)
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

	// Step 5: Merge manager (created before camera manager so ArchiveCamera can use it)
	mergeMgr := merge.NewMergeManager(
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
		metrics,
	)

	// Step 5.1: Rolling merge coordinator (quasi-real-time, event-driven).
	// Subscribes to SegmentCompleted and merges segments into per-camera window
	// buckets within seconds. Independent of the periodic MergeManager above.
	recordRollingMergeMgr := merge.NewRollingMergeCoordinator(
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
		metrics,
		eventBus,
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
			EventBus:        eventBus,
			Config:          cfg,
		}, metrics)
		if err != nil {
			slog.Warn("Transcoding disabled — FFmpeg is an OPTIONAL dependency; all other features (recording, playback, live streaming, relay, timelapse, merge) work without it. To enable transcoding, install ffmpeg/ffprobe or use the in-app downloader.",
				"error", err)
			transcoding.SetDisabledReason(err.Error())
		} else {
			transcodeMgr = mgr
			slog.Info("Transcoding enabled", "workers", cfg.Transcoding.MaxWorkers)
		}
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

	// Step 5.6: Timelapse rolling merge manager (shared between camera manager and API)
	var mergeMerger timelapse.TimelapseMerger
	{
		mergeMerger = timelapse.NewAutoDetectMerger()
	}
	rollingMergeMgr := timelapse.NewRollingMergeManager(mergeMerger, db, 10, false)

	camMgr := camera.NewCameraManager(cfg, store, db, configPath, metrics, mergeMgr, transcodeMgr, rollingMergeMgr, appLoc, eventBus)

	// Step 6.5: Health manager (after camera manager, before streaming)
	healthMgr := health.NewManager(cfg.Health, db)
	if healthMgr != nil {
		camMgr.SetHealthManager(healthMgr)
		// Inject metrics into the stream stats collector so that
		// nvr_stream_fps / nvr_stream_bitrate_kbps / nvr_stream_idr_interval_seconds
		// gauges are actually written.
		healthMgr.SetMetrics(metrics)
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

	periodicMergeDir := filepath.Join(cfg.Storage.RootDir, "periodic-merge")
	mergeScheduler := timelapse.NewMergeScheduler(appLoc)
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
			periodicMergeManagers[cam.ID] = timelapse.NewPeriodicMergeManager(db, db, timelapse.NewGoMerger(), fps, periodicMergeDir, dur, appLoc)
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
	hlsDataDir := filepath.Join(cfg.Storage.RootDir, "hls")
	hlsMgr := hls.NewManagerWithOpts(context.Background(), hlsDataDir, cfg.HLS.WriteBufferSize, cfg.HLS.SegmentMaxSizeMB*1024*1024, cfg.HLS.SegmentCount, metrics)
	// Configure Low-Latency HLS if enabled
	if cfg.HLS.LowLatency {
		partDur, _ := time.ParseDuration(cfg.HLS.PartMinDuration)
		hlsMgr.SetLowLatency(true, partDur)
	}

	// Step 7.5: WebRTC manager (H.264 only)
	var webrtcMgr *webrtc.Manager
	if cfg.Streaming.WebRTC.Enabled != nil && *cfg.Streaming.WebRTC.Enabled {
		idleTimeout, _ := time.ParseDuration(cfg.Streaming.WebRTC.IdleTimeout)
		webrtcMgr = webrtc.NewManager(
			webrtc.WithMaxPeers(cfg.Streaming.WebRTC.MaxViewers),
			webrtc.WithIdleTimeout(idleTimeout),
			webrtc.WithMetrics(metrics),
		)
		slog.Info("WebRTC manager initialized", "max_viewers", cfg.Streaming.WebRTC.MaxViewers)
	}

	// Step 7.6: FLV manager
	var flvMgr *flv.Manager
	if cfg.Streaming.FLV.Enabled != nil && *cfg.Streaming.FLV.Enabled {
		flvMgr = flv.NewManager(
			flv.WithMaxViewers(cfg.Streaming.FLV.MaxViewers),
			flv.WithMetrics(metrics),
		)
		slog.Info("FLV manager initialized", "max_viewers", cfg.Streaming.FLV.MaxViewers)
	}

	// Step 7.7: WebSocket stream manager (always available)
	wsMgr := wsstream.NewManager(
		wsstream.WithMaxViewers(cfg.WebSocket.MaxViewers),
		wsstream.WithWriteBufSize(cfg.WebSocket.WriteBufSize),
		wsstream.WithIdleTimeout(cfg.WebSocket.IdleTimeout),
	)
	slog.Info("WebSocket stream manager initialized", "max_viewers", cfg.WebSocket.MaxViewers, "write_buf_size", cfg.WebSocket.WriteBufSize, "idle_timeout", cfg.WebSocket.IdleTimeout)

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

	// Step 7.7: RTMP server (optional)
	var rtmpServer *rtmp.Server
	if cfg.RTMP.Enabled != nil && *cfg.RTMP.Enabled {
		rtmpServer = rtmp.NewServer(
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
		rtmpServer.NALUProvider = func(cameraID string) rtmp.NALUCallback {
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

	// Step 7.8: SRT listener (optional)
	var srtListener *srt.Listener
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
		srtListener = srt.NewListener(cfg.SRT)
		srtListener.HubProvider = camMgr.GetOrCreateHub
		srtListener.OnConnect = func(cameraID string, _ *model.StreamHub) {
			if ir := camMgr.GetIngestRecorder(cameraID); ir != nil {
				ir.WriteConnected()
			}
		}
		srtListener.OnDisconnect = func(cameraID string) {
			if ir := camMgr.GetIngestRecorder(cameraID); ir != nil {
				ir.OnDisconnect()
			}
		}
		srtListener.NALUProvider = func(cameraID string) func(au [][]byte, ptsTicks int64, isIDR bool) {
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

	// Step 8: Cleanup manager
	cleanupMgr, err := cleanup.NewCleanupManager(db, store, cfg.Cleanup, metrics)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("cleanup: %w", err)
	}
	cleanupMgr.SetEventBus(eventBus)
	// Wire the live yaml camera set so directory-scanning cleanup (orphan /
	// stale-record) skips dirs belonging to cameras that were removed from
	// the config but whose rows/files linger — avoids recurring O(N) stat
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

	// Step 9: Optional MQTT client
	var mqttClient *mqtt.Client
	if cfg.MQTT.Enabled {
		mqttClient = mqtt.NewClient(cfg.MQTT.Broker, cfg.MQTT.ClientID, cfg.MQTT.Topic, cfg.MQTT.Username, cfg.MQTT.Password, nil)
	}

	// Wire MQTT client into health manager for event publishing
	if healthMgr != nil && mqttClient != nil {
		healthMgr.SetMQTTClient(mqttClient)
	}

	// Step 10: Optional FTP server
	var ftpServer *ftp.Server
	if cfg.FTP.Enabled != nil && *cfg.FTP.Enabled {
		ftpAddr := fmt.Sprintf(":%d", cfg.FTP.Port)
		ftpServer = ftp.NewServer(ftpAddr, cfg.FTP.PassivePortRange, cfg.Auth.Username, cfg.Auth.Password, store, db)
	}

	// ---- Build HTTP router ----
	cloudProxy := api.NewLocalXiaomiAuth(cfg)
	handler := api.NewHandler(db, store, authMW, cfg, camMgr, hlsMgr, configPath, mergeMgr, cloudProxy, mergeScheduler)

	// Wire streaming managers
	handler.SetWebRTCManager(webrtcMgr)
	handler.SetFLVManager(flvMgr)
	handler.SetWSManager(wsMgr)
	handler.SetHealthManager(healthMgr)
	handler.SetStabilityProvider(healthMgr)
	handler.SetEventBus(eventBus)
	api.SetAPIMetrics(metrics)
	if rollingMergeMgr != nil {
		handler.SetTimelapseMergeMgr(rollingMergeMgr)
	}
	handler.SetRollingMergeMgr(recordRollingMergeMgr)
	// Wire AI handler (config + zones only, no backend inference)
	aiMgr := ai.NewManager(aiConfigFromConfig(cfg.AI), eventBus)
	ah := api.NewAIHandler(aiMgr, cfg, configPath)
	handler.SetAIHandler(ah)
	handler.SetRelayManager(relayMgr)
	// Create and populate StreamRegistry for protocol discovery
	reg := api.NewStreamRegistry()
	reg.Register(&api.HLSStreamHandler{Mgr: hlsMgr})
	// Always register LL-HLS so it appears as greyed-out when disabled
	reg.Register(&api.LLHLSStreamHandler{
		HLSStreamHandler:  api.HLSStreamHandler{Mgr: hlsMgr},
		LowLatencyEnabled: cfg.HLS.LowLatency,
	})
	if webrtcMgr != nil {
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
	r, err := buildRouter(cfg, authMW, handler, metrics, davHandler, uploadHandler)
	if err != nil {
		return nil, err
	}

	httpServer := &http.Server{
		Addr:    cfg.Server.Listen,
		Handler: r,
	}

	// ---- Create App and register services ----
	a := New()

	// 1. db — registered first so it stops last
	if err := a.Register(&serviceFunc{
		name: "db",
		startFunc: func(_ context.Context) error {
			return nil
		},
		stopFunc: func() error {
			db.Close()
			return nil
		},
	}); err != nil {
		return nil, fmt.Errorf("register db: %w", err)
	}

	// 2. camera — relay folded in (relay starts before camera, stops before camera)
	if err := a.Register(&serviceFunc{
		name: "camera",
		startFunc: func(ctx context.Context) error {
			if relayMgr != nil {
				relayMgr.Start(ctx)
			}
			go func() {
				if err := camMgr.Start(ctx); err != nil {
					slog.Error("camera manager", "error", err)
				}
			}()
			return nil
		},
		stopFunc: func() error {
			if relayMgr != nil {
				relayMgr.Stop()
			}
			if err := camMgr.Stop(); err != nil {
				slog.Warn("camera manager stop error", "error", err)
			}
			return nil
		},
	}); err != nil {
		return nil, fmt.Errorf("register camera: %w", err)
	}

	// 3. health (always present)
	if err := a.Register(&serviceFunc{
		name: "health",
		startFunc: func(ctx context.Context) error {
			if err := healthMgr.Start(ctx); err != nil {
				slog.Error("health manager", "error", err)
			}
			return nil
		},
		stopFunc: func() error {
			healthMgr.Stop()
			return nil
		},
	}); err != nil {
		return nil, fmt.Errorf("register health: %w", err)
	}

	// 4. merge — run in background goroutine with its own cancel
	{
		var mergeCancel context.CancelFunc
		if err := a.Register(&serviceFunc{
			name: "merge",
			startFunc: func(ctx context.Context) error {
				var runCtx context.Context
				runCtx, mergeCancel = context.WithCancel(ctx)
				go func() {
					if cfg.Merge.Enabled {
						mergeMgr.Run(runCtx)
						slog.Info("merge-manager stopped")
					}
				}()
				return nil
			},
			stopFunc: func() error {
				if mergeCancel != nil {
					mergeCancel()
				}
				return nil
			},
		}); err != nil {
			return nil, fmt.Errorf("register merge: %w", err)
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
				return recordRollingMergeMgr.Start(runCtx)
			},
			stopFunc: func() error {
				if rollingCancel != nil {
					rollingCancel()
				}
				recordRollingMergeMgr.Stop()
				return nil
			},
		}); err != nil {
			return nil, fmt.Errorf("register rolling-merge: %w", err)
		}
	}

	// 5. transcode (optional)
	if transcodeMgr != nil {
		if err := a.Register(&serviceFunc{
			name: "transcode",
			startFunc: func(ctx context.Context) error {
				go transcodeMgr.Run(ctx)
				return nil
			},
			stopFunc: func() error {
				transcodeMgr.Stop()
				return nil
			},
		}); err != nil {
			return nil, fmt.Errorf("register transcode: %w", err)
		}
	}

	// 6. mergeScheduler
	if err := a.Register(&serviceFunc{
		name: "mergeScheduler",
		startFunc: func(ctx context.Context) error {
			mergeScheduler.Start(ctx)
			return nil
		},
		stopFunc: func() error {
			mergeScheduler.Stop()
			return nil
		},
	}); err != nil {
		return nil, fmt.Errorf("register mergeScheduler: %w", err)
	}

	// 7. cleanup — run in background goroutine with its own cancel
	{
		var cleanupCancel context.CancelFunc
		if err := a.Register(&serviceFunc{
			name: "cleanup",
			startFunc: func(ctx context.Context) error {
				var runCtx context.Context
				runCtx, cleanupCancel = context.WithCancel(ctx)
				go cleanupMgr.Run(runCtx)
				return nil
			},
			stopFunc: func() error {
				if cleanupCancel != nil {
					cleanupCancel()
				}
				return nil
			},
		}); err != nil {
			return nil, fmt.Errorf("register cleanup: %w", err)
		}
	}

	// 8. mqtt (optional)
	if mqttClient != nil {
		if err := a.Register(&serviceFunc{
			name: "mqtt",
			startFunc: func(ctx context.Context) error {
				go func() {
					if err := mqttClient.Start(ctx); err != nil {
						slog.Error("mqtt", "error", err)
					}
				}()
				return nil
			},
			stopFunc: func() error {
				if err := mqttClient.Stop(); err != nil {
					slog.Warn("MQTT stop error", "error", err)
				}
				return nil
			},
		}); err != nil {
			return nil, fmt.Errorf("register mqtt: %w", err)
		}
	}

	// 9. ftp (optional)
	if ftpServer != nil {
		if err := a.Register(&serviceFunc{
			name: "ftp",
			startFunc: func(ctx context.Context) error {
				go func() {
					if err := ftpServer.Start(ctx); err != nil {
						slog.Error("ftp", "error", err)
					}
				}()
				return nil
			},
			stopFunc: func() error {
				ftpServer.Close()
				return nil
			},
		}); err != nil {
			return nil, fmt.Errorf("register ftp: %w", err)
		}
	}

	// 10. rtmp (optional)
	if rtmpServer != nil {
		if err := a.Register(&serviceFunc{
			name: "rtmp",
			startFunc: func(ctx context.Context) error {
				go func() {
					if err := rtmpServer.Start(ctx); err != nil {
						slog.Error("rtmp", "error", err)
					}
				}()
				return nil
			},
			stopFunc: func() error {
				_ = rtmpServer.Stop()
				return nil
			},
		}); err != nil {
			return nil, fmt.Errorf("register rtmp: %w", err)
		}
	}

	// 11. srt (optional)
	if srtListener != nil {
		if err := a.Register(&serviceFunc{
			name: "srt",
			startFunc: func(ctx context.Context) error {
				go func() {
					if err := srtListener.Start(); err != nil {
						slog.Error("srt", "error", err)
					}
				}()
				if err := srtListener.StartCallers(); err != nil {
					slog.Error("srt callers", "error", err)
				}
				return nil
			},
			stopFunc: func() error {
				_ = srtListener.Stop()
				return nil
			},
		}); err != nil {
			return nil, fmt.Errorf("register srt: %w", err)
		}
	}

	// 12. ws
	if err := a.Register(&serviceFunc{
		name: "ws",
		startFunc: func(_ context.Context) error {
			return nil
		},
		stopFunc: func() error {
			wsMgr.StopAll()
			return nil
		},
	}); err != nil {
		return nil, fmt.Errorf("register ws: %w", err)
	}

	// 13. webrtc (optional)
	if webrtcMgr != nil {
		if err := a.Register(&serviceFunc{
			name: "webrtc",
			startFunc: func(_ context.Context) error {
				return nil
			},
			stopFunc: func() error {
				webrtcMgr.StopAll()
				return nil
			},
		}); err != nil {
			return nil, fmt.Errorf("register webrtc: %w", err)
		}
	}

	// 14. hls
	if err := a.Register(&serviceFunc{
		name: "hls",
		startFunc: func(_ context.Context) error {
			return nil
		},
		stopFunc: func() error {
			hlsMgr.StopAll()
			return nil
		},
	}); err != nil {
		return nil, fmt.Errorf("register hls: %w", err)
	}

	// 15. remoteLog (optional) — registered last so it stops first
	if remoteLogHandler != nil {
		if err := a.Register(&serviceFunc{
			name: "remoteLog",
			startFunc: func(_ context.Context) error {
				return nil
			},
			stopFunc: func() error {
				remoteLogHandler.Close()
				return nil
			},
		}); err != nil {
			return nil, fmt.Errorf("register remoteLog: %w", err)
		}
	}

	// Values (typed handles for out-of-module consumers)
	if err := a.RegisterValue("camera-manager", camMgr.AsPublic()); err != nil {
		return nil, fmt.Errorf("register camera-manager value: %w", err)
	}
	if err := a.RegisterValue("relay-manager", relayMgr); err != nil {
		return nil, fmt.Errorf("register relay-manager value: %w", err)
	}
	if err := a.RegisterValue("eventbus", event.NewBusAdapter(eventBus)); err != nil {
		return nil, fmt.Errorf("register eventbus value: %w", err)
	}
	if err := a.RegisterValue("config", cfg); err != nil {
		return nil, fmt.Errorf("register config value: %w", err)
	}
	if err := a.RegisterValue("http-router", r); err != nil {
		return nil, fmt.Errorf("register http-router value: %w", err)
	}

	if err := a.RegisterValue("http-server", httpServer); err != nil {
		return nil, fmt.Errorf("register http-server value: %w", err)
	}

	return a, nil
}
