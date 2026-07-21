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
	case "fragments":
		return runRepairFragments()
	case "delete-by-format":
		return runRepairDeleteByFormat()
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
	// delete-by-format specific
	keepFormat string // format value to PRESERVE (e.g. "timelapse"); all other formats are deleted
	olderThan  time.Duration // 0 = no age filter (delete all matching regardless of age)
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

// ---------------------------------------------------------------------------
// repair fragments — clean up segments the merge engine gave up on
// ---------------------------------------------------------------------------

// deadMergeStatuses are the merge_status values that mean the merge engine has
// permanently given up on a segment. These accumulate as debris that rolling
// merge will never retry (rolling.go marks them "incompatible so we don't retry
// forever"). Live states (pending/merged/merging) must never be matched — the
// CLI refuses them to avoid clobbering in-flight merges.
var deadMergeStatuses = map[string]bool{
	model.MergeStatusIncompatible: true,
	model.MergeStatusFailed:       true,
	model.MergeStatusDark:         true,
}

func runRepairFragments() int {
	opts := parseRepairFlags(3)
	if opts.configPath == "__help__" {
		printRepairFragmentsUsage()
		return 0
	}

	// Parse fragments-specific flags (in addition to the shared flags already
	// parsed by parseRepairFlags: --execute/--dry-run/--camera/--limit/--config).
	var statusStr string
	var retry, forceDelete, resetFakeMerged bool
	var maxDurationSec float64
	for i := 3; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--retry":
			retry = true
		case "--force-delete":
			forceDelete = true
		case "--reset-fake-merged":
			resetFakeMerged = true
		case "--max-duration":
			if i+1 < len(os.Args) {
				i++
				if v, err := parseFloat(os.Args[i]); err == nil && v > 0 {
					maxDurationSec = v
				} else {
					fmt.Fprintf(os.Stderr, "Error: invalid --max-duration %q (must be a positive number of seconds)\n", os.Args[i])
					return 1
				}
			}
		case "--status":
			if i+1 < len(os.Args) {
				i++
				statusStr = os.Args[i]
			}
		case "--help", "-h":
			printRepairFragmentsUsage()
			return 0
		}
	}

	// --reset-fake-merged is a distinct operation mode: it targets segments
	// marked merged but never actually merged (empty merge_path), resetting
	// them to pending so the merge engine re-queues them. It does not use
	// --status/--retry/--force-delete.
	if resetFakeMerged {
		if statusStr != "" || retry || forceDelete {
			fmt.Fprintln(os.Stderr, "Error: --reset-fake-merged cannot be combined with --status/--retry/--force-delete.")
			return 1
		}
		return runRepairFragmentsResetFakeMerged(opts, maxDurationSec)
	}

	// Default status: incompatible (the most common debris). Comma-separated list allowed.
	if statusStr == "" {
		statusStr = model.MergeStatusIncompatible
	}
	statuses := splitCSV(statusStr)
	for _, s := range statuses {
		if !deadMergeStatuses[s] {
			fmt.Fprintf(os.Stderr, "Error: --status %q is not a dead merge status.\n", s)
			fmt.Fprintf(os.Stderr, "Allowed (segments the merge engine gave up on): incompatible, failed, dark.\n")
			fmt.Fprintf(os.Stderr, "Live statuses (pending/merged/merging) are never cleared — they are still being processed.\n")
			return 1
		}
	}
	if retry && forceDelete {
		fmt.Fprintln(os.Stderr, "Error: --retry and --force-delete are mutually exclusive.")
		fmt.Fprintln(os.Stderr, "  --retry         reset to pending (give the merge engine another chance)")
		fmt.Fprintln(os.Stderr, "  --force-delete  delete the DB row + file permanently")
		return 1
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
	action := "report only"
	switch {
	case retry:
		action = "reset to pending (retry merge)"
	case forceDelete:
		action = "delete DB row + file"
	}
	fmt.Printf("repair fragments — %s — %s\n", mode, action)
	fmt.Printf("  status filter: %s\n", statusStr)
	if opts.cameraID != "" {
		fmt.Printf("  camera:        %s\n", opts.cameraID)
	}
	if opts.limit > 0 {
		fmt.Printf("  limit:         %d\n", opts.limit)
	}
	fmt.Println()

	recs, err := db.ListRecordingsByMergeStatus(ctx, statuses, opts.cameraID, opts.limit)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error listing fragments: %v\n", err)
		return 1
	}
	if len(recs) == 0 {
		fmt.Println("No matching fragments found. Nothing to do.")
		return 0
	}

	// Per-camera summary (count / total duration / total size).
	type camStat struct {
		count int
		dur   float64
		size  int64
	}
	byCam := map[string]*camStat{}
	var totalDur float64
	var totalSize int64
	for _, r := range recs {
		s := byCam[r.CameraID]
		if s == nil {
			s = &camStat{}
			byCam[r.CameraID] = s
		}
		s.count++
		s.dur += r.Duration
		s.size += r.FileSize
		totalDur += r.Duration
		totalSize += r.FileSize
	}
	fmt.Printf("Found %d fragments across %d camera(s):\n\n", len(recs), len(byCam))
	for cam, s := range byCam {
		fmt.Printf("  %-40s %4d segments  %7.1f min  %6.2f GB\n", cam, s.count, s.dur/60, float64(s.size)/1024/1024/1024)
	}
	fmt.Printf("  %-40s %4d segments  %7.1f min  %6.2f GB\n", "TOTAL", len(recs), totalDur/60, float64(totalSize)/1024/1024/1024)
	fmt.Println()

	// Dry-run stops here (report only). --execute + (--retry|--force-delete) applies.
	if !opts.dryRun && !retry && !forceDelete {
		fmt.Println("No action specified. Re-run with --retry or --force-delete (and --execute) to act.")
		fmt.Println("Dry run — no changes made.")
		return 0
	}
	if opts.dryRun {
		fmt.Println("Dry run — no changes made. Re-run with --execute (and --retry or --force-delete) to act.")
		return 0
	}

	// --- Apply: retry (reset to pending) ---
	if retry {
		ids := make([]string, len(recs))
		for i, r := range recs {
			ids[i] = r.ID
		}
		// Chunk the reset to stay under SQLite's variable limit and bound lock time.
		const chunkSize = 500
		reset := 0
		failed := 0
		for start := 0; start < len(ids); start += chunkSize {
			if ctx.Err() != nil {
				break
			}
			end := start + chunkSize
			if end > len(ids) {
				end = len(ids)
			}
			chunk := ids[start:end]
			if err := db.SetMergeStatus(ctx, chunk, model.MergeStatusPending); err != nil {
				fmt.Fprintf(os.Stderr, "  reset failed for batch [%d:%d]: %v\n", start, end, err)
				failed += len(chunk)
				continue
			}
			reset += len(chunk)
			fmt.Printf("  reset %d/%d to pending\n", reset, len(ids))
		}
		fmt.Println()
		fmt.Printf("Summary: %d reset to pending, %d failed (of %d total)\n", reset, failed, len(recs))
		if reset > 0 {
			fmt.Println("The merge engine will retry these. If they fail again they'll be re-marked")
			fmt.Println("incompatible — then re-run with --force-delete --execute to reclaim the space.")
		}
		return 0
	}

	// --- Apply: force-delete (DB row + file) ---
	deleted := 0
	deleteFailed := 0
	var freedBytes int64
	for i, r := range recs {
		if ctx.Err() != nil {
			break
		}
		// Delete the DB row first (source of truth), then best-effort the file.
		if err := db.DeleteRecording(ctx, r.ID); err != nil {
			fmt.Fprintf(os.Stderr, "  [%d/%d] DB DELETE FAILED %s: %v\n", i+1, len(recs), r.ID, err)
			deleteFailed++
			continue
		}
		freedBytes += r.FileSize
		deleted++
		// Best-effort file removal. FilePath may be a directory (MJPEG) or a file;
		// either way os.RemoveAll handles both. Ignore errors — the DB row is gone,
		// and an orphan file will be swept by the cleanup manager's orphan pass.
		if r.FilePath != "" {
			_ = os.RemoveAll(r.FilePath)
		}
		if deleted%50 == 0 || deleted == len(recs) {
			fmt.Printf("  [%d/%d] deleted\n", deleted, len(recs))
		}
		// Throttle: avoid IO spikes on RPi/SD-card during large deletes.
		select {
		case <-time.After(20 * time.Millisecond):
		case <-ctx.Done():
		}
	}
	fmt.Println()
	fmt.Printf("Summary: %d deleted (%.2f GB freed), %d failed (of %d total)\n",
		deleted, float64(freedBytes)/1024/1024/1024, deleteFailed, len(recs))
	return 0
}

// runRepairFragmentsResetFakeMerged handles `repair fragments --reset-fake-merged`:
// finds segments marked merge_status='merged' but with an empty merge_path (the
// rolling merge singleton fast-path marked them merged without actually merging),
// and resets them to pending so the merge engine re-queues them for a real merge.
//
// This is the cleanup companion to the singleton-bug fix in rolling.go: existing
// deployments already have thousands of these fake-merged fragments accumulated
// before the fix. After resetting, a service restart (or waiting for periodic
// merge) will re-merge them into proper long recordings.
func runRepairFragmentsResetFakeMerged(opts repairOpts, maxDurationSec float64) int {
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
	fmt.Printf("repair fragments --reset-fake-merged — %s\n", mode)
	fmt.Printf("  target: merge_status='merged' with empty merge_path (never actually merged)\n")
	if maxDurationSec > 0 {
		fmt.Printf("  max-duration: %.0fs (only short singleton fragments; long recordings left alone)\n", maxDurationSec)
	}
	if opts.cameraID != "" {
		fmt.Printf("  camera: %s\n", opts.cameraID)
	}
	if opts.limit > 0 {
		fmt.Printf("  limit:  %d\n", opts.limit)
	}
	fmt.Println()

	recs, err := db.ListFakeMergedRecordings(ctx, opts.cameraID, opts.limit, maxDurationSec)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error listing fake-merged recordings: %v\n", err)
		return 1
	}
	if len(recs) == 0 {
		fmt.Println("No fake-merged recordings found. Nothing to do.")
		return 0
	}

	// Per-camera summary (count / total duration / total size).
	type camStat struct {
		count int
		dur   float64
		size  int64
	}
	byCam := map[string]*camStat{}
	var totalDur float64
	var totalSize int64
	for _, r := range recs {
		s := byCam[r.CameraID]
		if s == nil {
			s = &camStat{}
			byCam[r.CameraID] = s
		}
		s.count++
		s.dur += r.Duration
		s.size += r.FileSize
		totalDur += r.Duration
		totalSize += r.FileSize
	}
	fmt.Printf("Found %d fake-merged recordings across %d camera(s):\n\n", len(recs), len(byCam))
	for cam, s := range byCam {
		fmt.Printf("  %-40s %4d segments  %7.1f min  %6.2f GB\n", cam, s.count, s.dur/60, float64(s.size)/1024/1024/1024)
	}
	fmt.Printf("  %-40s %4d segments  %7.1f min  %6.2f GB\n", "TOTAL", len(recs), totalDur/60, float64(totalSize)/1024/1024/1024)
	fmt.Println()

	if opts.dryRun {
		fmt.Println("Dry run — no changes made. Re-run with --execute to reset these to pending.")
		fmt.Println("After resetting, restart the service (or wait for periodic merge) to re-merge them.")
		return 0
	}

	// Reset to pending in chunks (SetMergeStatus batches internally).
	ids := make([]string, len(recs))
	for i, r := range recs {
		ids[i] = r.ID
	}
	const chunkSize = 500
	reset := 0
	failed := 0
	for start := 0; start < len(ids); start += chunkSize {
		if ctx.Err() != nil {
			break
		}
		end := start + chunkSize
		if end > len(ids) {
			end = len(ids)
		}
		chunk := ids[start:end]
		if err := db.SetMergeStatus(ctx, chunk, model.MergeStatusPending); err != nil {
			fmt.Fprintf(os.Stderr, "  reset failed for batch [%d:%d]: %v\n", start, end, err)
			failed += len(chunk)
			continue
		}
		reset += len(chunk)
		fmt.Printf("  reset %d/%d to pending\n", reset, len(ids))
	}
	fmt.Println()
	fmt.Printf("Summary: %d reset to pending, %d failed (of %d total)\n", reset, failed, len(recs))
	if reset > 0 {
		fmt.Println("Restart the service (or wait for the next periodic merge cycle) to re-merge these")
		fmt.Println("into proper long recordings. They will now appear as pending on the timeline.")
	}
	return 0
}

// runRepairDeleteByFormat implements `repair delete-by-format`: bulk-delete a
// camera's recordings whose format does NOT match --keep-format, preserving
// every recording of the kept format (e.g. delete all regular video while
// keeping every timelapse segment). This is the cleanup companion to
// timelapse-only mode (recording_enabled=false + timelapse.enabled=true):
// after switching a camera to timelapse-only, run this to reclaim the disk
// space held by historical regular-video segments.
//
// Safety:
//   - Always per-camera (--camera required).
//   - In-flight segments (ended_at zero) are skipped — never delete a row the
//     recorder is still writing.
//   - MiBeeVision-protected rows (ai_status='processing') are skipped, matching
//     the cleanup manager's BatchDeleteRecordingsWithFiles guard.
//   - DB row deleted first, then file (best-effort) — project convention.
//   - For rows with a separate merge_path (rolling/periodic merge output), the
//     merge_path file is also removed so it doesn't linger as an orphan that
//     orphan-cleanup can't sweep (it doesn't match the <cameraID>_* pattern).
//   - --dry-run default; --execute to apply. 20ms throttle between deletes.
func runRepairDeleteByFormat() int {
	opts := parseRepairFlags(3)
	if opts.configPath == "__help__" {
		printRepairDeleteByFormatUsage()
		return 0
	}

	if opts.cameraID == "" {
		fmt.Fprintln(os.Stderr, "Error: --camera is required (delete-by-format is always per-camera to avoid accidents).")
		fmt.Fprintln(os.Stderr, "Usage: mibee-nvr repair delete-by-format --camera=<id> --keep-format=timelapse [--dry-run|--execute]")
		return 1
	}
	if opts.keepFormat == "" {
		fmt.Fprintln(os.Stderr, "Error: --keep-format is required (the format to PRESERVE).")
		fmt.Fprintln(os.Stderr, "Example: --keep-format=timelapse  (deletes every other format for this camera)")
		fmt.Fprintln(os.Stderr, "Allowed format values: h264, h265, mjpeg, timelapse, avi")
		return 1
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
	fmt.Printf("repair delete-by-format — %s\n", mode)
	fmt.Printf("  camera:       %s\n", opts.cameraID)
	fmt.Printf("  keep-format:  %s   (PRESERVED)\n", opts.keepFormat)
	fmt.Printf("  delete:       every other format\n")
	if opts.olderThan > 0 {
		fmt.Printf("  older-than:   %s (only segments older than this)\n", opts.olderThan)
	}
	if opts.limit > 0 {
		fmt.Printf("  limit:        %d (max to delete)\n", opts.limit)
	}
	fmt.Println()

	// Fetch every recording for the camera (no format filter — we want all
	// formats so we can both select the to-delete set AND confirm the kept
	// set is untouched). Limit 0 = no LIMIT clause (full table scan for this
	// one camera; bounded by the camera's row count).
	recs, err := db.ListRecordings(ctx, model.RecordingFilter{CameraID: opts.cameraID, Limit: 0, SortBy: "started_at", SortOrder: "asc"})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error listing recordings for camera %s: %v\n", opts.cameraID, err)
		return 1
	}

	// Split into kept (preserve) vs candidates (delete). Candidates are further
	// filtered by: in-flight (ended_at zero) → skip; age (--older-than) → skip
	// if newer; AI-protected (ai_status='processing') → skip.
	keepFormat := model.Format(opts.keepFormat)
	var kept, candidates, skippedInflight, skippedAge []model.Recording
	now := time.Now()
	for _, r := range recs {
		if r.Format == keepFormat {
			kept = append(kept, r)
			continue
		}
		if r.EndedAt.IsZero() {
			skippedInflight = append(skippedInflight, r)
			continue
		}
		if opts.olderThan > 0 && now.Sub(r.EndedAt) < opts.olderThan {
			skippedAge = append(skippedAge, r)
			continue
		}
		candidates = append(candidates, r)
	}

	// Apply --limit (post-filter, so the count is exact).
	if opts.limit > 0 && len(candidates) > opts.limit {
		candidates = candidates[:opts.limit]
	}

	// AI-safety guard: batch-fetch ai_status for the candidates and drop any
	// 'processing' rows (mirrors cleanup.BatchDeleteRecordingsWithFiles).
	skippedAI := 0
	if len(candidates) > 0 {
		ids := make([]string, len(candidates))
		for i, r := range candidates {
			ids[i] = r.ID
		}
		aiStatuses, err := db.BatchGetRecordingAIStatus(ctx, ids)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error fetching AI status: %v\n", err)
			return 1
		}
		filtered := candidates[:0]
		for _, r := range candidates {
			if aiStatuses[r.ID] == "processing" {
				skippedAI++
				continue
			}
			filtered = append(filtered, r)
		}
		candidates = filtered
	}

	// Summary: per-format breakdown of what WILL be deleted, plus confirmation
	// that the kept set is intact.
	type fmtStat struct {
		count int
		size  int64
		dur   float64
	}
	byFmt := map[string]*fmtStat{}
	var totalSize int64
	var totalDur float64
	for _, r := range candidates {
		s := byFmt[string(r.Format)]
		if s == nil {
			s = &fmtStat{}
			byFmt[string(r.Format)] = s
		}
		s.count++
		s.size += r.FileSize
		s.dur += r.Duration
		totalSize += r.FileSize
		totalDur += r.Duration
	}
	var keptSize int64
	for _, r := range kept {
		keptSize += r.FileSize
	}

	fmt.Printf("Camera %s has %d total recordings:\n", opts.cameraID, len(recs))
	fmt.Printf("  PRESERVE (%s): %d recordings (%.2f GB) — NOT touched\n", opts.keepFormat, len(kept), float64(keptSize)/1024/1024/1024)
	fmt.Println()
	if len(candidates) == 0 {
		fmt.Println("No recordings match the delete criteria. Nothing to do.")
		if len(skippedInflight) > 0 {
			fmt.Printf("  (skipped %d in-flight segments with no ended_at)\n", len(skippedInflight))
		}
		if len(skippedAge) > 0 {
			fmt.Printf("  (skipped %d newer than %s)\n", len(skippedAge), opts.olderThan)
		}
		if skippedAI > 0 {
			fmt.Printf("  (skipped %d being processed by MiBeeVision)\n", skippedAI)
		}
		return 0
	}
	fmt.Printf("DELETE candidates: %d recordings (%.2f GB, %.1f min):\n", len(candidates), float64(totalSize)/1024/1024/1024, totalDur/60)
	for fmt_, s := range byFmt {
		fmt.Printf("  %-12s %5d segments  %6.2f GB\n", fmt_, s.count, float64(s.size)/1024/1024/1024)
	}
	fmt.Println()
	if len(skippedInflight) > 0 {
		fmt.Printf("  Skipped (in-flight, no ended_at):   %d\n", len(skippedInflight))
	}
	if len(skippedAge) > 0 {
		fmt.Printf("  Skipped (newer than %s): %d\n", opts.olderThan, len(skippedAge))
	}
	if skippedAI > 0 {
		fmt.Printf("  Skipped (MiBeeVision processing):   %d\n", skippedAI)
	}

	// Show a few sample paths so the operator can sanity-check before --execute.
	fmt.Println()
	fmt.Println("Sample candidate paths (first 5):")
	for i := 0; i < len(candidates) && i < 5; i++ {
		fmt.Printf("  [%s] %s\n", candidates[i].Format, candidates[i].FilePath)
	}

	if opts.dryRun {
		fmt.Println()
		fmt.Println("Dry run — no changes made. Re-run with --execute to delete.")
		return 0
	}

	// --- Execute: per-row delete (DB first, then file + merge_path sibling) ---
	fmt.Println()
	fmt.Println("Executing deletions...")
	deleted := 0
	deleteFailed := 0
	var freedBytes int64
	for i, r := range candidates {
		if ctx.Err() != nil {
			fmt.Fprintf(os.Stderr, "\nInterrupted by signal. Deleted %d of %d before stop.\n", deleted, len(candidates))
			break
		}
		// DB row first (source of truth). On failure, do NOT touch the file —
		// leaving an orphan file is recoverable; a dangling DB row is not.
		if err := db.DeleteRecording(ctx, r.ID); err != nil {
			fmt.Fprintf(os.Stderr, "  [%d/%d] DB DELETE FAILED %s: %v\n", i+1, len(candidates), r.ID, err)
			deleteFailed++
			continue
		}
		freedBytes += r.FileSize
		deleted++
		// Best-effort file removal. FilePath is a regular .mp4/.avi file for
		// non-timelapse formats (we kept timelapse, so no frame-dir case here,
		// but os.RemoveAll handles both safely).
		if r.FilePath != "" {
			_ = os.RemoveAll(r.FilePath)
		}
		// Also remove the merged-output sibling if it's a distinct file.
		// Rolling/periodic merge produces <file_path>.mp4 (or a periodic_*.mp4)
		// stored in merge_path — orphan-cleanup won't sweep these because they
		// don't match the <cameraID>_* filename pattern.
		if r.MergePath != "" && r.MergePath != r.FilePath {
			_ = os.RemoveAll(r.MergePath)
		}
		if deleted%50 == 0 || deleted == len(candidates) {
			fmt.Printf("  [%d/%d] deleted\n", deleted, len(candidates))
		}
		// Throttle: avoid IO spikes on RPi/SD-card during large deletes
		// (matches repair fragments --force-delete cadence).
		select {
		case <-time.After(20 * time.Millisecond):
		case <-ctx.Done():
		}
	}
	fmt.Println()
	fmt.Printf("Summary: %d deleted (%.2f GB freed), %d failed (of %d candidates)\n",
		deleted, float64(freedBytes)/1024/1024/1024, deleteFailed, len(candidates))
	fmt.Printf("Preserved: %d %s recordings (untouched)\n", len(kept), opts.keepFormat)
	if deleted > 0 {
		fmt.Println()
		fmt.Println("Tip: if you also switched this camera to timelapse-only mode, no new regular")
		fmt.Println("video segments will be written — only timelapse frames going forward.")
	}
	return 0
}

// splitCSV splits a comma-separated string, trimming whitespace and dropping empties.
func splitCSV(s string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ',' {
			field := s[start:i]
			// trim spaces
			for len(field) > 0 && (field[0] == ' ' || field[0] == '\t') {
				field = field[1:]
			}
			for len(field) > 0 && (field[len(field)-1] == ' ' || field[len(field)-1] == '\t') {
				field = field[:len(field)-1]
			}
			if field != "" {
				out = append(out, field)
			}
			start = i + 1
		}
	}
	return out
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
  duration          Fix recordings stuck at duration=0 by re-probing video files
  merge-status      Reset merge_status for recordings whose merged file is missing
  fragments         Clean up segments the merge engine gave up on (incompatible/failed)
  delete-by-format  Bulk-delete a camera's recordings of all formats EXCEPT one
                    (e.g. delete regular video while keeping every timelapse segment)

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

func printRepairFragmentsUsage() {
	fmt.Print(`Usage: mibee-nvr repair fragments [options]

Clean up recording segments the merge engine has permanently given up on.
When MergeMP4Segments fails (e.g. SPS/PPS differ between segments after a
camera reconnect), the rolling/periodic merge marks those segments
merge_status='incompatible' so it doesn't retry forever. These accumulate as
debris that will never be merged — they still occupy disk space and clutter
the timeline. This command surfaces and clears them.

By default it reports only (dry-run). Use one of the action flags with
--execute to act:

  --retry           Reset matched segments to 'pending', giving the merge
                    engine another chance. Use this first — if the segments
                    are actually fine and the failure was transient, they'll
                    merge successfully and the debris clears itself.
  --force-delete    Delete the DB row AND the underlying file permanently.
                    Use this when --retry has already been tried and the
                    segments still come back incompatible (genuinely corrupt
                    / unplayable debris). Reclaims the disk space.

Examples:
  # See what debris exists and how much space it uses (no changes)
  mibee-nvr repair fragments --config /path/to/mibee-nvr.yaml

  # Give the merge engine another chance on a few segments first
  mibee-nvr repair fragments --retry --execute --camera <id> --limit 5
  # (watch the logs — if they merge, great; if they flip back to
  #  incompatible, they're genuinely corrupt and should be deleted)

  # Delete all incompatible debris across all cameras
  mibee-nvr repair fragments --force-delete --execute

  # Also include 'failed' and 'dark' segments (comma-separated)
  mibee-nvr repair fragments --status incompatible,failed,dark --force-delete --execute

Live merge states (pending/merged/merging) are NEVER matched by --status — the
tool refuses values that the merge engine is still actively processing.

--reset-fake-merged is a separate operation mode for a different class of debris:
recordings marked merge_status='merged' but with an empty merge_path. These were
marked "merged" by the rolling merge singleton fast-path (a batch with <2 valid
segments) without actually being merged, permanently ejecting them from the merge
queue. They show up as thousands of un-merged short fragments on the timeline.
Resetting them to pending re-queues them for a real merge.

  # Find fake-merged fragments (merged but never actually merged)
  mibee-nvr repair fragments --reset-fake-merged

  # Reset them to pending, then restart the service to re-merge
  mibee-nvr repair fragments --reset-fake-merged --execute
  sudo systemctl restart mibee-nvr

Options:
  --execute              Actually apply the action (default: dry-run, report only)
  --dry-run              Report only, no changes (default)
  --retry                Reset matched segments to pending (merge engine retries)
  --force-delete         Delete DB row + file permanently (mutually exclusive with --retry)
  --reset-fake-merged    Reset merged-but-not-actually-merged segments to pending
                         (separate mode; cannot combine with --status/--retry/--force-delete)
  --max-duration <sec>   Only match segments shorter than this (with --reset-fake-merged).
                         Targets singleton fragments while leaving long already-merged
                         recordings (which legitimately have an empty merge_path) untouched.
                         Recommended: 300 (5min) — singleton fragments are typically ≤60s.
  --status <list>        Comma-separated merge statuses to match
                         (default: incompatible; allowed: incompatible, failed, dark)
  --camera <id>          Only process this camera
  --limit <n>            Max segments to process (0 = no limit; bounds IO on RPi)
  --config <path>        Config file path (default: mibee-nvr.yaml)
  --help, -h             Show this help
`)
}

func printRepairDeleteByFormatUsage() {
	fmt.Print(`Usage: mibee-nvr repair delete-by-format [options]

Bulk-delete one camera's recordings whose format does NOT match --keep-format,
preserving every recording of the kept format. The cleanup companion to
timelapse-only mode (recording_enabled=false + timelapse.enabled=true): after
switching a camera to timelapse-only, run this to reclaim the disk held by
historical regular-video segments while keeping all timelapse recordings.

Always per-camera (--camera required) to avoid accidentally wiping multiple
cameras. The --keep-format value is PRESERVED; everything else for that camera
is deleted.

Safety guards:
  - In-flight segments (ended_at is zero) are NEVER deleted.
  - MiBeeVision-protected segments (ai_status='processing') are skipped.
  - DB row deleted first, then file (best-effort); merge_path siblings are
    also removed so rolling/periodic merge outputs don't linger as orphans.
  - --dry-run default; --execute to apply. 20ms throttle between deletes.

Examples:
  # See what would be deleted for one camera (count, size, samples)
  mibee-nvr repair delete-by-format --camera front-door --keep-format timelapse

  # Actually delete all regular-video segments, keep timelapse
  mibee-nvr repair delete-by-format --camera front-door \
      --keep-format timelapse --execute

  # Only delete recordings older than 7 days, keep timelapse
  mibee-nvr repair delete-by-format --camera front-door \
      --keep-format timelapse --older-than 168h --execute

Options:
  --camera <id>          (required) Only process this camera
  --keep-format <fmt>    (required) Format value to PRESERVE.
                         Allowed: h264, h265, mjpeg, timelapse, avi
  --older-than <dur>     Only delete segments older than this Go duration
                         (e.g. 24h, 168h). Default: no age filter (delete all)
  --execute              Actually delete (default: dry-run, report only)
  --dry-run              Report only, no changes (default)
  --limit <n>            Max segments to delete (0 = no limit; bounds IO on RPi)
  --config <path>        Config file path (default: mibee-nvr.yaml)
  --help, -h             Show this help
`)
}

// parseInt is a small helper to avoid importing strconv just for one call.
func parseInt(s string) (int, error) {
	var n int
	_, err := fmt.Sscanf(s, "%d", &n)
	return n, err
}

// parseFloat parses a float (e.g. "300" seconds) without importing strconv.
func parseFloat(s string) (float64, error) {
	var f float64
	_, err := fmt.Sscanf(s, "%f", &f)
	return f, err
}
