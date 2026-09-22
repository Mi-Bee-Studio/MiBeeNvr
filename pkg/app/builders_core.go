package app

// builders_core.go — buildAppDeps phase 1: device identity, storage root,
// database, metrics, I/O budgets, event bus, remote log, storage manager,
// migration, startup background goroutines and the auth middleware.

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/iobudget"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/memlimit"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/merge"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	authmw "github.com/Mi-Bee-Studio/MiBeeNvr/internal/middleware"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/middleware/remotelog"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/migration"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/motion"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/recorder"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/slogx"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/timelapse"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/transcoding"
)

// memlimitAppliedFn reads the applied GOMEMLIMIT for the metrics gauge —
// seam for wiring tests (#756).
var memlimitAppliedFn = memlimit.Applied

// buildCoreDeps mutates deps with the core infrastructure (db, metrics,
// budgets, event bus, storage, migration, auth). Error paths close what was
// already opened; no startup-bg goroutines run yet unless deps.startupBgCancel
// is set.
func buildCoreDeps(deps *appDeps) error {
	cfg, configPath := deps.cfg, deps.configPath
	ctx := context.Background()

	// Step -1: Stable device identity (#330) — generate + persist the
	// device_id on first startup so LAN clients can anchor on an ID instead
	// of an IP. Best-effort: a read-only config keeps the in-memory ID.
	if err := config.EnsureDeviceIdentity(configPath, cfg); err != nil {
		slog.Warn("failed to persist device identity", "path", configPath, "error", err)
	}

	// Step 0: Ensure storage root directory exists. Inside containers an
	// un-creatable root_dir (stale host path left by an uninstall-keep-data
	// reinstall, #434) falls back to the data volume (in-memory only) instead
	// of aborting startup into a restart loop. The DB is decoupled from the
	// root (dbpath.go), so this only gates where recordings land.
	if err := ensureStorageRoot(cfg, config.DockerDataDir()); err != nil {
		return err
	}

	// Step 1: Open database (decoupled from the recording root; adopts a
	// legacy root-bound DB once, see dbpath.go).
	db, err := openDatabase(cfg, configPath)
	if err != nil {
		return err
	}
	deps.db = db

	// Step 2: Metrics
	m := metrics.NewMetrics()
	deps.metrics = m

	// Wire DB observability hooks: query-latency histogram + SQLITE_BUSY counter.
	db.SetMetrics(m)
	storage.SetBusyErrorHook(m.IncSQLiteBusyErrors)

	// Publish the GOMEMLIMIT applied by main.go (#756) — builders run after
	// applyMemoryLimit, so the recorded value is final. Seam for wiring tests.
	m.MemorySoftLimitBytes.Set(float64(memlimitAppliedFn()))
	// Step 2.15: Shared background I/O budget (#751) — merge, cleanup and
	// timelapse extraction pace their bulk I/O against one process-wide
	// token bucket so foreground work (recording writes, API file serving,
	// SQLite) keeps its latency on busy media. Off by default
	// (io.budget_bytes_per_sec: 0 → nil bucket, every consumer's fast path
	// is a nil check). Cleanup receives it at Step 8 (manager construction);
	// merge/timelapse are package-level setters applied here, before any of
	// their managers exist.
	ioBudget := iobudget.New(cfg.IO.BudgetBytesPerSec, cfg.IO.BudgetBurstBytes,
		iobudget.WithObservers(
			func(consumer string, d time.Duration) {
				m.IOBudgetWaitSecondsTotal.WithLabelValues(consumer).Add(d.Seconds())
			},
			func(consumer string, n int64) {
				m.IOBudgetChargedBytesTotal.WithLabelValues(consumer).Add(float64(n))
			},
		))
	merge.SetIOBudget(ioBudget)
	timelapse.SetIOBudget(ioBudget)
	transcoding.SetIOBudget(ioBudget)
	// Foreground budgeting is opt-in per path (#886 gray-release): recording
	// writes join as the "recording" tenant only when explicitly configured —
	// pacing the reliability-critical recorder can drop frames under a tight
	// budget, so it stays unthrottled by default.
	if ioBudget != nil && cfg.IO.RecordingWritesBudgeted {
		recorder.SetWriteBudget(ioBudget)
	}
	deps.ioBudget = ioBudget
	if ioBudget != nil {
		slog.Info("background I/O budget enabled",
			"bytes_per_sec", cfg.IO.BudgetBytesPerSec, "burst_bytes", cfg.IO.BudgetBurstBytes)
	}

	// Unlink guardrail (#755): per-FILE rate limit for recursive frame-tree
	// deletion, active only with the byte budget (replaces the fixed
	// 200-files/200ms time-slice so fast media sprints and busy media backs
	// off). Default tier 200 unlink/s per the #748 jbd2-saturation lesson.
	if ioBudget != nil {
		unlinkRate := cfg.IO.DeleteUnlinksPerSec // default 200 applied by config defaults
		deps.unlinkBudget = iobudget.New(unlinkRate, unlinkRate,
			iobudget.WithObservers(
				func(consumer string, d time.Duration) {
					m.IOBudgetWaitSecondsTotal.WithLabelValues(consumer).Add(d.Seconds())
				},
				func(consumer string, n int64) {
					m.IOBudgetChargedUnlinksTotal.WithLabelValues(consumer).Add(float64(n))
				},
			))
	}

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
		// Wrap slog.Default() with multi-handler to fan out to both stdout and
		// remote. slogx.SetDefault (not a bare slog.SetDefault): the swap
		// rewrites stdlib log output, which would silently discard an armed
		// stdlib-log throttle (#813).
		if current := slog.Default(); current.Handler() != nil {
			slogx.SetDefault(slog.New(remotelog.MultiHandler(current.Handler(), rh)))
		} else {
			slogx.SetDefault(slog.New(rh))
		}
	}

	// Step 3: Storage manager
	store, err := storage.NewManager(cfg.Storage.RootDir, m)
	if err != nil {
		db.Close()
		return fmt.Errorf("storage: %w", err)
	}
	// Raw-segment durability tier (#760): strict (default) or relaxed
	// (skip proactive fsync on raw segments; merge products always sync).
	store.SetDurability(cfg.Storage.Durability)
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
	// LocalBypass is opt-in (default false): only bare-metal installs that open
	// http://localhost on the host itself should enable it. Reverse-proxy and
	// Docker published-port deployments must leave it off — there every proxied
	// request arrives from loopback and would otherwise bypass auth entirely.
	// The closure reads cfg on every call (like GetUsername/GetHash) so a future
	// runtime toggle of local_bypass takes effect immediately; freezing the bool
	// at startup would keep the bypass active after a disable — fail-open.
	authMW, effectiveHash := authmw.NewAuthMiddleware(authmw.AuthProvider{
		GetUsername: func() string { return cfg.Auth.Username },
		GetHash:     func() string { return cfg.Auth.PasswordHash },
		LocalBypass: func() bool { return cfg.Auth.LocalBypass != nil && *cfg.Auth.LocalBypass },
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

	return nil
}
