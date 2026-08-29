package main

// repair.go — `mibee-nvr repair` CLI: on-demand data repair for operational issues
// that don't belong in the hot path of the long-running server.
//
// Subcommands:
//   repair duration     — re-probe video files to fix recordings stuck at duration=0
//   repair merge-status — reset merge_status for recordings whose merged file is missing
//
// Both mirror the migrate-mjpeg CLI shape (--dry-run default, --execute to apply,
// --config / --camera / --limit). They open their own DB connection from the config
// and are safe to run while the server is stopped (preferred) or running (WAL mode
// allows concurrent readers, but prefer stopping the server for large repairs).

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)

func cmdRepair() {
	os.Exit(runRepair())
}

func runRepair() int {
	if len(os.Args) < 3 {
		printRepairUsage()
		return 1
	}
	sub := os.Args[2]
	switch sub {
	case "duration":
		return runRepairDuration()
	case "merge-status":
		return runRepairMergeStatus()
	case "fragments":
		return runRepairFragments()
	case "delete-by-format":
		return runRepairDeleteByFormat()
	case "prune-intermediate-mp4":
		return runRepairPruneIntermediateMP4()
	case "reclaim-orphan-merges":
		return runRepairReclaimOrphanMerges()
	case "normalize-endpoints":
		return runRepairNormalizeEndpoints()
	case "--help", "-h":
		printRepairUsage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "Unknown repair subcommand: %s\n\n", sub)
		printRepairUsage()
		return 1
	}
}

// repairOpts holds shared CLI flags parsed from os.Args.

// repairOpts holds shared CLI flags parsed from os.Args.
type repairOpts struct {
	configPath string
	cameraID   string
	limit      int
	dryRun     bool
	prune      bool // delete DB row + file for recordings that can't be repaired
	// delete-by-format specific
	keepFormat string        // format value to PRESERVE (e.g. "timelapse"); all other formats are deleted
	olderThan  time.Duration // 0 = no age filter (delete all matching regardless of age)
	// prune-intermediate-mp4 specific
	before string // YYYY-MM-DD — only act on recordings started before this date (UTC)
}

func parseRepairFlags(startIdx int) repairOpts {
	opts := repairOpts{
		configPath: "mibee-nvr.yaml",
		limit:      0,
		dryRun:     true, // safe default
	}
	for i := startIdx; i < len(os.Args); i++ {
		arg := os.Args[i]
		switch {
		case arg == "--execute":
			opts.dryRun = false
		case arg == "--dry-run":
			opts.dryRun = true
		case arg == "--prune":
			opts.prune = true
		case arg == "--config" && i+1 < len(os.Args):
			i++
			opts.configPath = os.Args[i]
		case arg == "--camera" && i+1 < len(os.Args):
			i++
			opts.cameraID = os.Args[i]
		case arg == "--limit" && i+1 < len(os.Args):
			i++
			if n, err := parseInt(os.Args[i]); err == nil && n >= 0 {
				opts.limit = n
			}
		case arg == "--keep-format" && i+1 < len(os.Args):
			i++
			opts.keepFormat = os.Args[i]
		case arg == "--older-than" && i+1 < len(os.Args):
			i++
			d, err := time.ParseDuration(os.Args[i])
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: invalid --older-than %q (use Go duration like 24h, 7d is not supported, use 168h): %v\n", os.Args[i], err)
				os.Exit(1)
			}
			opts.olderThan = d
		case arg == "--before" && i+1 < len(os.Args):
			i++
			opts.before = os.Args[i]
		case arg == "--help", arg == "-h":
			opts.configPath = "__help__"
		}
	}
	return opts
}

// openDBFromConfig loads the config and opens the recordings DB (same pattern as
// migrate-mjpeg). Returns nil db on error (caller prints and exits).

// openDBFromConfig loads the config and opens the recordings DB (same pattern as
// migrate-mjpeg). Returns nil db on error (caller prints and exits).
func openDBFromConfig(cfgPath string) (*storage.DB, *config.Config, error) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, nil, fmt.Errorf("load config %q: %w", cfgPath, err)
	}
	if err := config.Validate(cfg); err != nil {
		return nil, nil, fmt.Errorf("config validation: %w", err)
	}
	dbPath := cfg.Storage.RootDir + "/mibee-nvr.db"
	db, err := storage.New(dbPath)
	if err != nil {
		return nil, nil, fmt.Errorf("open database %q: %w", dbPath, err)
	}
	ctx := context.Background()
	if err := db.Init(ctx); err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("init database: %w", err)
	}
	return db, cfg, nil
}

// setupSignalHandler returns a context that is cancelled on SIGINT/SIGTERM,
// so repair loops can finish the current item and exit cleanly.

// setupSignalHandler returns a context that is cancelled on SIGINT/SIGTERM,
// so repair loops can finish the current item and exit cleanly.
func setupSignalHandler() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Fprintln(os.Stderr, "\nInterrupt received, finishing current item...")
		cancel()
	}()
	return ctx, cancel
}

// ---------------------------------------------------------------------------
// repair duration — fix recordings with duration=0
// ---------------------------------------------------------------------------

func printRepairUsage() {
	fmt.Print(`Usage: mibee-nvr repair <subcommand> [options]

On-demand data repair for operational issues. These tools touch the DB directly
and are safe to run while the server is stopped (preferred) or running (WAL mode
allows concurrent access).

Subcommands:
  duration               Fix recordings stuck at duration=0 by re-probing video files
  merge-status           Reset merge_status for recordings whose merged file is missing
  fragments              Clean up segments the merge engine gave up on (incompatible/failed)
  delete-by-format       Bulk-delete a camera's recordings of all formats EXCEPT one
                         (e.g. delete regular video while keeping every timelapse segment)
  prune-intermediate-mp4 Bulk-remove per-segment rolling-merge .mp4 outputs that have
                         already been folded into a periodic (8h/24h/7d/30d) merge output
  reclaim-orphan-merges Remove merged-output .mp4 files left on disk after their recording
                         row was deleted via the web UI (pre-#117 fix leak). Only touches
                         unreferenced .mp4 files; never source segments or frame dirs.
  normalize-endpoints   Canonicalize every camera's onvif_endpoint (elide default :80/:443,
                         lowercase scheme/host, strip trailing slash) so dedup queries match
                         across discovery paths. Fixes legacy rows written before #175.

Common options:
  --dry-run      Report what would change without modifying (default)
  --execute      Actually apply the repair
  --prune        (duration only) Delete DB row + file for unrepairable recordings
  --config <path> Config file path (default: mibee-nvr.yaml)
  --help, -h     Show help

Run 'mibee-nvr repair <subcommand> --help' for subcommand-specific options.
`)
}
