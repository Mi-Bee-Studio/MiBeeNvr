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
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/mediaprobe"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
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
type repairOpts struct {
	configPath string
	cameraID   string
	limit      int
	dryRun     bool
	prune      bool // delete DB row + file for recordings that can't be repaired
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
		case arg == "--help", arg == "-h":
			opts.configPath = "__help__"
		}
	}
	return opts
}

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

func runRepairDuration() int {
	opts := parseRepairFlags(3)
	if opts.configPath == "__help__" {
		printRepairDurationUsage()
		return 0
	}

	db, _, err := openDBFromConfig(opts.configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}
	defer db.Close()

	ctx, cancel := setupSignalHandler()
	defer cancel()

	mode := "DRY RUN (no changes)"
	if !opts.dryRun {
		mode = "EXECUTE"
	}
	fmt.Printf("repair duration — %s\n", mode)
	if opts.cameraID != "" {
		fmt.Printf("  camera: %s\n", opts.cameraID)
	}
	if opts.limit > 0 {
		fmt.Printf("  limit:  %d\n", opts.limit)
	}
	if opts.prune {
		fmt.Printf("  prune:  yes (unrepairable recordings will be deleted)\n")
	}
	fmt.Println()

	zeroRecs, err := db.ListZeroDurationRecordings(ctx, opts.cameraID, opts.limit)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error listing zero-duration recordings: %v\n", err)
		return 1
	}
	if len(zeroRecs) == 0 {
		fmt.Println("No recordings with duration=0 found. Nothing to repair.")
		return 0
	}

	fmt.Printf("Found %d recordings with duration=0.\n\n", len(zeroRecs))

	fixed := 0
	skipped := 0
	pruned := 0
	failed := 0

	// handleUnrepairable is called when a recording can't be repaired (probe
	// failed or duration too small). With --prune, it deletes the DB row and
	// the file; without --prune, it just skips.
	handleUnrepairable := func(idx int, total int, rec *model.Recording, reason string) {
		if opts.prune {
			fmt.Printf("  [%d/%d] PRUNE %s — %s\n", idx, total, rec.ID, reason)
			if !opts.dryRun {
				// Delete the DB row first, then the file (DB is the source of truth).
				if err := db.DeleteRecording(ctx, rec.ID); err != nil {
					fmt.Fprintf(os.Stderr, "         DB DELETE FAILED: %v\n", err)
					failed++
					return
				}
				// Best-effort file/directory deletion. Ignore errors — the DB row
				// is already gone, and the file may be a directory (MJPEG) or
				// already missing.
				if rec.FilePath != "" {
					_ = os.RemoveAll(rec.FilePath)
				}
			}
			pruned++
		} else {
			fmt.Fprintf(os.Stderr, "  [%d/%d] SKIP %s — %s\n", idx, total, rec.ID, reason)
			skipped++
		}
	}

	for i, rec := range zeroRecs {
		if ctx.Err() != nil {
			break
		}

		// Probe the actual duration. For MP4 files use FastProbeDuration (reads
		// only the stts box — ~100× faster than full ParseSegment for large
		// files). For MJPEG frame directories (ESP32 MiBeeCam), estimate from
		// frame count × a nominal frame interval.
		var dur float64
		if mediaprobe.IsLikelyMP4(rec.FilePath) {
			d, err := mediaprobe.FastProbeDuration(rec.FilePath)
			if err != nil {
				handleUnrepairable(i+1, len(zeroRecs), rec, fmt.Sprintf("probe failed: %v", err))
				continue
			}
			dur = d
		} else {
			// MJPEG frame directory: estimate from file count × interval.
			// ESP32 MiBeeCam captures at ~2 fps with 30s segments; if frame_count
			// is recorded, use it; otherwise count files in the directory.
			d, err := estimateMJpegDirDuration(rec.FilePath, rec.FrameCount)
			if err != nil {
				handleUnrepairable(i+1, len(zeroRecs), rec, fmt.Sprintf("mjpeg estimate failed: %v", err))
				continue
			}
			dur = d
		}
		if dur < 0.1 {
			handleUnrepairable(i+1, len(zeroRecs), rec, fmt.Sprintf("duration too small (%.1fs)", dur))
			continue
		}

		endedAt := rec.StartedAt.Add(time.Duration(dur * float64(time.Second)))
		fmt.Printf("  [%d/%d] %s → %.1fs (ended %s)\n", i+1, len(zeroRecs), rec.ID, dur, endedAt.UTC().Format("15:04:05"))

		if !opts.dryRun {
			if err := db.UpdateRecordingDuration(ctx, rec.ID, dur, endedAt); err != nil {
				fmt.Fprintf(os.Stderr, "         UPDATE FAILED: %v\n", err)
				failed++
				continue
			}
		}
		fixed++

		// Throttle: avoid IO spikes on RPi (each probe reads the MP4 moov box).
		select {
		case <-time.After(100 * time.Millisecond):
		case <-ctx.Done():
		}
	}

	fmt.Println()
	fmt.Printf("Summary: %d fixed, %d skipped, %d pruned, %d failed (of %d total)\n", fixed, skipped, pruned, failed, len(zeroRecs))
	if opts.dryRun && (fixed > 0 || pruned > 0) {
		fmt.Println("Dry run — no changes made. Re-run with --execute to apply.")
	}
	return 0
}

// ---------------------------------------------------------------------------
// repair merge-status — reset stale merge statuses (extracted from startup)
// ---------------------------------------------------------------------------

func runRepairMergeStatus() int {
	opts := repairOpts{
		configPath: "mibee-nvr.yaml",
		dryRun:     true,
	}
	for i := 3; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--execute":
			opts.dryRun = false
		case "--dry-run":
			opts.dryRun = true
		case "--config":
			if i+1 < len(os.Args) {
				i++
				opts.configPath = os.Args[i]
			}
		case "--help", "-h":
			printRepairMergeStatusUsage()
			return 0
		}
	}

	db, cfg, err := openDBFromConfig(opts.configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}
	defer db.Close()

	ctx, cancel := setupSignalHandler()
	defer cancel()

	mode := "DRY RUN (no changes)"
	if !opts.dryRun {
		mode = "EXECUTE"
	}
	fmt.Printf("repair merge-status — %s\n", mode)
	fmt.Println()

	recordings, err := db.ListMergedRecordingsForValidation(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error listing merged recordings: %v\n", err)
		return 1
	}
	if len(recordings) == 0 {
		fmt.Println("No merged recordings to validate.")
		return 0
	}

	fmt.Printf("Checking %d merged recordings for stale/missing files...\n\n", len(recordings))

	staleCount := 0
	for _, rec := range recordings {
		if ctx.Err() != nil {
			break
		}
		if rec.MergePath == "" {
			continue
		}
		info, err := os.Stat(rec.MergePath)
		if err != nil || info.Size() == 0 {
			staleCount++
			reason := "missing"
			if err == nil {
				reason = "empty"
			}
			fmt.Printf("  STALE: %s (camera=%s, %s)\n", rec.ID, rec.CameraID, reason)
			fmt.Printf("         merge_path: %s\n", rec.MergePath)

			if !opts.dryRun {
				if err := db.ResetMergeStatus(ctx, rec.ID); err != nil {
					fmt.Fprintf(os.Stderr, "         RESET FAILED: %v\n", err)
				}
			}
		}
	}

	fmt.Println()
	fmt.Printf("Summary: %d stale / %d checked\n", staleCount, len(recordings))
	if staleCount == 0 {
		fmt.Println("All merged recording files are present. No action needed.")
	}
	if opts.dryRun && staleCount > 0 {
		fmt.Println("Dry run — no changes made. Re-run with --execute to reset stale statuses.")
	}

	// Reference cfg to avoid unused warning if RootDir isn't needed.
	_ = cfg
	return 0
}

// estimateMJpegDirDuration estimates the duration of an MJPEG frame-directory
// recording. ESP32 MiBeeCam stores JPEG frames in a directory (not a single MP4).
// We count .jpg/.jpeg files in the directory and multiply by a nominal frame
// interval. If frame_count is recorded in the DB (non-zero), we use that instead
// of counting files (faster).
//
// The frame interval defaults to 0.5s (2fps — typical for ESP32 MiBeeCam at
// the configured segment duration). This is an estimate, not exact, but far
// better than 0 for timeline display purposes.
const mjpegNominalFrameInterval = 0.5 // seconds per frame (2fps)

func estimateMJpegDirDuration(filePath string, frameCount int) (float64, error) {
	// If frame_count is recorded and > 0, use it (avoids directory scan).
	if frameCount > 0 {
		return float64(frameCount) * mjpegNominalFrameInterval, nil
	}
	// Otherwise count JPEG files in the directory.
	entries, err := os.ReadDir(filePath)
	if err != nil {
		return 0, fmt.Errorf("read mjpeg dir: %w", err)
	}
	count := 0
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() && (hasSuffix(name, ".jpg") || hasSuffix(name, ".jpeg")) {
			count++
		}
	}
	if count == 0 {
		return 0, fmt.Errorf("no jpeg frames in %s", filePath)
	}
	return float64(count) * mjpegNominalFrameInterval, nil
}

func hasSuffix(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}

// ---------------------------------------------------------------------------
// Usage
// ---------------------------------------------------------------------------

func printRepairUsage() {
	fmt.Print(`Usage: mibee-nvr repair <subcommand> [options]

On-demand data repair for operational issues. These tools touch the DB directly
and are safe to run while the server is stopped (preferred) or running (WAL mode
allows concurrent access).

Subcommands:
  duration       Fix recordings stuck at duration=0 by re-probing video files
  merge-status   Reset merge_status for recordings whose merged file is missing

Common options:
  --dry-run      Report what would change without modifying (default)
  --execute      Actually apply the repair
  --prune        (duration only) Delete DB row + file for unrepairable recordings
  --config <path> Config file path (default: mibee-nvr.yaml)
  --help, -h     Show help

Run 'mibee-nvr repair <subcommand> --help' for subcommand-specific options.
`)
}

func printRepairDurationUsage() {
	fmt.Print(`Usage: mibee-nvr repair duration [options]

Fix recordings whose duration is 0 (a historical bug where the recorder failed
to compute ended_at at segment close time). The video files are typically intact,
so this tool re-probes each file's actual duration via pure-Go MP4 box parsing
(mediaprobe — no ffprobe needed) and restores the correct duration + ended_at.

After repair, the timeline will show full coverage for affected days.

Recordings that cannot be repaired (probe fails, file is corrupt/empty, or
probed duration is ~0) are skipped by default. Use --prune to delete their
DB row and the corrupt file instead of leaving them as duration=0 noise.

Options:
  --execute         Actually update the DB (default: dry-run, report only)
  --dry-run         Report only, no changes (default)
  --prune           Delete DB row + file for recordings that can't be repaired
  --camera <id>     Only repair recordings for this camera
  --limit <n>       Max recordings to process (0 = no limit)
  --config <path>   Config file path (default: mibee-nvr.yaml)
  --help, -h        Show this help
`)
}

func printRepairMergeStatusUsage() {
	fmt.Print(`Usage: mibee-nvr repair merge-status [options]

Validate that recordings marked as 'merged' still have their merged output file
on disk. Recordings whose file is missing or empty are reset to unmerged state
so playback falls back to the original segments instead of returning 404.

This was previously run automatically on every server startup; it is now a CLI
tool to avoid the per-boot full-table scan + per-file stat overhead.

Options:
  --execute         Actually reset stale statuses (default: dry-run, report only)
  --dry-run         Report only, no changes (default)
  --config <path>   Config file path (default: mibee-nvr.yaml)
  --help, -h        Show this help
`)
}

// parseInt is a small helper to avoid importing strconv just for one call.
func parseInt(s string) (int, error) {
	var n int
	_, err := fmt.Sscanf(s, "%d", &n)
	return n, err
}
