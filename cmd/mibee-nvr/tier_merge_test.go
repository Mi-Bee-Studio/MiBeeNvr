package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/muxer"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/stretchr/testify/require"
)

// Real parseable SPS (1920x1080) — synthetic SPS bytes cannot be probed into
// dimensions and would defeat the merge. Mirrors the merge-package fixture.
var tierTestSPS = []byte{
	0x67, 0x42, 0xc0, 0x28, 0xf4, 0x03, 0xc0, 0x11, 0x2f, 0x28,
}

func writeTierTestSegment(t *testing.T, path string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	m := muxer.NewMP4Muxer(path)
	trackID, err := m.AddH264Track(tierTestSPS, []byte{0x68, 0xce, 0x38, 0x80})
	require.NoError(t, err)
	require.NoError(t, m.WriteSample(trackID, []byte{0x65, 0x88, 0x80, 0x40}, 0, 33*time.Millisecond))
	require.NoError(t, m.WriteSample(trackID, []byte{0x41, 0x10, 0x00, 0x0c}, 33*time.Millisecond, 33*time.Millisecond))
	require.NoError(t, m.Close())
}

// tierMergeTestEnv builds a root with one tiered camera holding 3+2 layer-1
// segments across two hour windows (all older than the freshness guard) and
// returns the config path + DB handle.
func tierMergeTestEnv(t *testing.T) (string, *storage.DB) {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "mibee-nvr.yaml")
	content := "storage:\n  root_dir: " + dir + "\n  segment_duration: \"30s\"\n" +
		"cameras:\n" +
		"  - id: cam-tier\n    name: Tier\n    protocol: rtsp\n    encoding: h264\n    url: rtsp://127.0.0.1:1/sub\n" +
		"    recording_tier: tiered\n"
	require.NoError(t, os.WriteFile(cfgPath, []byte(content), 0o644))

	db, err := storage.New(filepath.Join(dir, "mibee-nvr.db"))
	require.NoError(t, err)
	require.NoError(t, db.Init(context.Background()))

	base := time.Now().Add(-4 * time.Hour).UTC().Truncate(time.Hour)
	for i := range 3 {
		start := base.Add(time.Duration(i) * time.Minute)
		rel := filepath.ToSlash(filepath.Join("cam-tier", start.Format("200601"), start.Format("02"), start.Format("15"),
			"sub_"+start.Format("20060102_150405")+".mp4"))
		writeTierTestSegment(t, filepath.Join(dir, rel))
		require.NoError(t, db.InsertRecording(context.Background(), &model.Recording{
			ID: fmt.Sprintf("tier-src-a-%d", i), CameraID: "cam-tier", FilePath: rel, Format: model.FormatH264,
			StartedAt: start, EndedAt: start.Add(time.Minute), Duration: 60, FileSize: 100,
			MergeStatus: model.MergeStatusSublayer, Layer: model.LayerSub,
		}))
	}
	// Second window, one hour earlier: 2 segments.
	base2 := base.Add(-time.Hour)
	for i := range 2 {
		start := base2.Add(time.Duration(i) * time.Minute)
		rel := filepath.ToSlash(filepath.Join("cam-tier", start.Format("200601"), start.Format("02"), start.Format("15"),
			"sub_"+start.Format("20060102_150405")+".mp4"))
		writeTierTestSegment(t, filepath.Join(dir, rel))
		require.NoError(t, db.InsertRecording(context.Background(), &model.Recording{
			ID: fmt.Sprintf("tier-src-b-%d", i), CameraID: "cam-tier", FilePath: rel, Format: model.FormatH264,
			StartedAt: start, EndedAt: start.Add(time.Minute), Duration: 60, FileSize: 100,
			MergeStatus: model.MergeStatusSublayer, Layer: model.LayerSub,
		}))
	}
	return cfgPath, db
}

func TestRunTierMergeDryRunKeepsEverything(t *testing.T) {
	cfgPath, db := tierMergeTestEnv(t)
	defer db.Close()

	f, code := parseTierMergeFlags([]string{"mibee-nvr", "tier-merge", "--camera", "cam-tier", "--config", cfgPath})
	require.Equal(t, -1, code)

	before := countTierRows(t, db)
	require.Equal(t, 5, before)

	rc := runTierMerge(f, os.Stdout)
	require.Equal(t, 0, rc)
	require.Equal(t, 5, countTierRows(t, db), "dry-run must not touch rows")
}

func TestRunTierMergeExecuteCollapsesWindows(t *testing.T) {
	cfgPath, db := tierMergeTestEnv(t)
	defer db.Close()

	f, code := parseTierMergeFlags([]string{"mibee-nvr", "tier-merge", "--camera", "cam-tier", "--config", cfgPath, "--execute"})
	require.Equal(t, -1, code)

	rc := runTierMerge(f, os.Stdout)
	require.Equal(t, 0, rc)

	// Two windows of ≥2 segments each collapse into exactly 2 product rows.
	rows := listTierRows(t, db)
	require.Len(t, rows, 2)
	for _, r := range rows {
		require.Equal(t, model.LayerSub, r.Layer)
		require.Equal(t, model.MergeStatusSublayer, r.MergeStatus)
		require.Greater(t, r.Duration, 60.0)
		st, err := os.Stat(filepath.Join(rootOf(t, cfgPath), r.FilePath))
		require.NoError(t, err)
		require.Greater(t, st.Size(), int64(0))
	}
}

func TestRunTierMergeFreshWindowSkipped(t *testing.T) {
	cfgPath, db := tierMergeTestEnv(t)
	defer db.Close()

	// A fresh segment written "now": window must be skipped by keep-newest.
	rel := filepath.ToSlash(filepath.Join("cam-tier", "now", "sub_fresh.mp4"))
	writeTierTestSegment(t, filepath.Join(rootOf(t, cfgPath), rel))
	require.NoError(t, db.InsertRecording(context.Background(), &model.Recording{
		ID: "tier-fresh", CameraID: "cam-tier", FilePath: rel, Format: model.FormatH264,
		StartedAt: time.Now().UTC().Add(-30 * time.Second), EndedAt: time.Now().UTC().Add(-10 * time.Second),
		Duration: 20, FileSize: 10, MergeStatus: model.MergeStatusSublayer, Layer: model.LayerSub,
	}))

	f, _ := parseTierMergeFlags([]string{"mibee-nvr", "tier-merge", "--camera", "cam-tier", "--config", cfgPath, "--execute", "--min-segments", "1"})
	rc := runTierMerge(f, os.Stdout)
	require.Equal(t, 0, rc)

	// The fresh row survives; the two old windows still collapsed (3 total).
	rows := listTierRows(t, db)
	require.Len(t, rows, 3)
	freshKept := false
	for _, r := range rows {
		if r.ID == "tier-fresh" {
			freshKept = true
		}
	}
	require.True(t, freshKept, "fresh segment must be kept")
}

func rootOf(t *testing.T, cfgPath string) string {
	t.Helper()
	b, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	for _, line := range strings.Split(string(b), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "root_dir:") {
			return strings.TrimSpace(strings.TrimPrefix(trimmed, "root_dir:"))
		}
	}
	t.Fatal("root_dir not found in test config")
	return ""
}

func countTierRows(t *testing.T, db *storage.DB) int {
	t.Helper()
	layer := 1
	rows, err := db.ListRecordings(context.Background(), model.RecordingFilter{
		CameraID: "cam-tier", Layer: &layer, SortBy: "started_at", SortOrder: "asc",
	})
	require.NoError(t, err)
	return len(rows)
}

func listTierRows(t *testing.T, db *storage.DB) []model.Recording {
	t.Helper()
	layer := 1
	rows, err := db.ListRecordings(context.Background(), model.RecordingFilter{
		CameraID: "cam-tier", Layer: &layer, SortBy: "started_at", SortOrder: "asc",
	})
	require.NoError(t, err)
	return rows
}
