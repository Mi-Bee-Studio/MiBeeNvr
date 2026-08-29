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

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

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
