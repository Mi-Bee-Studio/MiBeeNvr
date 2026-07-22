package main

// repair_remerge_h265.go — `mibee-nvr repair remerge-h265` CLI subcommand.
//
// One-shot remediation for H.265 timelapse MP4 files generated before PR #92.
// Those files carry a non-conformant hvcC header (GeneralTierFlag=true paired
// with GeneralProfileIdc=1) that Windows Edge's HEVC Video Extension refuses
// to play. The merger (internal/timelapse/h265_go_merge.go:buildHvcC) was
// fixed in PR #92 to use conservative Main-tier defaults, but every file
// written before that fix is still broken on disk.
//
// This command walks all timelapse recordings whose source frames are still
// on disk, detects which merged MP4s carry the bug, and re-runs the merger
// from the original frame_*.h265 directory. Output is atomically swapped via
// a .tmp + rename so a crash mid-merge never corrupts the original file.

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/timelapse"
)

// runRepairRemergeH265 implements `mibee-nvr repair remerge-h265`.
//
// Exit codes: 0 = success, 1 = fatal error (bad flags, DB open failed).
func runRepairRemergeH265() int {
	opts := parseRepairFlags(3)
	if opts.configPath == "__help__" {
		printRepairRemergeH265Usage()
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
	fmt.Printf("repair remerge-h265 — %s\n", mode)
	if opts.cameraID != "" {
		fmt.Printf("  camera:  %s\n", opts.cameraID)
	} else {
		fmt.Println("  camera:  (all cameras — only H.265 timelapse files are touched)")
	}
	if opts.before != "" {
		fmt.Printf("  before:  %s (UTC, inclusive)\n", opts.before)
	}
	if opts.after != "" {
		fmt.Printf("  after:   %s (UTC, inclusive)\n", opts.after)
	}
	if opts.limit > 0 {
		fmt.Printf("  limit:   %d recordings\n", opts.limit)
	}
	if opts.force {
		fmt.Println("  force:   true (skip hvcC detection, re-merge every H.265 timelapse)")
	}
	fps := opts.fps
	if fps == 0 {
		fps = 10 // matches the merger default used in production (keyframe extractor fps)
	}
	fmt.Printf("  fps:     %d\n", fps)
	fmt.Println()

	// RecordingFilter is the standard query layer used by all repair
	// subcommands. We list timelapse recordings (H.265, H.264, and JPEG sources
	// all share format="timelapse"; detectBuggyHvcC filters to H.265 only).
	// Sort ascending so oldest files get re-merged first (most likely to be the
	// ones users are trying to watch in Edge).
	filter := model.RecordingFilter{
		CameraID:  opts.cameraID,
		Formats:   []model.Format{model.FormatTimelapse},
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
	if opts.after != "" {
		after, err := time.Parse("2006-01-02", opts.after)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: invalid --after %q (use YYYY-MM-DD): %v\n", opts.after, err)
			return 1
		}
		filter.StartTime = after
	}

	// Cap the query with --limit (applied AFTER bug detection, so it limits
	// actual re-merges rather than the underlying scan).
	recs, err := db.ListRecordings(ctx, filter)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error listing recordings: %v\n", err)
		return 1
	}

	// Counters for the summary line.
	scanned := 0
	buggyCount := 0
	fixedCount := 0
	nonH265Count := 0
	missingMerge := 0
	missingFrames := 0
	remergedCount := 0
	var totalRead, totalWritten int64
	merger := timelapse.NewH265GoMerger()

	for _, r := range recs {
		scanned++

		if r.MergePath == "" {
			missingMerge++
			continue
		}
		if _, err := os.Stat(r.MergePath); err != nil {
			missingMerge++
			continue
		}

		// Detect the bug unless --force forces re-merge of every file.
		if !opts.force {
			buggy, codec, derr := detectBuggyHvcC(r.MergePath)
			if derr != nil {
				fmt.Fprintf(os.Stderr, "  WARN: detect failed for %s: %v\n", r.MergePath, derr)
				continue
			}
			if codec != "h265" {
				// H.264 / MJPEG / JPEG timelapse files don't have this bug.
				nonH265Count++
				continue
			}
			if !buggy {
				fixedCount++
				continue
			}
		}

		buggyCount++

		// The recording's source frame dir must still exist for re-merge.
		// Some installations prune frame dirs after periodic merge — those
		// recordings can't be repaired, only re-captured from the camera.
		if _, err := os.Stat(r.FilePath); err != nil {
			missingFrames++
			continue
		}

		// Honor --limit here (after we've counted buggy + missing-frames).
		if opts.limit > 0 && remergedCount >= opts.limit {
			continue
		}

		// Dry-run: just report what would happen.
		if opts.dryRun {
			var size int64
			if st, err := os.Stat(r.MergePath); err == nil {
				size = st.Size()
			}
			totalRead += size
			fmt.Printf("  [dry-run] would re-merge: %s\n", filepath.Base(r.MergePath))
			fmt.Printf("            %s, %s frames, %s\n", r.ID, r.Format, humanBytes(size))
			remergedCount++
			continue
		}

		// Execute: re-merge into a .tmp file next to the target, then rename.
		// The .tmp file lives in the same directory so the rename is atomic on
		// the same filesystem (no cross-device copy).
		tmpPath := r.MergePath + ".remerge.tmp"
		start := time.Now()
		mres, merr := merger.Merge(ctx, r.FilePath, tmpPath, fps)
		if merr != nil {
			fmt.Fprintf(os.Stderr, "  WARN: merge failed for %s: %v\n", r.MergePath, merr)
			os.Remove(tmpPath) // best-effort cleanup of partial output
			continue
		}
		if mres.Error != "" {
			fmt.Fprintf(os.Stderr, "  WARN: merge reported error for %s: %s\n", r.MergePath, mres.Error)
			os.Remove(tmpPath)
			continue
		}
		if err := os.Rename(tmpPath, r.MergePath); err != nil {
			fmt.Fprintf(os.Stderr, "  WARN: rename %s -> %s failed: %v\n", tmpPath, r.MergePath, err)
			os.Remove(tmpPath)
			continue
		}
		newSize, _ := os.Stat(r.MergePath)
		var newBytes int64
		if newSize != nil {
			newBytes = newSize.Size()
		}
		totalRead += newBytes
		totalWritten += newBytes
		remergedCount++
		fmt.Printf("  re-merged: %s (%s frames, %s, %v)\n",
			filepath.Base(r.MergePath), r.Format, humanBytes(newBytes), time.Since(start).Round(time.Millisecond))
		// Throttle to be friendly to USB HDD on RPi/Banana Pi.
		time.Sleep(50 * time.Millisecond)
	}

	fmt.Println()
	fmt.Printf("Summary: scanned %d timelapse recordings, re-merged %d\n", scanned, remergedCount)
	if !opts.dryRun {
		fmt.Printf("  disk I/O: %s read, %s written\n", humanBytes(totalRead), humanBytes(totalWritten))
	}
	if buggyCount > 0 {
		fmt.Printf("  buggy hvcC detected:    %d\n", buggyCount)
	}
	if fixedCount > 0 {
		fmt.Printf("  already fixed (skipped): %d\n", fixedCount)
	}
	if nonH265Count > 0 {
		fmt.Printf("  non-H.265 (skipped):    %d\n", nonH265Count)
	}
	if missingMerge > 0 {
		fmt.Printf("  missing merge file:     %d\n", missingMerge)
	}
	if missingFrames > 0 {
		fmt.Printf("  missing source frames:  %d\n", missingFrames)
		fmt.Println("  (these can't be repaired — original frame_*.h265 dirs were pruned)")
	}
	if opts.dryRun && remergedCount > 0 {
		fmt.Println("\nThis was a DRY RUN. Re-run with --execute to apply.")
	}
	return 0
}

// printRepairRemergeH265Usage prints the help text for `repair remerge-h265`.
func printRepairRemergeH265Usage() {
	fmt.Print(`Usage: mibee-nvr repair remerge-h265 [options]

Re-merge H.265 timelapse recordings whose merged MP4 carries the buggy hvcC
header that Windows Edge refuses to play (general_tier_flag=1 paired with
general_profile_idc=1, fixed in PR #92). For each affected recording, reads
the original frame_*.h265 directory and re-runs the now-fixed pure-Go merger,
atomically swapping the .mp4 via .tmp + rename.

Safe to run while the server is stopped (preferred) or running (WAL mode
allows concurrent readers). Recommend --dry-run first.

Options:
  --camera <id>          Limit to one camera (default: all H.265 timelapse)
  --before <YYYY-MM-DD>  Only re-merge recordings started before this UTC date
  --after  <YYYY-MM-DD>  Only re-merge recordings started after this UTC date
  --limit <N>            Cap the number of re-merges (0 = no cap)
  --fps <N>              FPS for the re-merge (default: 10)
  --force                Skip hvcC detection; re-merge every H.265 timelapse
                         regardless of whether it's already fixed
  --dry-run              Report what would change without writing (default)
  --execute              Actually re-merge and atomically swap
  --config <path>        Config file path (default: mibee-nvr.yaml)
  --help, -h             Show this help
`)
}
