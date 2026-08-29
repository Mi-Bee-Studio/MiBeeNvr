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

func runRepairFragments() int {
	opts := parseRepairFlags(3)
	if opts.configPath == "__help__" {
		printRepairFragmentsUsage()
		return 0
	}

	// Parse fragments-specific flags (in addition to the shared flags already
	// parsed by parseRepairFlags: --execute/--dry-run/--camera/--limit/--config).
	var statusStr string
	var retry, forceDelete bool
	for i := 3; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--retry":
			retry = true
		case "--force-delete":
			forceDelete = true
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

Options:
  --execute              Actually apply the action (default: dry-run, report only)
  --dry-run              Report only, no changes (default)
  --retry                Reset matched segments to pending (merge engine retries)
  --force-delete         Delete DB row + file permanently (mutually exclusive with --retry)
  --status <list>        Comma-separated merge statuses to match
                         (default: incompatible; allowed: incompatible, failed, dark)
  --camera <id>          Only process this camera
  --limit <n>            Max segments to process (0 = no limit; bounds IO on RPi)
  --config <path>        Config file path (default: mibee-nvr.yaml)
  --help, -h             Show this help
`)
}
