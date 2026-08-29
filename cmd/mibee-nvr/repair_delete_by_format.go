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

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

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

// runRepairPruneIntermediateMP4 implements `repair prune-intermediate-mp4`:
// one-shot bulk reclaim of the per-segment rolling-merge .mp4 outputs that
// accumulate when periodic timelapse merges run with
// retain_intermediate_mp4=true (or that pre-date the auto-prune feature).
//
// For each timelapse/mjpeg recording with a non-empty merge_path AND a
// 'daily_merged' status (meaning a periodic merge has already folded it into
// a long-window output), this command removes the intermediate .mp4 file and
// clears the DB pointer. The raw frame directories (file_path) are preserved.
//
// Safety:
//   - Per-camera by default (--camera); omit --camera to scan ALL cameras.
//   - --before YYYY-MM-DD limits to recordings started before that UTC date
//     (recommended — never prune the most-recent window the periodic merger
//     may still be working on).
//   - --limit caps the count for bounded runs.
//   - --dry-run default; --execute to apply. 20ms throttle between deletes.

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
