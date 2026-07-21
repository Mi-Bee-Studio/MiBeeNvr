package timelapse

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)

// newTimelapseTestDB opens a fresh storage.DB and runs migrations (including
// the v28 timelapse_merges table). Returns the *storage.DB which satisfies the
// TimelapseMergeStore interface.
func newTimelapseTestDB(t *testing.T) *storage.DB {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := storage.New(dbPath)
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Init(context.Background()); err != nil {
		t.Fatalf("db.Init: %v", err)
	}
	return db
}

// TestPeriodicMergeManager_RecordsDBRow verifies that a successful periodic
// merge inserts a 'completed' row in the timelapse_merges table with the
// correct camera, window, duration label, and source segment ids.
func TestPeriodicMergeManager_RecordsDBRow(t *testing.T) {
	t.Helper()
	db := newTimelapseTestDB(t)
	dataDir := t.TempDir()

	// Two source segment directories with dummy JPEG frames.
	segDir1 := filepath.Join(dataDir, "seg-1")
	mkdirWrite(t, segDir1, "frame_000001.jpg", []byte("dummy"))
	segDir2 := filepath.Join(dataDir, "seg-2")
	mkdirWrite(t, segDir2, "frame_000001.jpg", []byte("dummy"))

	merger := &successMerger{}
	mgr := NewPeriodicMergeManager(
		&mockRecordingListerWithSegments{
			segments: []model.Recording{
				{ID: "seg-1", CameraID: "cam-db", FilePath: segDir1, Format: model.FormatTimelapse, MergeStatus: model.MergeStatusMerged},
				{ID: "seg-2", CameraID: "cam-db", FilePath: segDir2, Format: model.FormatTimelapse, MergeStatus: model.MergeStatusMerged},
			},
		},
		newTrackDB(),
		merger,
		30,
		dataDir,
		8*time.Hour,
		time.UTC,
		WithMergeStore(db),
		WithDurationLabel("8h"),
	)

	refTime := time.Date(2026, 7, 21, 10, 30, 0, 0, time.UTC)
	if err := mgr.Run(context.Background(), "cam-db", refTime); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// The 8h window containing 10:30 UTC is 08:00-16:00.
	expectedStart := time.Date(2026, 7, 21, 8, 0, 0, 0, time.UTC)
	row, err := db.FindTimelapseMergeByWindow(context.Background(), "cam-db", expectedStart, "8h")
	if err != nil {
		t.Fatalf("FindTimelapseMergeByWindow: %v", err)
	}
	if row == nil {
		t.Fatal("expected a timelapse_merges row, got nil")
	}
	if row.Status != model.TimelapseMergeStatusCompleted {
		t.Errorf("status = %q, want %q", row.Status, model.TimelapseMergeStatusCompleted)
	}
	if row.CameraID != "cam-db" {
		t.Errorf("camera_id = %q, want cam-db", row.CameraID)
	}
	if row.DurationLabel != "8h" {
		t.Errorf("duration_label = %q, want 8h", row.DurationLabel)
	}
	if row.OutputPath == "" {
		t.Error("output_path should be set")
	}
	if row.FileSize == 0 {
		t.Error("file_size should be > 0 (the successMerger writes 'merged' content)")
	}
	if row.CompletedAt.IsZero() {
		t.Error("completed_at should be set for completed status")
	}
	if row.SourceSegmentIDs != `["seg-1","seg-2"]` {
		t.Errorf("source_segment_ids = %q, want [\"seg-1\",\"seg-2\"]", row.SourceSegmentIDs)
	}
}

// TestPeriodicMergeManager_ReRunUpsertsDBRow verifies that a re-merge of the
// same window (e.g. user-triggered after the scheduled one) UPDATES the
// existing row instead of inserting a duplicate.
func TestPeriodicMergeManager_ReRunUpsertsDBRow(t *testing.T) {
	t.Helper()
	db := newTimelapseTestDB(t)
	dataDir := t.TempDir()

	// Use two segments so the real merge path (not handleSingleSegment) runs.
	segDir1 := filepath.Join(dataDir, "seg-1")
	mkdirWrite(t, segDir1, "frame_000001.jpg", []byte("dummy"))
	segDir2 := filepath.Join(dataDir, "seg-2")
	mkdirWrite(t, segDir2, "frame_000001.jpg", []byte("dummy"))

	merger := &successMerger{}
	mgr := NewPeriodicMergeManager(
		&mockRecordingListerWithSegments{
			segments: []model.Recording{
				{ID: "seg-1", CameraID: "cam-up", FilePath: segDir1, Format: model.FormatTimelapse, MergeStatus: model.MergeStatusMerged},
				{ID: "seg-2", CameraID: "cam-up", FilePath: segDir2, Format: model.FormatTimelapse, MergeStatus: model.MergeStatusMerged},
			},
		},
		newTrackDB(),
		merger,
		30,
		dataDir,
		24*time.Hour,
		time.UTC,
		WithMergeStore(db),
		WithDurationLabel("natural-day"),
	)

	refTime := time.Date(2026, 7, 21, 10, 30, 0, 0, time.UTC)
	if err := mgr.Run(context.Background(), "cam-up", refTime); err != nil {
		t.Fatalf("first Run: %v", err)
	}

	// Second run of the same window.
	if err := mgr.Run(context.Background(), "cam-up", refTime); err != nil {
		t.Fatalf("second Run: %v", err)
	}

	// Exactly one row should exist for this (camera, window, label).
	merges, err := db.ListTimelapseMerges(context.Background(), storage.TimelapseMergeFilter{
		CameraID: "cam-up",
	})
	if err != nil {
		t.Fatalf("ListTimelapseMerges: %v", err)
	}
	if len(merges) != 1 {
		t.Fatalf("expected exactly 1 row after re-run, got %d", len(merges))
	}
	if merges[0].Status != model.TimelapseMergeStatusCompleted {
		t.Errorf("status = %q, want completed", merges[0].Status)
	}
}

// TestPeriodicMergeManager_NoStoreSkipsDB verifies that when WithMergeStore
// is NOT set, the manager behaves as before (legacy mode) — no DB interaction,
// no error.
func TestPeriodicMergeManager_NoStoreSkipsDB(t *testing.T) {
	t.Helper()
	dataDir := t.TempDir()
	// Two segments → merge path (not single-segment copy).
	segDir1 := filepath.Join(dataDir, "seg-1")
	mkdirWrite(t, segDir1, "frame_000001.jpg", []byte("dummy"))
	segDir2 := filepath.Join(dataDir, "seg-2")
	mkdirWrite(t, segDir2, "frame_000001.jpg", []byte("dummy"))

	merger := &successMerger{}
	// No WithMergeStore option.
	mgr := NewPeriodicMergeManager(
		&mockRecordingListerWithSegments{
			segments: []model.Recording{
				{ID: "seg-1", CameraID: "cam-nostore", FilePath: segDir1, Format: model.FormatTimelapse, MergeStatus: model.MergeStatusMerged},
				{ID: "seg-2", CameraID: "cam-nostore", FilePath: segDir2, Format: model.FormatTimelapse, MergeStatus: model.MergeStatusMerged},
			},
		},
		newTrackDB(),
		merger,
		30,
		dataDir,
		8*time.Hour,
		time.UTC,
	)

	if err := mgr.Run(context.Background(), "cam-nostore", time.Date(2026, 7, 21, 10, 30, 0, 0, time.UTC)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Nothing to assert — the point is that it doesn't panic or error with no store.
}

// mkdirWrite is a tiny helper that creates a directory and writes one file
// inside it, failing the test on any error.
func mkdirWrite(t *testing.T, dir, filename string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, filename), content, 0o644); err != nil {
		t.Fatalf("write %s: %v", filename, err)
	}
}
