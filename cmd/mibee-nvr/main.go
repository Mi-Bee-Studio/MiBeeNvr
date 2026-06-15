package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	_ "net/http/pprof"
	_ "time/tzdata" // embed timezone database for minimal containers (scratch/alpine)

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/api"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/camera"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/cleanup"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/flv"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/ftp"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/hls"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/health"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/merge"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	authmw "github.com/Mi-Bee-Studio/MiBeeNvr/internal/middleware"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/middleware/remotelog"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/mqtt"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/rtmp"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/srt"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	ui "github.com/Mi-Bee-Studio/MiBeeNvr/internal/ui"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/upload"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/webrtc"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/webdav"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/wsstream"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/ai"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/timelapse"
	_ "github.com/Mi-Bee-Studio/MiBeeNvr/internal/xiaomi"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/transcoding"
)

var (
	configPath = flag.String("config", "mibee-nvr.yaml", "path to configuration file")
	version    = flag.Bool("version", false, "print version and exit")
)

var appVersion = "0.1.0-dev" // overridden via -ldflags at build time

func autoInitConfig(configPath string) *config.Config {
	// Determine data directory
	dataDir := os.Getenv("NVR_DATA_DIR")
	if dataDir == "" {
		// Check if /data exists (Docker container)
		if info, err := os.Stat("/data"); err == nil && info.IsDir() {
			dataDir = "/data"
		} else {
			dataDir = "/var/lib/mibee-nvr"
		}
	}

	// Check for initial password from env var
	password := os.Getenv("NVR_PASSWORD")

	cfg := &config.Config{
		Server:        config.ServerConfig{Listen: ":9090"},
		Storage:       config.StorageConfig{RootDir: dataDir, SegmentDuration: "30s"},
		Auth:          config.AuthConfig{Username: "admin"},
		Cameras:       []config.CameraConfig{},
		Cleanup:       config.CleanupConfig{RetentionDays: 30, CheckInterval: "1h", DiskThresholdPercent: 95},
		FTP:           config.FTPConfig{Port: 2121, PassivePortRange: "2122-2140"},
		WebDAV:        config.WebDAVConfig{PathPrefix: "/dav"},
		Observability: config.ObservabilityConfig{LogLevel: "info", LogFormat: "text"},
		Version:       "1.0",
		AI: config.AIConfig{
			Enabled:             false,
			ConfidenceThreshold: 0.5,
			FrameSkipRate:       10,
			EnabledCameras:      []string{},
		},
}
	// Apply defaults so all fields (HLS, etc.) are populated before saving
	cfg.ApplyDefaults()

	if password != "" {
		if len(password) < 8 {
			slog.Error("NVR_PASSWORD must be at least 8 characters")
			os.Exit(1)
		}
		cfg.Auth.Password = password
	}
	// Create data directory if needed
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		slog.Warn("failed to create data directory", "dir", dataDir, "error", err)
	}

	// Create config directory if needed
	configDir := filepath.Dir(configPath)
	if configDir != "." && configDir != "/" {
		if err := os.MkdirAll(configDir, 0755); err != nil {
			slog.Warn("failed to create config directory", "dir", configDir, "error", err)
		}
	}

	if err := config.Save(configPath, cfg); err != nil {
		slog.Warn("failed to save auto-generated config", "path", configPath, "error", err)
	} else {
		slog.Info("auto-generated default config", "path", configPath, "data_dir", dataDir)
		if password == "" {
			slog.Warn("no password set — all API requests will return 503 until a password is configured. Set via NVR_PASSWORD env var or edit the config")
		}
	}

	return cfg
}

// dockerStorageDir detects the correct storage directory for Docker environments.
// Returns empty string if not running in Docker or no Docker-specific path found.
func dockerStorageDir() string {
	// Method 1: Explicit env var (set in Dockerfile and docker-compose.yml)
	if dir := os.Getenv("NVR_DATA_DIR"); dir != "" {
		return dir
	}
	// Method 2: /data directory exists (Docker container indicator)
	if info, err := os.Stat("/data"); err == nil && info.IsDir() {
		return "/data"
	}
	// Method 3: Docker marker files
	if _, err := os.Stat("/.dockerenv"); err == nil {
		// Running in Docker but NVR_DATA_DIR not set — check /data
		if info, err := os.Stat("/data"); err == nil && info.IsDir() {
			return "/data"
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// CLI subcommands
// ---------------------------------------------------------------------------

func cmdHealth() {
	addr := ":9090"
	for i := 2; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--addr":
			i++
			if i < len(os.Args) {
				addr = os.Args[i]
			}
		case "--config":
			i++
			if i < len(os.Args) {
				cfg, err := config.Load(os.Args[i])
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
					os.Exit(1)
				}
				if cfg.Server.Listen != "" {
					addr = cfg.Server.Listen
				}
			}
		}
	}
	resp, err := http.Get("http://localhost" + addr + "/api/health")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Health check failed: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Health check failed: HTTP %d\n", resp.StatusCode)
		os.Exit(1)
	}
	os.Exit(0)
}

func cmdInit() {
	var password, dataDir, listenAddr, cfgPath, username string
	var force bool
	for i := 2; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--password":
			i++
			if i < len(os.Args) {
				password = os.Args[i]
			}
		case "--data-dir":
			i++
			if i < len(os.Args) {
				dataDir = os.Args[i]
			}
		case "--listen":
			i++
			if i < len(os.Args) {
				listenAddr = os.Args[i]
			}
		case "--config":
			i++
			if i < len(os.Args) {
				cfgPath = os.Args[i]
			}
		case "--username":
			i++
			if i < len(os.Args) {
				username = os.Args[i]
			}
		case "--force":
			force = true
		}
	}
	if dataDir == "" {
		dataDir = "/var/lib/mibee-nvr"
	}
	if listenAddr == "" {
		listenAddr = ":9090"
	}
	if cfgPath == "" {
		cfgPath = "mibee-nvr.yaml"
	}
	if username == "" {
		username = "admin"
	}
	if password == "" {
		stat, _ := os.Stdin.Stat()
		if (stat.Mode() & os.ModeCharDevice) != 0 {
			fmt.Print("Enter password: ")
			scanner := bufio.NewScanner(os.Stdin)
			if scanner.Scan() {
				password = scanner.Text()
			}
		}
		if password == "" {
			fmt.Fprintln(os.Stderr, "Error: password is required (use --password or provide via terminal)")
			os.Exit(1)
		}
	}
	if len(password) < 8 {
		fmt.Fprintln(os.Stderr, "Error: password must be at least 8 characters")
		os.Exit(1)
	}
	if _, err := os.Stat(cfgPath); err == nil && !force {
		fmt.Fprintf(os.Stderr, "Error: config file %s already exists (use --force to overwrite)\n", cfgPath)
		os.Exit(2)
	}
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating data directory: %v\n", err)
		os.Exit(1)
	}
	hash, err := authmw.HashPassword(password)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error hashing password: %v\n", err)
		os.Exit(1)
	}
	cfg := config.Config{
		Server:        config.ServerConfig{Listen: listenAddr},
		Storage:       config.StorageConfig{RootDir: dataDir, SegmentDuration: "30s"},
		Auth:          config.AuthConfig{Username: username, PasswordHash: hash},
		Cameras:       []config.CameraConfig{},
		Cleanup:       config.CleanupConfig{RetentionDays: 30, CheckInterval: "1h", DiskThresholdPercent: 95},
		FTP:           config.FTPConfig{Port: 2121, PassivePortRange: "2122-2140"},
		WebDAV:        config.WebDAVConfig{PathPrefix: "/dav"},
		Observability: config.ObservabilityConfig{LogLevel: "info", LogFormat: "text"},
		Version:       "1.0",
		AI: config.AIConfig{
			Enabled:             false,
			ConfidenceThreshold: 0.5,
			FrameSkipRate:       10,
			EnabledCameras:      []string{},
		},
	}
	if err := config.Save(cfgPath, &cfg); err != nil {
		fmt.Fprintf(os.Stderr, "Error saving config: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Configuration saved to %s\n", cfgPath)
	fmt.Printf("Data directory: %s\n", dataDir)
	fmt.Println("\nNext steps:")
	fmt.Printf("  1. Edit %s to add your cameras\n", cfgPath)
	fmt.Printf("  2. Run: ./mibee-nvr -config %s\n", cfgPath)
	fmt.Printf("  3. Open http://localhost%s in your browser\n", listenAddr)
	os.Exit(0)
}

func cmdHashPassword() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "Usage: mibee-nvr hash-password <password>")
		os.Exit(1)
	}
	hash, err := authmw.HashPassword(os.Args[2])
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(hash)
	os.Exit(0)
}

func cmdEncryptConfig() {
	cfgPath := "mibee-nvr.yaml"
	for i := 2; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--config":
			i++
			if i < len(os.Args) {
				cfgPath = os.Args[i]
			}
		}
	}
	fields, err := config.EncryptConfigFile(cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if len(fields) == 0 {
		fmt.Println("No plaintext sensitive fields found. All fields are already encrypted or empty.")
	} else {
		fmt.Printf("Encrypted %d sensitive field(s) in %s:\n", len(fields), cfgPath)
		for _, f := range fields {
			fmt.Printf("  - %s\n", f)
		}
	}
	os.Exit(0)
}

// ---------------------------------------------------------------------------
// App — encapsulates all application dependencies and lifecycle
// ---------------------------------------------------------------------------

// App holds all major components of the NVR application.
//
// Initialization order (dependency chain):
//
//	1. Config — loaded/validated before App creation
//	2. Storage (DB) — SQLite, schema migrations
//	3. Metrics — Prometheus registry
//	4. Storage Manager — file operations, temp cleanup, orphan reconciliation
//	5. Auth middleware — bcrypt, auto-hash persistence
//	6. Merge manager — depends on DB, Storage, Config
//	7. Camera manager — depends on Config, Storage, DB, Metrics, Merge
//	8. HLS manager — depends on Config, Metrics
//	9. API handler — depends on DB, Storage, Auth, Config, Camera, HLS, Merge
//	10. WebDAV server — depends on Storage, Auth, DB, Config
//	11. Upload handler — depends on Storage, DB
//	12. Cleanup manager — depends on DB, Storage, Config, Metrics
//	13. MQTT client — depends on Config
//	14. FTP server — depends on Config, Storage, DB
//	15. HTTP server — depends on Router (which depends on all above)
type App struct {
	cfg        *config.Config
	configPath string

	// Core infrastructure
	db      *storage.DB
	store   *storage.Manager
	metrics *metrics.Metrics
	authMW  func(http.Handler) http.Handler

	// Managers
	mergeMgr   *merge.MergeManager
	camMgr     *camera.CameraManager
	hlsMgr     *hls.Manager
	cleanupMgr *cleanup.CleanupManager
	healthMgr  *health.Manager
	mergeScheduler *timelapse.MergeScheduler
	rollingMergeMgr *timelapse.RollingMergeManager

	// Optional network services (nil when disabled)
	mqttClient  *mqtt.Client
	ftpServer   *ftp.Server
	rtmpServer  *rtmp.Server
	srtListener *srt.Listener

	// Streaming managers
	webrtcMgr    *webrtc.Manager
	flvMgr       *flv.Manager
	wsMgr        *wsstream.Manager
	transcodeMgr *transcoding.TranscodeManager
	// HTTP server
	httpServer *http.Server

	// Remote log handler (nil when disabled)
	remoteLogHandler *remotelog.Handler

	// Lifecycle
	cancel context.CancelFunc

	// Event bus
	eventBus *event.EventBus
}

// NewApp constructs the application with all dependencies initialized in
// correct order. It opens the database, runs migrations, creates storage
// and all managers but does NOT start any goroutines — call Start() for that.
func NewApp(cfg *config.Config, configPath string) (*App, error) {
	a := &App{
		cfg:        cfg,
		configPath: configPath,
	}

	// Step 0: Ensure storage root directory exists
	if err := os.MkdirAll(cfg.Storage.RootDir, 0755); err != nil {
		return nil, fmt.Errorf("create storage dir %s: %w", cfg.Storage.RootDir, err)
	}

	// Step 1: Open database
	dbPath := filepath.Join(cfg.Storage.RootDir, "mibee-nvr.db")
	db, err := storage.New(dbPath)
	if err != nil {
		return nil, fmt.Errorf("db open: %w", err)
	}
	a.db = db

	ctx := context.Background()
	if err := db.Init(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("db init: %w", err)
	}

	// Step 2: Metrics
	a.metrics = metrics.NewMetrics()

	// Step 2.1: Event bus
	a.eventBus = event.NewEventBus(64)

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
		rh := remotelog.New(cfg.RemoteLog.Endpoint, cfg.RemoteLog.Format, logLevel, a.metrics)
		a.remoteLogHandler = rh
		// Wrap slog.Default() with multi-handler to fan out to both stdout and remote
		if current := slog.Default(); current.Handler() != nil {
			slog.SetDefault(slog.New(remotelog.MultiHandler(current.Handler(), rh)))
		} else {
			slog.SetDefault(slog.New(rh))
		}
	}

	// Step 3: Storage manager
	store, err := storage.NewManager(cfg.Storage.RootDir, a.metrics)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("storage: %w", err)
	}
	a.store = store

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
	authmw.SetAuthMetrics(a.metrics)
	authMW, effectiveHash := authmw.NewAuthMiddleware(authmw.AuthProvider{
		GetUsername: func() string { return cfg.Auth.Username },
		GetHash:     func() string { return cfg.Auth.PasswordHash },
	}, cfg.Auth.Password, authmw.AuthRateLimitConfig{
		Enabled:       cfg.Auth.RateLimit.Enabled != nil && *cfg.Auth.RateLimit.Enabled,
		MaxFailures:   cfg.Auth.RateLimit.MaxFailures,
		WindowMinutes: cfg.Auth.RateLimit.WindowMinutes,
	})
	a.authMW = authMW
	if effectiveHash != "" && cfg.Auth.PasswordHash == "" && cfg.Auth.Password != "" {
		slog.Info("persisting auto-hashed password to config", "component", "main")
		cfg.Auth.PasswordHash = effectiveHash
		cfg.Auth.Password = ""
		if err := config.Save(configPath, cfg); err != nil {
			slog.Error("failed to save config after auto-hash", "error", err)
		}
	}

	// Step 5: Merge manager (created before camera manager so ArchiveCamera can use it)
	a.mergeMgr = merge.NewMergeManager(
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
		a.metrics,
	)

	// Step 5.5: Transcode manager (after merge, before camera)
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
			EventBus:        a.eventBus,
			Config:          cfg,
		}, a.metrics)
		if err != nil {
			slog.Warn("Transcoding disabled", "error", err)
			transcoding.SetDisabledReason(err.Error())
		} else {
			a.transcodeMgr = mgr
			slog.Info("Transcoding enabled", "workers", cfg.Transcoding.MaxWorkers)
		}
	}

	// Load display timezone for merge window alignment and camera scheduling.
	var appLoc *time.Location = time.Local // Default: use server's local timezone
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

	// Step 5.5: Initialize global FFmpeg/ffprobe binary paths for the transcoding package.
	// TODO: transcoding.SetBinaryPaths not yet implemented

	// Step 5.6: Timelapse rolling merge manager (shared between camera manager and API)
	// Probe FFmpeg for H.265→H.264 transcoding in timelapse merge.
	var mergeMerger timelapse.TimelapseMerger
	{
		mergeMerger = timelapse.NewAutoDetectMerger()
	}
	a.rollingMergeMgr = timelapse.NewRollingMergeManager(mergeMerger, db, 10, false)

	a.camMgr = camera.NewCameraManager(cfg, store, db, configPath, a.metrics, a.mergeMgr, a.transcodeMgr, a.rollingMergeMgr, appLoc, a.eventBus)
	// Step 6.5: Health manager (after camera manager, before streaming)
	a.healthMgr = health.NewManager(cfg.Health, db)
	if a.healthMgr != nil {
		a.camMgr.SetHealthManager(a.healthMgr)
		// Inject metrics into the stream stats collector so that
		// nvr_stream_fps / nvr_stream_bitrate_kbps / nvr_stream_idr_interval_seconds
		// gauges are actually written.
		a.healthMgr.SetMetrics(a.metrics)
	}
	// Wire auto-remediation into health manager
	if a.healthMgr != nil && a.camMgr != nil {
		a.healthMgr.SetRestarter(a.camMgr.RestartRecorder)
		a.healthMgr.SetCameraEnabledFn(func(cameraID string) bool {
			return a.camMgr.GetCameraConfig(cameraID) != nil
		})
	}

	periodicMergeDir := filepath.Join(cfg.Storage.RootDir, "periodic-merge")
	a.mergeScheduler = timelapse.NewMergeScheduler(appLoc)
	// Pre-create per-camera merge managers and register them in the scheduler
	periodicMergeManagers := make(map[string]*timelapse.PeriodicMergeManager)
	for _, cam := range cfg.Cameras {
		if cam.Timelapse != nil {
			dur := 24 * time.Hour
			if cam.Timelapse.MergeDuration != "" {
				if parsed, err := config.ParseMergeDuration(cam.Timelapse.MergeDuration); err == nil {
					dur = parsed
				} else {
					slog.Warn("merge scheduler: invalid merge duration, defaulting to 24h",
						"camera_id", cam.ID,
						"merge_duration", cam.Timelapse.MergeDuration,
						"error", err,
					)
				}
			}
			periodicMergeManagers[cam.ID] = timelapse.NewPeriodicMergeManager(db, db, timelapse.NewGoMerger(), 10, periodicMergeDir, dur, appLoc)
			a.mergeScheduler.AddOrUpdate(cam.ID, dur)
			slog.Info("merge scheduler: configured camera",
				"camera_id", cam.ID,
				"duration", dur.String(),
			)
		}
	}
	a.mergeScheduler.SetRunFunc(func(ctx context.Context, cameraID string, refTime time.Time) error {
		manager, ok := periodicMergeManagers[cameraID]
		if !ok {
			return fmt.Errorf("merge scheduler: no manager for camera %s", cameraID)
		}
		return manager.Run(ctx, cameraID, refTime)
	})

	// Step 7: HLS manager
	hlsDataDir := filepath.Join(cfg.Storage.RootDir, "hls")
	a.hlsMgr = hls.NewManagerWithOpts(context.Background(), hlsDataDir, cfg.HLS.WriteBufferSize, cfg.HLS.SegmentMaxSizeMB*1024*1024, cfg.HLS.SegmentCount, a.metrics)
	// Configure Low-Latency HLS if enabled
	if cfg.HLS.LowLatency {
		partDur, _ := time.ParseDuration(cfg.HLS.PartMinDuration)
		a.hlsMgr.SetLowLatency(true, partDur)
	}

	// Step 7.5: WebRTC manager (H.264 only)
	if cfg.Streaming.WebRTC.Enabled != nil && *cfg.Streaming.WebRTC.Enabled {
		idleTimeout, _ := time.ParseDuration(cfg.Streaming.WebRTC.IdleTimeout)
		a.webrtcMgr = webrtc.NewManager(
			webrtc.WithMaxPeers(cfg.Streaming.WebRTC.MaxViewers),
			webrtc.WithIdleTimeout(idleTimeout),
			webrtc.WithMetrics(a.metrics),
		)
		slog.Info("WebRTC manager initialized", "max_viewers", cfg.Streaming.WebRTC.MaxViewers)
	}

	// Step 7.6: FLV manager
	if cfg.Streaming.FLV.Enabled != nil && *cfg.Streaming.FLV.Enabled {
		a.flvMgr = flv.NewManager(
			flv.WithMaxViewers(cfg.Streaming.FLV.MaxViewers),
			flv.WithMetrics(a.metrics),
		)
		slog.Info("FLV manager initialized", "max_viewers", cfg.Streaming.FLV.MaxViewers)
	}

	// Step 7.7: WebSocket stream manager (always available)
	a.wsMgr = wsstream.NewManager(
		wsstream.WithMaxViewers(cfg.WebSocket.MaxViewers),
		wsstream.WithWriteBufSize(cfg.WebSocket.WriteBufSize),
		wsstream.WithIdleTimeout(cfg.WebSocket.IdleTimeout),
	)
	slog.Info("WebSocket stream manager initialized", "max_viewers", cfg.WebSocket.MaxViewers, "write_buf_size", cfg.WebSocket.WriteBufSize, "idle_timeout", cfg.WebSocket.IdleTimeout)

	// Step 7.7: RTMP server (optional)
	if cfg.RTMP.Enabled != nil && *cfg.RTMP.Enabled {
		a.rtmpServer = rtmp.NewServer(
			rtmp.Config{Addr: fmt.Sprintf(":%d", cfg.RTMP.Port)},
			nil, nil, nil, nil, // resolv, hubFn, onConn, onDisc — can be wired later
		)
		slog.Info("RTMP server configured", "port", cfg.RTMP.Port)
	}

	// Step 7.8: SRT listener (optional)
	if cfg.SRT.Enabled != nil && *cfg.SRT.Enabled {
		a.srtListener = srt.NewListener(cfg.SRT)
		slog.Info("SRT listener configured", "port", cfg.SRT.Port)
	}

	// Step 8: Cleanup manager
	a.cleanupMgr, err = cleanup.NewCleanupManager(db, store, cfg.Cleanup, a.metrics)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("cleanup: %w", err)
	}
	if cfg.Health.Enabled {
		healthRetention, err := time.ParseDuration(cfg.Health.EventsRetention)
		if err != nil {
			slog.Warn("invalid health events_retention, disabling health cleanup", "error", err)
		} else {
			a.cleanupMgr.SetHealthConfig(true, healthRetention)
		}
	}

	// Wire transcode orphan cleanup into periodic cleanup
	if a.transcodeMgr != nil {
		dataDir := cfg.Storage.RootDir
		a.cleanupMgr.SetTranscodeOrphanCleanup(func(ctx context.Context) error {
			return transcoding.CleanOrphanedTranscodes(ctx, dataDir, db)
		})
	}
	// Wire transcode history retention cleanup
	if cfg.Transcoding.HistoryRetention != "" {
		if hr, err := time.ParseDuration(cfg.Transcoding.HistoryRetention); err == nil {
			a.cleanupMgr.SetTranscodeHistoryRetention(hr)
		}
	}
	// Wire ffprobe path for zero-duration recording repair
	// TODO: transcoding.FFprobePath not yet implemented

	// Step 9: Optional MQTT client
	if cfg.MQTT.Enabled {
		a.mqttClient = mqtt.NewClient(cfg.MQTT.Broker, cfg.MQTT.ClientID, cfg.MQTT.Topic, cfg.MQTT.Username, cfg.MQTT.Password, nil)
	}

	// Wire MQTT client into health manager for event publishing
	if a.healthMgr != nil && a.mqttClient != nil {
		a.healthMgr.SetMQTTClient(a.mqttClient)
	}

	// Step 10: Optional FTP server
	if cfg.FTP.Enabled != nil && *cfg.FTP.Enabled {
		ftpAddr := fmt.Sprintf(":%d", cfg.FTP.Port)
		a.ftpServer = ftp.NewServer(ftpAddr, cfg.FTP.PassivePortRange, cfg.Auth.Username, cfg.Auth.Password, store, db)
	}

	// Step 11: Build HTTP router
	a.httpServer = &http.Server{
		Addr:    cfg.Server.Listen,
		Handler: a.buildRouter(),
	}

	return a, nil
}

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

// buildRouter constructs the chi router with all routes mounted.
func (a *App) buildRouter() http.Handler {
	cfg := a.cfg

	cloudProxy := api.NewLocalXiaomiAuth(cfg)
	handler := api.NewHandler(a.db, a.store, a.authMW, cfg, a.camMgr, a.hlsMgr, a.configPath, a.mergeMgr, cloudProxy, a.mergeScheduler)

	// Wire streaming managers
	handler.SetWebRTCManager(a.webrtcMgr)
	handler.SetFLVManager(a.flvMgr)
	handler.SetWSManager(a.wsMgr)
	handler.SetHealthManager(a.healthMgr)
	handler.SetStabilityProvider(a.healthMgr)
	handler.SetEventBus(a.eventBus)
	if a.rollingMergeMgr != nil {
		handler.SetTimelapseMergeMgr(a.rollingMergeMgr)
	}
	// Wire AI handler (config + zones only, no backend inference)
	aiMgr := ai.NewManager(aiConfigFromConfig(cfg.AI), a.eventBus)
	ah := api.NewAIHandler(aiMgr, a.cfg, a.configPath)
	handler.SetAIHandler(ah)
	// Create and populate StreamRegistry for protocol discovery
	reg := api.NewStreamRegistry()
	reg.Register(&api.HLSStreamHandler{Mgr: a.hlsMgr})
	// Always register LL-HLS so it appears as greyed-out when disabled
	reg.Register(&api.LLHLSStreamHandler{
		HLSStreamHandler:  api.HLSStreamHandler{Mgr: a.hlsMgr},
		LowLatencyEnabled: cfg.HLS.LowLatency,
	})
	if a.webrtcMgr != nil {
		reg.Register(&api.WebRTCStreamHandler{})
	}
	if a.flvMgr != nil {
		reg.Register(&api.FLVStreamHandler{})
	}
	// WebSocket stream handler is always available
	reg.Register(&api.WSStreamHandler{})
	// MJPEG stream handler for JPEG/MJPEG cameras (proxy on-demand)
	reg.Register(&api.MJPEGStreamHandler{})
	handler.SetStreamRegistry(reg)

	// Wire FFmpeg downloader for transcoding status/download APIs
	if a.transcodeMgr != nil {
		handler.SetDownloader(a.transcodeMgr.Downloader())
		handler.SetTranscodeManager(a.transcodeMgr)
	} else {
		// Always provide a downloader so FFmpeg status APIs work even when transcoding is disabled
		handler.SetDownloader(transcoding.NewDownloader(cfg.Storage.RootDir, nil))
	}

	// WebDAV
	var davHandler http.Handler
	if cfg.WebDAV.Enabled != nil && *cfg.WebDAV.Enabled {
		davSrv := webdav.NewServer(a.store, cfg.WebDAV.PathPrefix, a.authMW, a.db, cfg.WebDAV.ReadWrite)
		davHandler = davSrv.Handler()
	}

	// Upload handler
	uploadHandler := upload.NewHandler(a.store, a.db, 100<<20) // 100MB max

	// Register WebDAV methods with chi so it doesn't reject them as 405.
	chi.RegisterMethod("PROPFIND")
	chi.RegisterMethod("MKCOL")
	chi.RegisterMethod("LOCK")
	chi.RegisterMethod("UNLOCK")
	chi.RegisterMethod("COPY")
	chi.RegisterMethod("MOVE")

	r := chi.NewRouter()
	r.Use(authmw.RequestLogger(slog.Default(), "/api/health", "/api/readyz"))
	r.Use(middleware.Recoverer)
	r.Use(authmw.SecurityHeaders)
	r.Use(authmw.COOPHeaders)


	// Prometheus metrics — independent auth when configured, public otherwise
	if cfg.MetricsAuth.IsConfigured() {
		metricsAuthMW, _ := authmw.NewAuthMiddleware(authmw.AuthProvider{
			GetUsername: func() string { return cfg.MetricsAuth.Username },
			GetHash:     func() string { return cfg.MetricsAuth.PasswordHash },
		}, cfg.MetricsAuth.Password, authmw.AuthRateLimitConfig{})
		r.With(metricsAuthMW).Handle("/metrics", promhttp.HandlerFor(a.metrics.Registry, promhttp.HandlerOpts{ErrorHandling: promhttp.ContinueOnError}))
	} else {
		r.Handle("/metrics", promhttp.HandlerFor(a.metrics.Registry, promhttp.HandlerOpts{ErrorHandling: promhttp.ContinueOnError}))
	}

	r.Mount("/", handler.Routes())

	// WebDAV
	if davHandler != nil {
		r.Mount(cfg.WebDAV.PathPrefix, davHandler)
	}

	// Upload routes (authenticated)
	r.Group(func(r chi.Router) {
		r.Use(a.authMW)
		uploadHandler.RegisterRoutes(r)
	})

	// Static UI — serve from embedded filesystem
	staticContent, err := fs.Sub(ui.StaticFS, "static")
	if err != nil {
		slog.Error("static fs", "error", err)
		os.Exit(1)
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

	return r
}

// Start launches all service goroutines and blocks until a shutdown signal
// is received or the context is cancelled.
func (a *App) Start() error {
	ctx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel

	// Start camera manager
	go func() {
		if err := a.camMgr.Start(ctx); err != nil {
			slog.Error("camera manager", "error", err)
		}
	}()

	// Start health manager (optional, after camera manager)
	if a.healthMgr != nil {
		if err := a.healthMgr.Start(ctx); err != nil {
			slog.Error("health manager", "error", err)
		}
	}

	// Start merge manager
	go func() {
		if a.cfg.Merge.Enabled {
			a.mergeMgr.Run(ctx)
			slog.Info("merge-manager stopped")
		}
	}()

	// Start transcode manager (optional)
	if a.transcodeMgr != nil {
		go a.transcodeMgr.Run(ctx)
	}

	// Start merge scheduler for timelapse cameras.
	if a.mergeScheduler != nil {
		a.mergeScheduler.Start(ctx)
	}

	// Start cleanup manager
	go a.cleanupMgr.Run(ctx)

	// Start MQTT client (optional)
	if a.mqttClient != nil {
		go func() {
			if err := a.mqttClient.Start(ctx); err != nil {
				slog.Error("mqtt", "error", err)
			}
		}()
	}

	// Start FTP server (optional)
	if a.ftpServer != nil {
		go func() {
			if err := a.ftpServer.Start(ctx); err != nil {
				slog.Error("ftp", "error", err)
			}
		}()
	}

	// Start RTMP server (optional)
	if a.rtmpServer != nil {
		go func() {
			if err := a.rtmpServer.Start(ctx); err != nil {
				slog.Error("rtmp", "error", err)
			}
		}()
	}

	// Start SRT listener (optional)
	if a.srtListener != nil {
		go func() {
			if err := a.srtListener.Start(); err != nil {
				slog.Error("srt", "error", err)
			}
		}()
		// Start caller-mode SRT streams that connect outbound to remote sources
		if err := a.srtListener.StartCallers(); err != nil {
			slog.Error("srt callers", "error", err)
		}
	}

	// Start HTTP server
	go func() {
		slog.Info("MiBee NVR listening", "version", appVersion, "addr", a.cfg.Server.Listen)
		if err := a.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("http", "error", err)
			os.Exit(1)
		}
	}()

	// Wait for shutdown signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	slog.Info("received signal, shutting down", "signal", sig.String())

	return a.Stop()
}


// Stop gracefully shuts down all components in reverse dependency order
// with a 30-second timeout. Shutdown order:
//
	//	1. HTTP server — stop accepting new requests
//	2. FTP server — close listener
//	3. MQTT client — disconnect from broker
//	4. WebDAV — handled via HTTP server shutdown
//	5. Cleanup manager — stopped via context cancellation
//	6. Merge manager — stopped via context cancellation
//	7. HLS manager — stop all active streams
//	8. Camera manager — stop all recorders
//	9. Storage (DB) — close connection
func (a *App) Stop() error {
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	done := make(chan struct{})
	go func() {
		// Cancel context to signal all goroutines (FTP, cleanup, merge, MQTT)
		if a.cancel != nil {
			a.cancel()
		}

		log := authmw.ComponentLogger("server")
		log.Info("shutting down...")

		// 0. Remote log handler — flush remaining logs
		if a.remoteLogHandler != nil {
			log.Info("flushing remote log handler")
			a.remoteLogHandler.Close()
		}

		// 1. HTTP server — stop accepting new requests
		log.Info("stopping HTTP server")
		if err := a.httpServer.Shutdown(shutdownCtx); err != nil {
			log.Warn("HTTP server shutdown error", "error", err)
		}

		// 2. FTP server
		if a.ftpServer != nil {
			log.Info("stopping FTP server")
			a.ftpServer.Close()
	}

	// 3. WebRTC manager
	if a.webrtcMgr != nil {
		log.Info("stopping WebRTC manager")
		a.webrtcMgr.StopAll()
	}

	// 4. WebSocket stream manager
	if a.wsMgr != nil {
		log.Info("stopping WebSocket stream manager")
		a.wsMgr.StopAll()
	}

	// 4. RTMP server
	if a.rtmpServer != nil {
		log.Info("stopping RTMP server")
		_ = a.rtmpServer.Stop()
	}

	// 5. SRT listener
	if a.srtListener != nil {
		log.Info("stopping SRT listener")
		_ = a.srtListener.Stop()
	}

	// 6. MQTT client
		if a.mqttClient != nil {
			log.Info("stopping MQTT client")
			if err := a.mqttClient.Stop(); err != nil {
				log.Warn("MQTT stop error", "error", err)
			}
		}

		// 4. WebDAV — no explicit stop needed (handler served by HTTP server)

		// 5. Cleanup manager — stopped via context cancellation above
		log.Info("cleanup manager stopped")

		// 6. Merge manager — stopped via context cancellation above
		log.Info("merge manager stopped")

		// 6.1. Merge scheduler
		if a.mergeScheduler != nil {
			log.Info("stopping merge scheduler")
			a.mergeScheduler.Stop()
		}
		log.Info("merge scheduler stopped")

		// 7. HLS manager
		log.Info("stopping HLS streams")
		a.hlsMgr.StopAll()

		// 7.5. Health manager (before camera manager)
		if a.healthMgr != nil {
			log.Info("stopping health manager")
			a.healthMgr.Stop()
		}

		// 7.8. Transcode manager
		if a.transcodeMgr != nil {
			log.Info("stopping transcode manager")
			a.transcodeMgr.Stop()
		}


		log.Info("stopping camera manager")
		if err := a.camMgr.Stop(); err != nil {
			log.Warn("camera manager stop error", "error", err)
		}

		// 9. Storage (DB)
		log.Info("closing database")
		a.db.Close()

		close(done)
	}()

	select {
	case <-done:
		authmw.ComponentLogger("server").Info("shutdown complete")
	case <-shutdownCtx.Done():
		authmw.ComponentLogger("server").Warn("shutdown timed out, forcing exit")
	}

	slog.Info("MiBee NVR stopped")
	return nil
}

// ---------------------------------------------------------------------------
// main
// ---------------------------------------------------------------------------

func main() {
	// Dispatch CLI subcommands before flag parsing
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "health":
			cmdHealth()
		case "init":
			cmdInit()
		case "hash-password":
			cmdHashPassword()
		case "encrypt-config":
	case "download-model":
		cmdDownloadModel()
			cmdEncryptConfig()
		}
	}

	// Setup initial logger before config load
	logger := authmw.SetupLogger("info", "text")
	slog.SetDefault(logger)

	flag.Parse()

	if *version {
		fmt.Printf("MiBee NVR version %s\n", appVersion)
		os.Exit(0)
	}

	// Load and validate config
	cfg, err := config.Load(*configPath)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Error("config", "error", err)
			os.Exit(1)
		}
		// Auto-initialize: config file not found, generate defaults
		slog.Info("config file not found, auto-initializing with defaults", "path", *configPath)
		cfg = autoInitConfig(*configPath)
	}
	// Fix Docker storage path mismatch: if running in Docker but config has
	// the non-Docker default /var/lib/mibee-nvr, auto-fix to /data.
	if dockerDir := dockerStorageDir(); dockerDir != "" {
		if cfg.Storage.RootDir == "/var/lib/mibee-nvr" || cfg.Storage.RootDir == "" {
			slog.Warn("auto-fixing storage.root_dir for Docker environment",
				"old", cfg.Storage.RootDir, "new", dockerDir)
			cfg.Storage.RootDir = dockerDir
			if err := config.Save(*configPath, cfg); err != nil {
				slog.Warn("failed to save auto-fixed config", "error", err)
			}
		}
	}

	if err := config.Validate(cfg); err != nil {
		slog.Error("config validation", "error", err)
		os.Exit(1)
	}

	// Reconfigure logger with user settings after config load
	logger = authmw.SetupLogger(cfg.Observability.LogLevel, cfg.Observability.LogFormat)
	slog.SetDefault(logger)

	app, err := NewApp(cfg, *configPath)
	if err != nil {
		slog.Error("init", "error", err)
		os.Exit(1)
	}

	if err := app.Start(); err != nil {
		slog.Error("run", "error", err)
		os.Exit(1)
	}
}
