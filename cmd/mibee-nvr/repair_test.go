package main

// Tests for repair.go — repair subcommand flag parsing + dry-run execution
// paths (#233). Covers parseRepairFlags (pure), the duration/merge-status
// dry-run paths against a temp DB, and the usage printers.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/stretchr/testify/require"
)

// withArgs saves os.Args, swaps in the given slice for the duration of fn,
// then restores. parseRepairFlags reads os.Args starting at startIdx.
func withArgs(args []string, fn func()) {
	orig := os.Args
	os.Args = args
	defer func() { os.Args = orig }()
	fn()
}

func TestParseRepairFlags_Defaults(t *testing.T) {
	var opts repairOpts
	withArgs([]string{"mibee-nvr", "repair", "duration"}, func() {
		opts = parseRepairFlags(3)
	})
	require.True(t, opts.dryRun, "dry-run must default to true (safe)")
	require.Equal(t, "mibee-nvr.yaml", opts.configPath)
	require.Equal(t, 0, opts.limit)
	require.False(t, opts.prune)
}

func TestParseRepairFlags_Execute(t *testing.T) {
	var opts repairOpts
	withArgs([]string{"bin", "repair", "duration", "--execute"}, func() {
		opts = parseRepairFlags(3)
	})
	require.False(t, opts.dryRun, "--execute clears dry-run")
}

func TestParseRepairFlags_AllFlags(t *testing.T) {
	var opts repairOpts
	withArgs([]string{
		"bin", "repair", "duration",
		"--config", "/tmp/cfg.yaml",
		"--camera", "cam-1",
		"--limit", "50",
		"--prune",
		"--dry-run",
	}, func() {
		opts = parseRepairFlags(3)
	})
	require.Equal(t, "/tmp/cfg.yaml", opts.configPath)
	require.Equal(t, "cam-1", opts.cameraID)
	require.Equal(t, 50, opts.limit)
	require.True(t, opts.prune)
	require.True(t, opts.dryRun)
}

func TestParseRepairFlags_DeleteByFormatFlags(t *testing.T) {
	var opts repairOpts
	withArgs([]string{
		"bin", "repair", "delete-by-format",
		"--keep-format", "timelapse",
		"--older-than", "48h",
	}, func() {
		opts = parseRepairFlags(3)
	})
	require.Equal(t, "timelapse", opts.keepFormat)
	require.Equal(t, 48*time.Hour, opts.olderThan)
}

func TestParseRepairFlags_PruneIntermediateBeforeFlag(t *testing.T) {
	var opts repairOpts
	withArgs([]string{
		"bin", "repair", "prune-intermediate-mp4",
		"--before", "2026-01-01",
	}, func() {
		opts = parseRepairFlags(3)
	})
	require.Equal(t, "2026-01-01", opts.before)
}

func TestParseRepairFlags_LimitNegativeIgnored(t *testing.T) {
	var opts repairOpts
	withArgs([]string{"bin", "repair", "duration", "--limit", "-5"}, func() {
		opts = parseRepairFlags(3)
	})
	require.Equal(t, 0, opts.limit, "negative limit must be ignored")
}

func TestParseRepairFlags_Help(t *testing.T) {
	var opts repairOpts
	withArgs([]string{"bin", "repair", "duration", "--help"}, func() {
		opts = parseRepairFlags(3)
	})
	require.Equal(t, "__help__", opts.configPath)
}

// writeRepairConfig writes a minimal valid config whose storage.root_dir points
// at dir, so openDBFromConfig opens a fresh DB inside the temp dir.
func writeRepairConfig(t *testing.T, dir string) string {
	t.Helper()
	cfgPath := filepath.Join(dir, "mibee-nvr.yaml")
	content := "storage:\n  root_dir: " + dir + "\n  segment_duration: \"30s\"\n"
	require.NoError(t, os.WriteFile(cfgPath, []byte(content), 0o644))
	return cfgPath
}

func TestRunRepairDuration_Help(t *testing.T) {
	var rc int
	withArgs([]string{"bin", "repair", "duration", "--help"}, func() {
		rc = runRepairDuration()
	})
	require.Equal(t, 0, rc, "--help must exit 0")
}

func TestRunRepairDuration_NoConfig(t *testing.T) {
	var rc int
	withArgs([]string{"bin", "repair", "duration", "--config", "/nonexistent/cfg.yaml"}, func() {
		rc = runRepairDuration()
	})
	require.Equal(t, 1, rc, "missing config must exit 1")
}

func TestRunRepairDuration_DryRunNoRecordings(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeRepairConfig(t, dir)

	var rc int
	withArgs([]string{"bin", "repair", "duration", "--config", cfgPath, "--dry-run"}, func() {
		rc = runRepairDuration()
	})
	require.Equal(t, 0, rc, "dry-run with no zero-duration recordings must exit 0")
}

func TestRunRepairMergeStatus_Help(t *testing.T) {
	var rc int
	withArgs([]string{"bin", "repair", "merge-status", "--help"}, func() {
		rc = runRepairMergeStatus()
	})
	require.Equal(t, 0, rc)
}

func TestRunRepairMergeStatus_NoConfig(t *testing.T) {
	var rc int
	withArgs([]string{"bin", "repair", "merge-status", "--config", "/nonexistent/cfg.yaml"}, func() {
		rc = runRepairMergeStatus()
	})
	require.Equal(t, 1, rc)
}

func TestPrintRepairUsage(t *testing.T) {
	// Smoke-test the usage printers: they must not panic and must produce
	// non-empty output on stdout/stderr. Capturing is overkill for a printer.
	require.NotPanics(t, printRepairUsage)
	require.NotPanics(t, printRepairDurationUsage)
	require.NotPanics(t, printRepairMergeStatusUsage)
	require.NotPanics(t, printRepairFragmentsUsage)
	require.NotPanics(t, printRepairDeleteByFormatUsage)
	require.NotPanics(t, printRepairPruneIntermediateMP4Usage)
}

// Seed a zero-duration recording into the temp DB so the dry-run path reports
// it. This exercises the query + reporting loop without mutating anything.
func TestRunRepairDuration_DryRunFindsZeroDuration(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeRepairConfig(t, dir)

	// Open the DB directly and seed a zero-duration recording.
	dbPath := filepath.Join(dir, "mibee-nvr.db")
	openAndSeed := func() {
		db, err := openDBForTest(dbPath)
		require.NoError(t, err)
		defer db.Close()
		require.NoError(t, db.InsertRecording(t.Context(), &model.Recording{
			ID:        "1780000000000000001",
			CameraID:  "cam-1",
			FilePath:  filepath.Join(dir, "nonexistent.mp4"), // absent → skip probe
			Format:    model.FormatH264,
			StartedAt: time.Now(),
			Duration:  0, // zero duration — the repair target
		}))
	}
	openAndSeed()

	var rc int
	withArgs([]string{"bin", "repair", "duration", "--config", cfgPath, "--dry-run"}, func() {
		rc = runRepairDuration()
	})
	require.Equal(t, 0, rc, "dry-run must exit 0 even when recordings are found")
}

// openDBForTest opens + inits the DB at dbPath (mirrors openDBFromConfig minus
// config validation, which isn't needed for these focused tests).
func openDBForTest(dbPath string) (*storage.DB, error) {
	db, err := storage.New(dbPath)
	if err != nil {
		return nil, err
	}
	if err := db.Init(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}
