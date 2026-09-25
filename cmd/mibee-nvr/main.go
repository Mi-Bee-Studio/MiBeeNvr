package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/api"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/install"
	authmw "github.com/Mi-Bee-Studio/MiBeeNvr/internal/middleware"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/slogx"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/tray"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/ui"
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
			dataDir = config.DefaultDataDir
		}
	}

	password := os.Getenv("NVR_PASSWORD")

	cfg := &config.Config{
		Server:        config.ServerConfig{Listen: config.DefaultListenAddr},
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
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		slog.Warn("failed to create data directory", "dir", dataDir, "error", err)
	}

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
	// Windows GUI-subsystem binary: reattach to the launching terminal so
	// CLI output stays visible (no-op on other platforms and on desktop
	// launches, where staying console-less is the point).
	if runtime.GOOS == "windows" {
		install.EnsureParentConsole()
	}

	// Dispatch CLI subcommands before flag parsing
	dispatchSubcommand(os.Args)

	// Setup initial logger before config load. slogx.SetDefault (not a bare
	// slog.SetDefault) re-arms the stdlib-log throttle on every swap — a bare
	// swap rewrites log output and would silently discard it (#813).
	logger := authmw.SetupLogger("info", "text")
	slogx.SetDefault(logger)

	flag.Parse()

	if *version {
		fmt.Printf("MiBee NVR version %s\n", appVersion)
		os.Exit(0)
	}

	// Bare desktop runs of the INSTALLED binary resolve the default config
	// to the installed one — otherwise a double-click would auto-init a
	// stray config next to the exe. Explicit -config always wins.
	configExplicit := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "config" {
			configExplicit = true
		}
	})
	if !configExplicit {
		if p := desktopInstalledConfig(); p != "" {
			*configPath = p
		}
	}

	// Load and validate config. A config that fails to load or validate is
	// first retried from the ".last-good" snapshot (written after every
	// successful boot below) — a bad runtime write or hand edit must not
	// turn into a crash loop (#867: an fnOS container restart-looped twice
	// on disk_threshold_percent=20 because #737/#738 shipped only the
	// snapshot half of this recovery point).
	cfg, err := config.Load(*configPath)
	if err != nil {
		if !os.IsNotExist(err) {
			if restored, rerr := config.RestoreLastGood(*configPath); rerr == nil {
				slog.Warn("config failed to load — restored last-good snapshot; the rejected file is kept alongside as .bad", "load_error", err)
				cfg = restored
			} else {
				slog.Error("config", "error", err)
				os.Exit(1)
			}
		} else {
			// Auto-initialize: config file not found, generate defaults
			slog.Info("config file not found, auto-initializing with defaults", "path", *configPath)
			cfg = autoInitConfig(*configPath)
		}
	}

	// Fix Docker storage path mismatch: if running in Docker but config has
	// the non-Docker default /var/lib/mibee-nvr, auto-fix to /data.
	if dockerDir := config.DockerDataDir(); dockerDir != "" {
		if cfg.Storage.RootDir == config.DefaultDataDir || cfg.Storage.RootDir == "" {
			slog.Warn("auto-fixing storage.root_dir for Docker environment",
				"old", cfg.Storage.RootDir, "new", dockerDir)
			cfg.Storage.RootDir = dockerDir
			if err := config.Save(*configPath, cfg); err != nil {
				slog.Warn("failed to save auto-fixed config", "error", err)
			}
		}
	}

	if err := config.Validate(cfg); err != nil {
		if restored, rerr := config.RestoreLastGood(*configPath); rerr == nil {
			slog.Warn("config failed validation — restored last-good snapshot; the rejected file is kept alongside as .bad", "validation_error", err)
			cfg = restored
		} else {
			slog.Error("config validation", "error", err)
			os.Exit(1)
		}
	}

	// Snapshot the boot-validated config as the recovery point for
	// shutdown-persist overwrites (running NVR re-serializes the YAML at
	// shutdown, clobbering hand edits; older binaries zero unknown sections).
	if err := config.WriteLastGoodBackup(*configPath); err != nil {
		slog.Warn("failed to write last-good config snapshot", "error", err)
	}

	// Reconfigure logger with user settings after config load. The throttle
	// interval is config-driven (observability.stdlog_throttle, default 10s,
	// "off"/"0s" disables) — arming here, after the final logger swap, keeps
	// it effective for the whole runtime.
	logger = authmw.SetupLogger(cfg.Observability.LogLevel, cfg.Observability.LogFormat)

	// Desktop windows server mode runs with a HIDDEN console (the tray is
	// the UI — a lingering cmd window reads as a stuck program). Stdout
	// would be lost with it, so tee the logs into the data dir first
	// (darwin's LaunchAgent already redirects to nvr.log; servers log to
	// journald/containers untouched).
	if runtime.GOOS == "windows" {
		if lw := openDesktopLogWriter(*configPath); lw != nil {
			logger = authmw.SetupLoggerWriter(cfg.Observability.LogLevel, cfg.Observability.LogFormat, lw)
			slog.Info("desktop log tee active", "file", filepath.Join(filepath.Dir(*configPath), "nvr.log"))
		}
	}
	slogx.SetDefault(logger)
	slogx.InstallStdLogThrottle(cfg.Observability.StdlogThrottleDuration())
	if runtime.GOOS == "windows" {
		install.HideOwnConsole()
	}

	// Process memory self-discipline (#756): conservative GOMEMLIMIT before
	// any manager starts allocating — env GOMEMLIMIT wins natively.
	applyMemoryLimit(cfg)

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

	// In-app version check (sensing layer). Polls GitHub Releases with ETag
	// conditional requests (304s do not count against the unauth rate limit)
	// and exposes the result at /api/update/check.
	if cfg.Update.IsEnabled() {
		interval, err := time.ParseDuration(cfg.Update.CheckInterval)
		if err != nil || interval < time.Minute {
			interval = time.Hour
		}
		upd := update.New(appVersion, cfg.Update.Repo, cfg.Update.Channel, interval)
		// Opt-in bare-metal auto-apply (#647): on the first sighting of a newer
		// stable release, hand off to the root helper unit (polkit-authorized).
		// Best-effort — trigger failures are logged, never fatal.
		if cfg.Update.IsAutoApply() {
			upd.SetOnAvailable(func(st update.Status) {
				if err := update.TriggerAutoApply(appVersion, st, update.Deployment(),
					cfg.Storage.RootDir, update.StartHelperUnit); err != nil {
					slog.Warn("update: auto-apply trigger failed", "error", err)
				}
			})
		}
		upd.Start(ctx)
		defer upd.Stop()
		api.SetUpdateChecker(upd)
	}

	// Loopback-local shutdown endpoint (macOS menu-bar helper is a separate
	// process and cannot close the in-process tray quit channel).
	apiShutdown := make(chan struct{})
	api.SetShutdownFunc(func() { close(apiShutdown) })

	httpSrv := a.Value("http-server").(*http.Server)
	apiHandler := httpSrv.Handler
	// Unblocks SSE handler loops the moment Shutdown begins — without this a
	// quit with the web UI open stalls on the never-idle /api/events stream.
	httpSrv.RegisterOnShutdown(api.CloseStreams)
	// Bind the listener explicitly (not ListenAndServe) so a later listen
	// swap can retire and rebuild just this listener (see applyListenAddr).
	httpLn := mustListenTCP(cfg.Server.Listen)
	go func() {
		slog.Info("MiBee NVR listening", "version", appVersion, "spa_build", ui.SPABuildInfo(), "addr", cfg.Server.Listen)
		if err := httpSrv.Serve(httpLn); err != nil && err != http.ErrServerClosed {
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
		tlsSrv.RegisterOnShutdown(api.CloseStreams)
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

	// HTTP(S): SSE/streaming connections never go idle, so Shutdown alone
	// stalls until the context deadline (a tray quit with the web UI open
	// used to take the full 30s). CloseStreams (fired by RegisterOnShutdown
	// on every server below) unblocks the SSE loops; the short window + hard
	// Close covers any remaining long-lived connection (e.g. FLV viewers).
	httpShutdown := func(srv *http.Server, name string) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			slog.Warn(name+" shutdown exceeded graceful window, closing", "error", err)
			if cerr := srv.Close(); cerr != nil {
				slog.Warn(name+" close", "error", cerr)
			}
		}
	}

	// applyListenAddr moves the plain-HTTP listener (tray 监听地址… menu /
	// PUT /api/system/listen): bind the NEW address first so a bad address
	// never takes the running server down, persist the config, drain the old
	// server, then serve on the new listener. TLS and the gateway socket
	// keep their own configured addresses.
	var listenMu sync.Mutex
	applyListenAddr := func(addr string) error {
		listenMu.Lock()
		defer listenMu.Unlock()

		addr = strings.TrimSpace(addr)
		if addr == cfg.Server.Listen {
			return nil
		}
		newLn, lerr := net.Listen("tcp", addr)
		if lerr != nil {
			return fmt.Errorf("无法监听 %s（地址无效或端口被占用）：%w", addr, lerr)
		}
		old, oldAddr := httpSrv, cfg.Server.Listen
		cfg.Server.Listen = addr
		if serr := config.Save(*configPath, cfg); serr != nil {
			cfg.Server.Listen = oldAddr
			_ = newLn.Close()
			return fmt.Errorf("保存配置失败：%w", serr)
		}

		// Drain the old server BEFORE arming the new one: its OnShutdown hook
		// re-fires CloseStreams (a no-op on the consumed Once), and
		// ResetStreams must only re-arm afterwards — otherwise the old
		// server's shutdown would close the new era's signal too.
		api.CloseStreams()
		httpShutdown(old, "http (listen swap)")
		api.ResetStreams()

		httpSrv = &http.Server{
			Handler:           apiHandler,
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       120 * time.Second,
			// WriteTimeout intentionally not set (SSE / streaming), matching
			// the boot server built in pkg/app.
		}
		httpSrv.RegisterOnShutdown(api.CloseStreams)
		go func() {
			slog.Info("MiBee NVR listening (rebind)", "addr", addr)
			if err := httpSrv.Serve(newLn); err != nil && err != http.ErrServerClosed {
				// Log-only: the tray can rebind again; killing the process
				// would take the whole NVR down over a listener hiccup.
				slog.Error("http serve (rebind)", "error", err)
			}
		}()

		tray.SetAddress(addr)
		// macOS: the menu-bar helper's base URL is baked in at compile time —
		// recompile + restart it so it follows the new address (no-op
		// elsewhere, and when the helper was never installed).
		if rerr := install.RefreshMenuBarHelper(); rerr != nil {
			slog.Warn("menu-bar helper refresh after listen change failed", "error", rerr)
		}
		slog.Info("listen address changed", "from", oldAddr, "to", addr)
		return nil
	}
	api.SetListenChangeFunc(applyListenAddr)

	// Desktop tray (windows builds; no-op elsewhere): without it a desktop
	// run has no discoverable entry to reach the UI or stop the server.
	trayStop, trayQuit := tray.Start(tray.Options{
		Tooltip:        "MiBee NVR " + appVersion,
		OpenURL:        tray.ListenURL(cfg.Server.Listen),
		Version:        appVersion,
		ListenAddr:     cfg.Server.Listen,
		OnChangeListen: applyListenAddr,
	})
	defer trayStop()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	select {
	case sig := <-sigCh:
		slog.Info("received signal, shutting down", "signal", sig.String())
	case <-trayQuit:
		slog.Info("tray quit requested, shutting down")
	case <-apiShutdown:
		slog.Info("local shutdown request received, shutting down")
	}

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
	// HTTP(S): SSE/streaming connections never go idle — httpShutdown (with
	// the CloseStreams hook registered on every server above) is defined
	// before the listen-swap closure and reused here.
	if tlsSrv != nil {
		httpShutdown(tlsSrv, "https")
	}
	httpShutdown(httpSrv, "http")
	if err := a.Stop(); err != nil {
		slog.Error("stop", "error", err)
	}
	// Desktop agents run with KeepAlive — detach from the supervisor before
	// exiting or launchd resurrects the process and a deliberate quit never
	// sticks (menu-bar "退出" field report 2026-09-20). No-op on servers.
	install.UnloadDesktopAgents()
	slog.Info("MiBee NVR stopped")
}

// openDesktopLogWriter tees server logs into <config dir>/nvr.log (10MB
// rotated to nvr.log.old) next to the desktop config — the hidden console
// makes stdout unreadable, and on-machine forensics (the 2026-09-19
// uninstaller incident) need a persistent record. Nil = keep stdout only.
func openDesktopLogWriter(configPath string) io.Writer {
	logPath := filepath.Join(filepath.Dir(configPath), "nvr.log")
	if fi, err := os.Stat(logPath); err == nil && fi.Size() > 10*1024*1024 {
		_ = os.Remove(logPath + ".old")
		_ = os.Rename(logPath, logPath+".old")
	}
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil
	}
	// f stays open for the process lifetime (owned by the logger).
	return io.MultiWriter(os.Stdout, f)
}

// mustListenTCP binds the main HTTP listener. Kept as a standalone function
// (with its own os.Exit calls) so gocritic's exitAfterDefer stays quiet in
// main — the same treatment as listenGatewaySocket: a fatal init error where
// main's defers are moot anyway.
func mustListenTCP(addr string) net.Listener {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		slog.Error("http listen", "addr", addr, "error", err)
		os.Exit(1)
	}
	return ln
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
