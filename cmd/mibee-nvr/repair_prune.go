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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

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
func runRepairPruneIntermediateMP4() int {
	opts := parseRepairFlags(3)
	if opts.configPath == "__help__" {
		printRepairPruneIntermediateMP4Usage()
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
	fmt.Printf("repair prune-intermediate-mp4 — %s\n", mode)
	if opts.cameraID != "" {
		fmt.Printf("  camera:  %s\n", opts.cameraID)
	} else {
		fmt.Println("  camera:  (all cameras)")
	}
	if opts.before != "" {
		fmt.Printf("  before:  %s (UTC, inclusive)\n", opts.before)
	}
	if opts.limit > 0 {
		fmt.Printf("  limit:   %d recordings\n", opts.limit)
	}
	fmt.Println()

	// Query timelapse + mjpeg recordings with a non-empty merge_path that have
	// been folded into a periodic output (daily_merged). Sort ascending so the
	// oldest (least-likely-to-be-re-merged) get pruned first.
	filter := model.RecordingFilter{
		CameraID:  opts.cameraID,
		Formats:   []model.Format{model.FormatTimelapse, model.FormatMJPEG},
		Limit:     opts.limit,
		SortBy:    "started_at",
		SortOrder: "asc",
	}
	if opts.before != "" {
		before, err := time.Parse("2006-01-02", opts.before)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: invalid --before %q (use YYYY-MM-DD): %v\n", opts.before, err)
			return 1
		}
		filter.EndTime = before
	}
	recs, err := db.ListRecordings(ctx, filter)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error listing recordings: %v\n", err)
		return 1
	}

	prunedCount := 0
	skippedNotDailyMerged := 0
	skippedNoMergePath := 0
	var clearedIDs []string
	totalBytes := int64(0)
	for _, r := range recs {
		if r.MergePath == "" {
			skippedNoMergePath++
			continue
		}
		// Only prune segments that have been folded into a periodic output.
		// A plain 'merged' status (rolling merge only, no periodic yet) is
		// preserved — the periodic output may not exist yet.
		if r.MergeStatus != model.MergeStatusDailyMerged {
			skippedNotDailyMerged++
			continue
		}
		if opts.dryRun {
			var size int64
			if st, err := os.Stat(r.MergePath); err == nil {
				size = st.Size()
			}
			totalBytes += size
			fmt.Printf("  [dry-run] would prune: %s  (%s, %s, %s)\n", r.MergePath, r.ID, r.Format, humanBytes(size))
			prunedCount++
			clearedIDs = append(clearedIDs, r.ID)
			continue
		}
		size, err := os.Stat(r.MergePath)
		var sizeBytes int64
		if err == nil {
			sizeBytes = size.Size()
		}
		if err := os.Remove(r.MergePath); err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "  WARN: failed to remove %s: %v\n", r.MergePath, err)
			continue
		}
		prunedCount++
		totalBytes += sizeBytes
		clearedIDs = append(clearedIDs, r.ID)
		// Throttle to be friendly to USB HDD.
		time.Sleep(20 * time.Millisecond)
	}

	// Batch-clear the merge_path DB pointers.
	if !opts.dryRun && len(clearedIDs) > 0 {
		if err := db.ClearMergePathBatch(ctx, clearedIDs); err != nil {
			fmt.Fprintf(os.Stderr, "  WARN: ClearMergePathBatch failed: %v (files already removed)\n", err)
		}
	}

	fmt.Println()
	fmt.Printf("Summary: pruned %d intermediate .mp4 outputs (%s reclaimed)\n", prunedCount, humanBytes(totalBytes))
	if skippedNoMergePath > 0 {
		fmt.Printf("  skipped (no merge_path):        %d\n", skippedNoMergePath)
	}
	if skippedNotDailyMerged > 0 {
		fmt.Printf("  skipped (not daily_merged yet):  %d\n", skippedNotDailyMerged)
	}
	if opts.dryRun && prunedCount > 0 {
		fmt.Println("\nThis was a DRY RUN. Re-run with --execute to apply.")
	}
	return 0
}

// runRepairReclaimOrphanMerges implements `repair reclaim-orphan-merges`: a
// one-shot reclaim of merged-output MP4 files left on disk after their
// recording rows were deleted via the web UI before the #117 fix shipped.
//
// The merged MP4 (recordings.merge_path) is written by the rolling/segment
// merge into the nested tree <rootDir>/<cameraID>/YYYYMM/DD/HH/. The recording
// delete paths historically only removed file_path, so the merge output leaked
// — and the automatic orphan scanner (cleanOrphansForCamera) never reaches
// that nested tree, so the leak was permanent. This command closes the gap for
// already-leaked historical files. Going forward, the delete paths reclaim
// merge_path themselves.
//
// For each .mp4 under the camera tree that is not referenced by any
// recording's file_path or merge_path (PathIsRecordingFile), it removes the
// file. Only *.mp4 files are considered — source frame directories and the
// raw segment files are never touched.
//
// Safety:
//   - Per-camera by default (--camera); omit --camera to scan ALL cameras.
//   - --limit caps the count for bounded runs.
//   - --dry-run default; --execute to apply. 20ms throttle between deletes.

// runRepairReclaimOrphanMerges implements `repair reclaim-orphan-merges`: a
// one-shot reclaim of merged-output MP4 files left on disk after their
// recording rows were deleted via the web UI before the #117 fix shipped.
//
// The merged MP4 (recordings.merge_path) is written by the rolling/segment
// merge into the nested tree <rootDir>/<cameraID>/YYYYMM/DD/HH/. The recording
// delete paths historically only removed file_path, so the merge output leaked
// — and the automatic orphan scanner (cleanOrphansForCamera) never reaches
// that nested tree, so the leak was permanent. This command closes the gap for
// already-leaked historical files. Going forward, the delete paths reclaim
// merge_path themselves.
//
// For each .mp4 under the camera tree that is not referenced by any
// recording's file_path or merge_path (PathIsRecordingFile), it removes the
// file. Only *.mp4 files are considered — source frame directories and the
// raw segment files are never touched.
//
// Safety:
//   - Per-camera by default (--camera); omit --camera to scan ALL cameras.
//   - --limit caps the count for bounded runs.
//   - --dry-run default; --execute to apply. 20ms throttle between deletes.
func runRepairReclaimOrphanMerges() int {
	opts := parseRepairFlags(3)
	if opts.configPath == "__help__" {
		printRepairReclaimOrphanMergesUsage()
		return 0
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
	fmt.Printf("repair reclaim-orphan-merges — %s\n", mode)
	if opts.cameraID != "" {
		fmt.Printf("  camera: %s\n", opts.cameraID)
	} else {
		fmt.Println("  camera: (all cameras)")
	}
	if opts.limit > 0 {
		fmt.Printf("  limit:  %d orphan files\n", opts.limit)
	}
	fmt.Println()

	rootDir := cfg.Storage.RootDir
	// Determine the camera directories to scan.
	var cameras []string
	if opts.cameraID != "" {
		cameras = []string{opts.cameraID}
	} else {
		entries, err := os.ReadDir(rootDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading storage root %q: %v\n", rootDir, err)
			return 1
		}
		for _, e := range entries {
			if e.IsDir() {
				cameras = append(cameras, e.Name())
			}
		}
	}

	reclaimedCount := 0
	skippedReferenced := 0
	totalBytes := int64(0)
	limitHit := false
	for _, cam := range cameras {
		if limitHit {
			break
		}
		camRoot := filepath.Join(rootDir, cam)
		// Recursively walk the camera tree. Only .mp4 files at the leaf
		// (YYYYMM/DD/HH/) level are candidates — that is where merge_path lives.
		walkErr := filepath.WalkDir(camRoot, func(path string, d os.DirEntry, werr error) error {
			if werr != nil {
				// Tolerate per-entry FS errors (e.g. a file vanished mid-scan)
				// by reporting and continuing the walk, rather than aborting.
				fmt.Fprintf(os.Stderr, "  WARN: walk error at %s: %v\n", path, werr)
				return nil
			}
			if d.IsDir() || !strings.HasSuffix(d.Name(), ".mp4") {
				return nil
			}
			if opts.limit > 0 && reclaimedCount >= opts.limit {
				limitHit = true
				return filepath.SkipDir
			}
			referenced, err := db.PathIsRecordingFile(ctx, cam, path)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  WARN: PathIsRecordingFile(%s): %v\n", path, err)
				return nil
			}
			if referenced {
				skippedReferenced++
				return nil
			}
			var size int64
			if st, err := os.Stat(path); err == nil {
				size = st.Size()
			}
			if opts.dryRun {
				totalBytes += size
				fmt.Printf("  [dry-run] would reclaim: %s  (%s)\n", path, humanBytes(size))
				reclaimedCount++
				return nil
			}
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				fmt.Fprintf(os.Stderr, "  WARN: failed to remove %s: %v\n", path, err)
				return nil
			}
			reclaimedCount++
			totalBytes += size
			time.Sleep(20 * time.Millisecond) // friendly to USB HDD
			return nil
		})
		if walkErr != nil && !errors.Is(walkErr, filepath.SkipDir) {
			fmt.Fprintf(os.Stderr, "  WARN: walk %s: %v\n", camRoot, walkErr)
		}
	}

	fmt.Println()
	fmt.Printf("Summary: reclaimed %d orphan merge MP4s (%s freed)\n", reclaimedCount, humanBytes(totalBytes))
	if skippedReferenced > 0 {
		fmt.Printf("  skipped (still referenced by a recording): %d\n", skippedReferenced)
	}
	if opts.dryRun && reclaimedCount > 0 {
		fmt.Println("\nThis was a DRY RUN. Re-run with --execute to apply.")
	}
	return 0
}

// humanBytes formats a byte count as a human-readable string (KB/MB/GB).

func printRepairPruneIntermediateMP4Usage() {
	fmt.Print(`Usage: mibee-nvr repair prune-intermediate-mp4 [options]

Bulk-remove the per-segment rolling-merge .mp4 outputs that have already been
folded into a periodic (8h/12h/24h/natural-day/7d/30d) timelapse merge output.
After a periodic merge succeeds, those intermediate files are redundant — the
long-window output already contains them. This command reclaims that disk
(the production NVR observed ~1.5GB/day per camera accumulating).

Only recordings with:
  - format IN (timelapse, mjpeg)
  - non-empty merge_path
  - merge_status = 'daily_merged'
are pruned. Recordings with merge_status='merged' (rolling only, not yet
periodic-folded) are PRESERVED — the periodic output may not exist yet.

The raw frame directories (recordings.file_path: frame_*.h264/.h265/.jpg) are
ALWAYS preserved regardless — only the intermediate .mp4 outputs are removed.

Examples:

  # Dry-run: see what would be pruned for one camera
  mibee-nvr repair prune-intermediate-mp4 --camera=front-door --before=2026-07-21

  # Apply for one camera
  mibee-nvr repair prune-intermediate-mp4 --camera=front-door --before=2026-07-21 --execute

  # Reclaim across ALL cameras, only recordings older than today
  mibee-nvr repair prune-intermediate-mp4 --before=2026-07-21 --execute

Options:
  --camera <id>          Limit to one camera (optional; omit for all cameras)
  --before YYYY-MM-DD    Only prune recordings started before this UTC date
                         (recommended — protects the active window)
  --limit <n>            Cap the number of recordings processed (0 = no limit)
  --dry-run              Report only, no changes (default)
  --execute              Actually prune files + clear DB pointers
  --config <path>        Config file path (default: mibee-nvr.yaml)
  --help, -h             Show this help
`)
}

func printRepairReclaimOrphanMergesUsage() {
	fmt.Print(`Usage: mibee-nvr repair reclaim-orphan-merges [options]

Reclaim merged-output .mp4 files left on disk after their recording row was
deleted via the web UI before the #117 fix shipped. The rolling/segment merge
writes each recording's merged MP4 into the nested tree
  <rootDir>/<cameraID>/YYYYMM/DD/HH/<cameraID>_<ts>_<uuid>.mp4
and records its path in recordings.merge_path. Before the #117 fix, deleting a
recording removed the DB row and the source file (file_path) but left the
merged MP4 behind — and the automatic orphan scanner never reaches that nested
tree, so the file leaked permanently. This command reclaims those historical
leaks. (Going forward, the delete paths reclaim merge_path themselves.)

For each .mp4 under the camera tree, it asks the DB whether any recording still
references it (as file_path OR merge_path); if not, the file is removed. Only
unreferenced .mp4 files are touched — source frame directories and the raw
segment files are never deleted.

Examples:

  # Dry-run: see what would be reclaimed across all cameras
  mibee-nvr repair reclaim-orphan-merges

  # Dry-run for one camera
  mibee-nvr repair reclaim-orphan-merges --camera=front-door

  # Apply (one camera, bounded)
  mibee-nvr repair reclaim-orphan-merges --camera=front-door --limit 200 --execute

Options:
  --camera <id>      Limit to one camera (optional; omit for all cameras)
  --limit <n>        Cap the number of orphan files removed (0 = no limit)
  --dry-run          Report only, no changes (default)
  --execute          Actually remove orphan files
  --config <path>    Config file path (default: mibee-nvr.yaml)
  --help, -h         Show this help
`)
}

// parseInt is a small helper to avoid importing strconv just for one call.
