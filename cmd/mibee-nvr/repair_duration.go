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
	"fmt"
	"os"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/mediaprobe"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

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
