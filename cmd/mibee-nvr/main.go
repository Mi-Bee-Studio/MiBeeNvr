package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/api"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	authmw "github.com/Mi-Bee-Studio/MiBeeNvr/internal/middleware"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/update"
	"github.com/Mi-Bee-Studio/MiBeeNvr/pkg/app"
)

var (
	configPath = flag.String("config", "mibee-nvr.yaml", "path to configuration file")
	version    = flag.Bool("version", false, "print version and exit")
)

var appVersion = "dev" // overridden via -ldflags -X main.appVersion=... at build time (see Makefile LDFLAGS)

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
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		slog.Warn("failed to create data directory", "dir", dataDir, "error", err)
	}

	// Create config directory if needed
	configDir := filepath.Dir(configPath)
	if configDir != "." && configDir != "/" {
		if err := os.MkdirAll(configDir, 0o755); err != nil {
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
	if dockerDir := config.DockerDataDir(); dockerDir != "" {
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
	if err := a.Start(ctx); err != nil {
		cancel()
		slog.Error("start", "error", err)
		os.Exit(1)
	}
	defer cancel()

	// In-app version check (sensing layer only — never executes an upgrade).
	// Polls GitHub Releases with ETag conditional requests (304s do not count
	// against the unauth rate limit) and exposes the result at /api/update/check.
	if cfg.Update.IsEnabled() {
		interval, err := time.ParseDuration(cfg.Update.CheckInterval)
		if err != nil || interval < time.Minute {
			interval = time.Hour
		}
		upd := update.New(appVersion, cfg.Update.Repo, cfg.Update.Channel, interval)
		upd.Start(ctx)
		defer upd.Stop()
		api.SetUpdateChecker(upd)
	}

	httpSrv := a.Value("http-server").(*http.Server)
	go func() {
		slog.Info("MiBee NVR listening", "version", appVersion, "addr", cfg.Server.Listen)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("http", "error", err)
			os.Exit(1)
		}
	}()

	// Optional HTTPS listener (for WebRTC WHEP / secure WebUI when not behind a
	// TLS-terminating reverse proxy). Shares the same handler as plain HTTP.
	var tlsSrv *http.Server
	if strings.TrimSpace(cfg.Server.TLSListen) != "" {
		tlsSrv = &http.Server{
			Addr:    cfg.Server.TLSListen,
			Handler: httpSrv.Handler,
		}
		go func() {
			slog.Info("MiBee NVR HTTPS listening", "version", appVersion, "addr", cfg.Server.TLSListen,
				"cert", cfg.Server.CertFile)
			if err := tlsSrv.ListenAndServeTLS(cfg.Server.CertFile, cfg.Server.KeyFile); err != nil && err != http.ErrServerClosed {
				slog.Error("https", "error", err)
				os.Exit(1)
			}
		}()
	}

	// Optional Unix-socket listener (fnOS unified gateway, #394). fnOS validates
	// the NAS login session, then forwards authenticated requests to this socket
	// with trusted X-Trim-* user headers. GatewayAuthMiddleware is mounted ONLY
	// here — the TCP listener never trusts those headers. Serving fails hard on
	// error so a broken gateway setup surfaces at start instead of silently
	// degrading into "desktop login never works".
	var gatewaySrv *http.Server
	if sock := strings.TrimSpace(cfg.Server.UnixSocket); sock != "" {
		gatewaySrv = listenGatewaySocket(sock, authmw.GatewayAuthMiddleware(httpSrv.Handler))
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	slog.Info("received signal, shutting down", "signal", sig.String())

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()
	if gatewaySrv != nil {
		if err := gatewaySrv.Shutdown(shutdownCtx); err != nil {
			slog.Warn("gateway socket shutdown", "error", err)
		}
		if sock := strings.TrimSpace(cfg.Server.UnixSocket); sock != "" {
			_ = os.Remove(sock)
		}
	}
	if tlsSrv != nil {
		if err := tlsSrv.Shutdown(shutdownCtx); err != nil {
			slog.Warn("https shutdown", "error", err)
		}
	}
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		slog.Warn("http shutdown", "error", err)
	}
	if err := a.Stop(); err != nil {
		slog.Error("stop", "error", err)
	}
	slog.Info("MiBee NVR stopped")
}

// listenGatewaySocket binds the fnOS unified-gateway Unix socket and starts
// serving handler on it. Kept as a standalone function (with its own os.Exit
// calls) so gocritic's exitAfterDefer stays quiet in main — and because these
// are fatal init errors where main's defers are moot anyway.
func listenGatewaySocket(sock string, handler http.Handler) *http.Server {
	if err := os.MkdirAll(filepath.Dir(sock), 0o755); err != nil {
		slog.Error("gateway socket: mkdir", "dir", filepath.Dir(sock), "error", err)
		os.Exit(1)
	}
	// A stale socket file from an unclean shutdown blocks net.Listen.
	if err := os.Remove(sock); err != nil && !os.IsNotExist(err) {
		slog.Error("gateway socket: remove stale", "path", sock, "error", err)
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		slog.Error("gateway socket: listen", "path", sock, "error", err)
		os.Exit(1)
	}
	// The fnOS gateway service connects to the socket; it lives in a
	// root-owned app directory, so group/other bits stay closed.
	if err := os.Chmod(sock, 0o660); err != nil {
		slog.Warn("gateway socket: chmod", "error", err)
	}
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		// No WriteTimeout: SSE and WebSocket need long-lived connections.
	}
	go func() {
		slog.Info("MiBee NVR gateway socket listening", "path", sock)
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("gateway socket serve", "error", err)
			os.Exit(1)
		}
	}()
	return srv
}
