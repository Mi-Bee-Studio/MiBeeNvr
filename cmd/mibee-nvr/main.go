package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	authmw "github.com/Mi-Bee-Studio/MiBeeNvr/internal/middleware"
	"github.com/Mi-Bee-Studio/MiBeeNvr/pkg/app"
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

func main() {
	// Dispatch CLI subcommands before flag parsing
	dispatchSubcommand(os.Args)

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

	a, err := app.RunFree(cfg, *configPath)
	if err != nil {
		slog.Error("init", "error", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := a.Start(ctx); err != nil {
		slog.Error("start", "error", err)
		os.Exit(1)
	}

	httpSrv := a.Value("http-server").(*http.Server)
	go func() {
		slog.Info("MiBee NVR listening", "version", appVersion, "addr", cfg.Server.Listen)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("http", "error", err)
			os.Exit(1)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	slog.Info("received signal, shutting down", "signal", sig.String())

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		slog.Warn("http shutdown", "error", err)
	}
	if err := a.Stop(); err != nil {
		slog.Error("stop", "error", err)
	}
	slog.Info("MiBee NVR stopped")
}
