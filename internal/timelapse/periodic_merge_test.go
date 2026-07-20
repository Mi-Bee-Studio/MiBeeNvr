package timelapse

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

func TestNewPeriodicMergeManager(t *testing.T) {
	t.Helper()
	m := NewPeriodicMergeManager(&mockRecordingLister{}, &mockMergeStatusUpdater{}, nil, 10, "/tmp/test-data", 8*time.Hour, nil)
	if m == nil {
		t.Fatal("expected non-nil PeriodicMergeManager")
	}
	if got := m.Duration(); got != 8*time.Hour {
		t.Fatalf("expected Duration 8h, got %v", got)
	}
}

func TestHasUnmergedRawSegments(t *testing.T) {
	t.Helper()
	tests := []struct {
		name     string
		segments []model.Recording
		want     bool
	}{
		{
			name:     "empty",
			segments: nil,
			want:     false,
		},
		{
			name: "all merged",
			segments: []model.Recording{
				{ID: "1", MergeStatus: model.MergeStatusMerged},
				{ID: "2", MergeStatus: model.MergeStatusMerged},
			},
			want: false,
		},
		{
			name: "one unmerged (empty status)",
			segments: []model.Recording{
				{ID: "1", MergeStatus: model.MergeStatusMerged},
				{ID: "2", MergeStatus: ""},
			},
			want: true,
		},
		{
			name: "one pending",
			segments: []model.Recording{
				{ID: "1", MergeStatus: model.MergeStatusPending},
			},
			want: true,
		},
		{
			name: "all unmerged",
			segments: []model.Recording{
				{ID: "1", MergeStatus: ""},
				{ID: "2", MergeStatus: ""},
			},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hasUnmergedRawSegments(tt.segments)
			if got != tt.want {
				t.Fatalf("hasUnmergedRawSegments() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFilterEligibleSegments_IncludesUnmerged(t *testing.T) {
	t.Helper()
	m := NewPeriodicMergeManager(&mockRecordingLister{}, &mockMergeStatusUpdater{}, nil, 10, "/tmp/test", 24*time.Hour, nil)
	recordings := []model.Recording{
		{ID: "merged-1", MergeStatus: model.MergeStatusMerged},
		{ID: "unmerged-1", MergeStatus: ""}, // raw frame dir, no rolling merge
		{ID: "pending-1", MergeStatus: model.MergeStatusPending},
	}
	segments := m.filterEligibleSegments(recordings)
	if len(segments) != 3 {
		t.Fatalf("expected 3 eligible segments (merged + unmerged + pending), got %d", len(segments))
	}
}

func TestPeriodicMergeManager_Duration(t *testing.T) {
	t.Helper()
	tests := []struct {
		dur  time.Duration
		name string
	}{
		{8 * time.Hour, "8h"},
		{12 * time.Hour, "12h"},
		{24 * time.Hour, "24h"},
		{7 * 24 * time.Hour, "7d"},
		{30 * 24 * time.Hour, "30d"},
	}
	for _, tt := range tests {
		m := NewPeriodicMergeManager(&mockRecordingLister{}, &mockMergeStatusUpdater{}, nil, 10, "/tmp/test-data", tt.dur, nil)
		if got := m.Duration(); got != tt.dur {
			t.Errorf("expected Duration %s, got %s", tt.dur, got)
		}
	}
}

func TestPeriodicMergeManager_Run_NoSegments(t *testing.T) {
	t.Helper()
	m := NewPeriodicMergeManager(&mockRecordingLister{}, &mockMergeStatusUpdater{}, nil, 10, t.TempDir(), 8*time.Hour, nil)
	err := m.Run(context.Background(), "test-cam", time.Date(2025, 6, 7, 10, 30, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// --- parseMergeRange tests ---

func TestParseMergeRange_8h(t *testing.T) {
	t.Helper()
	// t=2025-06-07T10:30:00Z → window=08:00-16:00
	tm := time.Date(2025, 6, 7, 10, 30, 0, 0, time.UTC)
	start, end := parseMergeRange(tm, 8*time.Hour, nil)

	expectedStart := time.Date(2025, 6, 7, 8, 0, 0, 0, time.UTC)
	expectedEnd := time.Date(2025, 6, 7, 16, 0, 0, 0, time.UTC)

	if !start.Equal(expectedStart) {
		t.Errorf("expected start %v, got %v", expectedStart, start)
	}
	if !end.Equal(expectedEnd) {
		t.Errorf("expected end %v, got %v", expectedEnd, end)
	}
}

func TestParseMergeRange_8h_Edge(t *testing.T) {
	t.Helper()
	// t=2025-06-07T08:00:00Z (exactly at boundary) → window=08:00-16:00
	tm := time.Date(2025, 6, 7, 8, 0, 0, 0, time.UTC)
	start, end := parseMergeRange(tm, 8*time.Hour, nil)

	expectedStart := time.Date(2025, 6, 7, 8, 0, 0, 0, time.UTC)
	expectedEnd := time.Date(2025, 6, 7, 16, 0, 0, 0, time.UTC)

	if !start.Equal(expectedStart) {
		t.Errorf("expected start %v, got %v", expectedStart, start)
	}
	if !end.Equal(expectedEnd) {
		t.Errorf("expected end %v, got %v", expectedEnd, end)
	}
}

func TestParseMergeRange_8h_FirstBlock(t *testing.T) {
	t.Helper()
	// t=2025-06-07T03:00:00Z → window=00:00-08:00
	tm := time.Date(2025, 6, 7, 3, 0, 0, 0, time.UTC)
	start, end := parseMergeRange(tm, 8*time.Hour, nil)

	expectedStart := time.Date(2025, 6, 7, 0, 0, 0, 0, time.UTC)
	expectedEnd := time.Date(2025, 6, 7, 8, 0, 0, 0, time.UTC)

	if !start.Equal(expectedStart) {
		t.Errorf("expected start %v, got %v", expectedStart, start)
	}
	if !end.Equal(expectedEnd) {
		t.Errorf("expected end %v, got %v", expectedEnd, end)
	}
}

func TestParseMergeRange_12h(t *testing.T) {
	t.Helper()
	// t=2025-06-07T10:30:00Z → window=00:00-12:00 (truncates down to midnight)
	tm := time.Date(2025, 6, 7, 10, 30, 0, 0, time.UTC)
	start, end := parseMergeRange(tm, 12*time.Hour, nil)

	expectedStart := time.Date(2025, 6, 7, 0, 0, 0, 0, time.UTC)
	expectedEnd := time.Date(2025, 6, 7, 12, 0, 0, 0, time.UTC)

	if !start.Equal(expectedStart) {
		t.Errorf("expected start %v, got %v", expectedStart, start)
	}
	if !end.Equal(expectedEnd) {
		t.Errorf("expected end %v, got %v", expectedEnd, end)
	}
}

func TestParseMergeRange_12h_Afternoon(t *testing.T) {
	t.Helper()
	// t=2025-06-07T14:00:00Z → window=12:00-00:00 (next midnight)
	tm := time.Date(2025, 6, 7, 14, 0, 0, 0, time.UTC)
	start, end := parseMergeRange(tm, 12*time.Hour, nil)

	expectedStart := time.Date(2025, 6, 7, 12, 0, 0, 0, time.UTC)
	expectedEnd := time.Date(2025, 6, 8, 0, 0, 0, 0, time.UTC)

	if !start.Equal(expectedStart) {
		t.Errorf("expected start %v, got %v", expectedStart, start)
	}
	if !end.Equal(expectedEnd) {
		t.Errorf("expected end %v, got %v", expectedEnd, end)
	}
}

func TestParseMergeRange_24h(t *testing.T) {
	t.Helper()
	// t=2025-06-07T10:30:00Z → window=00:00-00:00 (next midnight)
	tm := time.Date(2025, 6, 7, 10, 30, 0, 0, time.UTC)
	start, end := parseMergeRange(tm, 24*time.Hour, nil)

	expectedStart := time.Date(2025, 6, 7, 0, 0, 0, 0, time.UTC)
	expectedEnd := time.Date(2025, 6, 8, 0, 0, 0, 0, time.UTC)

	if !start.Equal(expectedStart) {
		t.Errorf("expected start %v, got %v", expectedStart, start)
	}
	if !end.Equal(expectedEnd) {
		t.Errorf("expected end %v, got %v", expectedEnd, end)
	}
}

func TestParseMergeRange_7d_Monday(t *testing.T) {
	t.Helper()
	// 2025-06-02 is a Monday
	// t=2025-06-04T12:00:00Z (Wednesday) → window=Mon 00:00 to next Mon 00:00
	tm := time.Date(2025, 6, 4, 12, 0, 0, 0, time.UTC)
	start, end := parseMergeRange(tm, 7*24*time.Hour, nil)

	expectedStart := time.Date(2025, 6, 2, 0, 0, 0, 0, time.UTC) // Monday
	expectedEnd := time.Date(2025, 6, 9, 0, 0, 0, 0, time.UTC)   // next Monday

	if !start.Equal(expectedStart) {
		t.Errorf("expected start %v (Monday), got %v", expectedStart, start)
	}
	if !end.Equal(expectedEnd) {
		t.Errorf("expected end %v (next Monday), got %v", expectedEnd, end)
	}
}

func TestParseMergeRange_7d_ExactMonday(t *testing.T) {
	t.Helper()
	// t=2025-06-02T00:00:00Z (Monday midnight) → window=this Mon to next Mon
	tm := time.Date(2025, 6, 2, 0, 0, 0, 0, time.UTC)
	start, end := parseMergeRange(tm, 7*24*time.Hour, nil)

	expectedStart := time.Date(2025, 6, 2, 0, 0, 0, 0, time.UTC)
	expectedEnd := time.Date(2025, 6, 9, 0, 0, 0, 0, time.UTC)

	if !start.Equal(expectedStart) {
		t.Errorf("expected start %v, got %v", expectedStart, start)
	}
	if !end.Equal(expectedEnd) {
		t.Errorf("expected end %v, got %v", expectedEnd, end)
	}
}

func TestParseMergeRange_7d_Sunday(t *testing.T) {
	t.Helper()
	// 2025-06-08 is a Sunday
	// t=2025-06-08T12:00:00Z → window=Mon 2025-06-02 to Mon 2025-06-09
	tm := time.Date(2025, 6, 8, 12, 0, 0, 0, time.UTC)
	start, end := parseMergeRange(tm, 7*24*time.Hour, nil)

	expectedStart := time.Date(2025, 6, 2, 0, 0, 0, 0, time.UTC) // Monday
	expectedEnd := time.Date(2025, 6, 9, 0, 0, 0, 0, time.UTC)   // next Monday

	if !start.Equal(expectedStart) {
		t.Errorf("expected start %v (Monday), got %v", expectedStart, start)
	}
	if !end.Equal(expectedEnd) {
		t.Errorf("expected end %v (next Monday), got %v", expectedEnd, end)
	}
}

func TestParseMergeRange_30d(t *testing.T) {
	t.Helper()
	// t=2025-06-15T10:30:00Z → window=June 1 to July 1
	tm := time.Date(2025, 6, 15, 10, 30, 0, 0, time.UTC)
	start, end := parseMergeRange(tm, 30*24*time.Hour, nil)

	expectedStart := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	expectedEnd := time.Date(2025, 7, 1, 0, 0, 0, 0, time.UTC)

	if !start.Equal(expectedStart) {
		t.Errorf("expected start %v (1st of month), got %v", expectedStart, start)
	}
	if !end.Equal(expectedEnd) {
		t.Errorf("expected end %v (1st of next month), got %v", expectedEnd, end)
	}
}

func TestParseMergeRange_30d_ExactFirst(t *testing.T) {
	t.Helper()
	// t=2025-06-01T00:00:00Z → window=June 1 to July 1
	tm := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	start, end := parseMergeRange(tm, 30*24*time.Hour, nil)

	expectedStart := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	expectedEnd := time.Date(2025, 7, 1, 0, 0, 0, 0, time.UTC)

	if !start.Equal(expectedStart) {
		t.Errorf("expected start %v, got %v", expectedStart, start)
	}
	if !end.Equal(expectedEnd) {
		t.Errorf("expected end %v, got %v", expectedEnd, end)
	}
}

func TestParseMergeRange_30d_YearBoundary(t *testing.T) {
	t.Helper()
	// t=2025-12-15T10:30:00Z → window=Dec 1 to Jan 1
	tm := time.Date(2025, 12, 15, 10, 30, 0, 0, time.UTC)
	start, end := parseMergeRange(tm, 30*24*time.Hour, nil)

	expectedStart := time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC)
	expectedEnd := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	if !start.Equal(expectedStart) {
		t.Errorf("expected start %v, got %v", expectedStart, start)
	}
	if !end.Equal(expectedEnd) {
		t.Errorf("expected end %v, got %v", expectedEnd, end)
	}
}

// --- Integration tests ---

func TestPeriodicMergeManager_Run_WithSegments(t *testing.T) {
	t.Helper()
	dataDir := t.TempDir()
	camDir := filepath.Join(dataDir, "test-cam")
	os.MkdirAll(camDir, 0o755)

	// Create segment directories with dummy frames for Go merge.
	segDir1 := filepath.Join(dataDir, "seg-1")
	os.MkdirAll(segDir1, 0o755)
	os.WriteFile(filepath.Join(segDir1, "frame_000001.jpg"), []byte("dummy"), 0o644)

	segDir2 := filepath.Join(dataDir, "seg-2")
	os.MkdirAll(segDir2, 0o755)
	os.WriteFile(filepath.Join(segDir2, "frame_000002.jpg"), []byte("dummy"), 0o644)

	merger := &successMerger{delay: 10 * time.Millisecond}
	mgr := NewPeriodicMergeManager(&mockRecordingListerWithSegments{
		segments: []model.Recording{
			{ID: "seg-1", CameraID: "test-cam", FilePath: segDir1, Format: model.FormatTimelapse, MergeStatus: model.MergeStatusMerged},
			{ID: "seg-2", CameraID: "test-cam", FilePath: segDir2, Format: model.FormatTimelapse, MergeStatus: model.MergeStatusMerged},
		},
	}, newTrackDB(), merger, 10, dataDir, 8*time.Hour, nil)

	err := mgr.Run(context.Background(), "test-cam", time.Date(2025, 6, 7, 10, 30, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPeriodicMergeManager_Run_CancelledContext(t *testing.T) {
	t.Helper()
	dataDir := t.TempDir()
	segDir := filepath.Join(dataDir, "seg-1")
	os.MkdirAll(segDir, 0o755)
	os.WriteFile(filepath.Join(segDir, "frame_000001.jpg"), []byte("dummy"), 0o644)

	merger := &successMerger{delay: 1 * time.Second}
	mgr := NewPeriodicMergeManager(&mockRecordingListerWithSegments{
		segments: []model.Recording{
			{ID: "seg-1", CameraID: "test-cam", FilePath: segDir, Format: model.FormatTimelapse, MergeStatus: model.MergeStatusMerged},
		},
	}, newTrackDB(), merger, 10, dataDir, 24*time.Hour, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	err := mgr.Run(ctx, "test-cam", time.Date(2025, 6, 7, 10, 30, 0, 0, time.UTC))
	if err == nil {
		t.Fatal("expected error from cancelled context, got nil")
	}
}

// --- RetryCounts cleanup tests ---

func TestPeriodicRetryCountCleanup(t *testing.T) {
	t.Helper()
	dataDir := t.TempDir()

	mgr := NewPeriodicMergeManager(&mockRecordingLister{}, &mockMergeStatusUpdater{}, nil, 10, dataDir, 8*time.Hour, nil)

	// Manually insert a stale retryCounts entry for a non-existent segment.
	mgr.retryMu.Lock()
	mgr.retryCounts["non-existent-seg"] = retryInfo{
		count:     1,
		timestamp: time.Now().Add(-48 * time.Hour), // older than 24h cutoff
	}
	mgr.retryMu.Unlock()

	// Run merge — triggers filterEligibleSegments which cleans stale entries.
	err := mgr.Run(context.Background(), "test-cam", time.Date(2025, 6, 7, 10, 30, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify stale entry was removed.
	mgr.retryMu.Lock()
	_, exists := mgr.retryCounts["non-existent-seg"]
	mgr.retryMu.Unlock()
	if exists {
		t.Error("expected stale retryCounts entry to be cleaned")
	}
}

// --- Progress tracking tests ---

func TestPeriodicMergeProgress(t *testing.T) {
	t.Helper()
	dataDir := t.TempDir()
	camDir := filepath.Join(dataDir, "test-cam")
	os.MkdirAll(camDir, 0o755)

	// Create 3 segments with dummy frames for Go merge.
	segDir1 := filepath.Join(dataDir, "seg-1")
	os.MkdirAll(segDir1, 0o755)
	os.WriteFile(filepath.Join(segDir1, "frame_000001.jpg"), []byte("dummy"), 0o644)
	os.WriteFile(filepath.Join(segDir1, "frame_000002.jpg"), []byte("dummy"), 0o644)

	segDir2 := filepath.Join(dataDir, "seg-2")
	os.MkdirAll(segDir2, 0o755)
	os.WriteFile(filepath.Join(segDir2, "frame_000003.jpg"), []byte("dummy"), 0o644)

	segDir3 := filepath.Join(dataDir, "seg-3")
	os.MkdirAll(segDir3, 0o755)
	os.WriteFile(filepath.Join(segDir3, "frame_000004.jpg"), []byte("dummy"), 0o644)

	db := newTrackDB()
	merger := &successMerger{delay: 5 * time.Millisecond}
	mgr := NewPeriodicMergeManager(&mockRecordingListerWithSegments{
		segments: []model.Recording{
			{ID: "seg-1", CameraID: "test-cam", FilePath: segDir1, Format: model.FormatTimelapse, MergeStatus: model.MergeStatusMerged},
			{ID: "seg-2", CameraID: "test-cam", FilePath: segDir2, Format: model.FormatTimelapse, MergeStatus: model.MergeStatusMerged},
			{ID: "seg-3", CameraID: "test-cam", FilePath: segDir3, Format: model.FormatTimelapse, MergeStatus: model.MergeStatusMerged},
		},
	}, db, merger, 10, dataDir, 8*time.Hour, nil)

	// Run merge which should produce progress updates.
	err := mgr.Run(context.Background(), "test-cam", time.Date(2025, 6, 7, 10, 30, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify progress was tracked for all segments and final is 100.
	db.mu.Lock()
	defer db.mu.Unlock()
	for _, id := range []string{"seg-1", "seg-2", "seg-3"} {
		p, ok := db.progress[id]
		if !ok {
			t.Errorf("expected progress entry for %s", id)
			continue
		}
		if p != 100 {
			t.Errorf("expected final progress 100 for %s, got %d", id, p)
		}
	}
}

// --- Retry exhaustion tests ---

func TestRetryExhaustion_PermanentFailure(t *testing.T) {
	t.Helper()
	dataDir := t.TempDir()
	db := newTrackDB()
	mgr := NewPeriodicMergeManager(&mockRecordingLister{}, db, &errorMerger{}, 10, dataDir, 8*time.Hour, nil)

	segs := []model.Recording{
		{ID: "seg-1", CameraID: "test-cam", FilePath: dataDir, Format: model.FormatTimelapse},
	}

	// 3 failures to exhaust retries.
	for i := range 3 {
		if err := mgr.markMergeFailed(context.Background(), segs, fmt.Errorf("error %d", i+1)); err != nil {
			t.Fatalf("markMergeFailed failed on attempt %d: %v", i+1, err)
		}
	}

	// Verify retryCounts reached 3.
	mgr.retryMu.Lock()
	info, ok := mgr.retryCounts["seg-1"]
	mgr.retryMu.Unlock()
	if !ok {
		t.Fatal("expected retryCounts entry for seg-1")
	}
	if info.count != 3 {
		t.Errorf("expected retry count 3, got %d", info.count)
	}

	// Verify filterEligibleSegments excludes the permanently failed segment.
	eligible := mgr.filterEligibleSegments([]model.Recording{
		{ID: "seg-1", CameraID: "test-cam", FilePath: dataDir, Format: model.FormatTimelapse, MergeStatus: model.MergeStatusFailed},
	})
	if len(eligible) != 0 {
		t.Errorf("expected 0 eligible segments after 3 failures, got %d", len(eligible))
	}
}

func TestRetryExhaustion_NotRetried(t *testing.T) {
	t.Helper()
	dataDir := t.TempDir()
	db := newTrackDB()

	// Segment starts as MergeStatusFailed (from previous failed attempts).
	mgr := NewPeriodicMergeManager(&mockRecordingListerWithSegments{
		segments: []model.Recording{
			{ID: "seg-1", CameraID: "test-cam", FilePath: dataDir, Format: model.FormatTimelapse, MergeStatus: model.MergeStatusFailed},
		},
	}, db, nil, 10, dataDir, 8*time.Hour, nil)

	// Simulate 3 retries already exhausted.
	mgr.retryMu.Lock()
	mgr.retryCounts["seg-1"] = retryInfo{count: 3, timestamp: time.Now()}
	mgr.retryMu.Unlock()

	// Run should return nil because filterEligibleSegments excludes seg-1 (count >= 3).
	err := mgr.Run(context.Background(), "test-cam", time.Date(2025, 6, 7, 10, 30, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify segment was NOT processed.
	if got := db.GetStatus("seg-1"); got == "daily_merged" {
		t.Error("segment should not have been merged after permanent failure")
	}
}

func TestRetryExhaustion_Recovery(t *testing.T) {
	t.Helper()
	dataDir := t.TempDir()
	db := newTrackDB()
	mgr := NewPeriodicMergeManager(&mockRecordingLister{}, db, &errorMerger{}, 10, dataDir, 8*time.Hour, nil)

	segs := []model.Recording{
		{ID: "seg-1", CameraID: "test-cam", FilePath: dataDir, Format: model.FormatTimelapse},
	}

	// 2 failures — retries still remaining.
	for i := range 2 {
		if err := mgr.markMergeFailed(context.Background(), segs, fmt.Errorf("error %d", i+1)); err != nil {
			t.Fatalf("markMergeFailed failed on attempt %d: %v", i+1, err)
		}
	}

	// Verify count is 2.
	mgr.retryMu.Lock()
	info, ok := mgr.retryCounts["seg-1"]
	mgr.retryMu.Unlock()
	if !ok {
		t.Fatal("expected retryCounts entry for seg-1")
	}
	if info.count != 2 {
		t.Errorf("expected retry count 2, got %d", info.count)
	}

	// Now simulate successful merge — finalizeMerge should clear retryCounts.
	if err := mgr.finalizeMerge(context.Background(), segs, "/tmp/output.mp4"); err != nil {
		t.Fatalf("finalizeMerge failed: %v", err)
	}

	// Verify retryCounts was cleaned up.
	mgr.retryMu.Lock()
	_, exists := mgr.retryCounts["seg-1"]
	mgr.retryMu.Unlock()
	if exists {
		t.Error("expected retryCounts entry to be cleared after successful recovery")
	}

	// Verify segment was marked as merged in DB.
	if got := db.GetStatus("seg-1"); got != "daily_merged" {
		t.Errorf("expected status 'daily_merged', got %q", got)
	}
}

// --- Edge case tests ---

func TestPeriodicMergeManager_Run_DiskFull(t *testing.T) {
	t.Helper()
	dataDir := t.TempDir()
	camDir := filepath.Join(dataDir, "test-cam")
	os.MkdirAll(camDir, 0o755)

	// Make the camera output directory read-only to simulate disk full / ENOSPC.
	os.Chmod(camDir, 0o444)
	t.Cleanup(func() { os.Chmod(camDir, 0o755) })

	// Create a segment directory with frames.
	segDir := filepath.Join(dataDir, "seg-1")
	os.MkdirAll(segDir, 0o755)
	os.WriteFile(filepath.Join(segDir, "frame_000001.jpg"), []byte("dummy"), 0o644)

	merger := &successMerger{delay: 10 * time.Millisecond}
	mgr := NewPeriodicMergeManager(&mockRecordingListerWithSegments{
		segments: []model.Recording{
			{ID: "seg-1", CameraID: "test-cam", FilePath: segDir, Format: model.FormatTimelapse, MergeStatus: model.MergeStatusMerged},
		},
	}, newTrackDB(), merger, 10, dataDir, 8*time.Hour, nil)

	err := mgr.Run(context.Background(), "test-cam", time.Date(2025, 6, 7, 10, 30, 0, 0, time.UTC))
	if err == nil {
		t.Fatal("expected error from disk-full-like scenario, got nil")
	}
	t.Logf("disk full error (expected): %v", err)
}

func TestPeriodicMergeManager_Run_CorruptedSegment(t *testing.T) {
	t.Helper()
	dataDir := t.TempDir()
	camDir := filepath.Join(dataDir, "test-cam")
	os.MkdirAll(camDir, 0o755)

	// Segment FilePath points to a non-existent path — simulates a corrupted/missing segment.
	segPath := filepath.Join(dataDir, "missing-segment")

	merger := &successMerger{delay: 10 * time.Millisecond}
	mgr := NewPeriodicMergeManager(&mockRecordingListerWithSegments{
		segments: []model.Recording{
			{ID: "seg-1", CameraID: "test-cam", FilePath: segPath, Format: model.FormatTimelapse, MergeStatus: model.MergeStatusMerged},
		},
	}, newTrackDB(), merger, 10, dataDir, 8*time.Hour, nil)

	err := mgr.Run(context.Background(), "test-cam", time.Date(2025, 6, 7, 10, 30, 0, 0, time.UTC))
	if err == nil {
		t.Fatal("expected error from corrupted/missing segment, got nil")
	}
	t.Logf("corrupted segment error (expected): %v", err)
}

func TestPeriodicMergeManager_Run_AllFailedSegments(t *testing.T) {
	t.Helper()
	dataDir := t.TempDir()

	// All segments have MergeStatusFailed with no retry entries → filterEligibleSegments returns empty.
	mgr := NewPeriodicMergeManager(&mockRecordingListerWithSegments{
		segments: []model.Recording{
			{ID: "seg-1", CameraID: "test-cam", FilePath: "/tmp/nonexistent", Format: model.FormatTimelapse, MergeStatus: model.MergeStatusFailed},
		},
	}, &mockMergeStatusUpdater{}, nil, 10, dataDir, 8*time.Hour, nil)

	err := mgr.Run(context.Background(), "test-cam", time.Date(2025, 6, 7, 10, 30, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("expected nil for all-failed segments with no retries, got: %v", err)
	}
}

func TestPeriodicMergeManager_Run_MixedFormatSegments(t *testing.T) {
	t.Helper()
	dataDir := t.TempDir()

	// Create 2 segments with different recording formats.
	segDir1 := filepath.Join(dataDir, "seg-1")
	os.MkdirAll(segDir1, 0o755)
	os.WriteFile(filepath.Join(segDir1, "frame_000001.jpg"), []byte("dummy"), 0o644)

	segDir2 := filepath.Join(dataDir, "seg-2")
	os.MkdirAll(segDir2, 0o755)
	os.WriteFile(filepath.Join(segDir2, "frame_000002.jpg"), []byte("dummy"), 0o644)

	db := newTrackDB()
	merger := &successMerger{delay: 10 * time.Millisecond}
	mgr := NewPeriodicMergeManager(&mockRecordingListerWithSegments{
		segments: []model.Recording{
			{ID: "seg-1", CameraID: "test-cam", FilePath: segDir1, Format: model.FormatH264, MergeStatus: model.MergeStatusMerged},
			{ID: "seg-2", CameraID: "test-cam", FilePath: segDir2, Format: model.FormatH265, MergeStatus: model.MergeStatusMerged},
		},
	}, db, merger, 10, dataDir, 8*time.Hour, nil)

	err := mgr.Run(context.Background(), "test-cam", time.Date(2025, 6, 7, 10, 30, 0, 0, time.UTC))
	// Should succeed via Go merge fallback — format differences don't affect Go merge.
	if err != nil {
		t.Fatalf("expected mixed format merge to succeed via Go fallback: %v", err)
	}
}

// --- Recording-enabled frame extraction tests ---

// mockRecordingListerMultiFormat returns different segments for timelapse vs
// video-format queries, enabling tests of the recording_enabled extraction path.
type mockRecordingListerMultiFormat struct {
	timelapseSegments []model.Recording
	videoSegments     []model.Recording
}

func (m *mockRecordingListerMultiFormat) ListRecordings(_ context.Context, filter model.RecordingFilter) ([]model.Recording, error) {
	if filter.Format == model.FormatTimelapse {
		return m.timelapseSegments, nil
	}
	if len(filter.Formats) > 0 {
		return m.videoSegments, nil
	}
	return nil, nil
}

func TestPeriodicMerge_RecordingEnabled_ProviderOption(t *testing.T) {
	t.Helper()
	dataDir := t.TempDir()
	cameraID := "test-cam"

	providerCalled := false
	provider := func(cameraID string) bool {
		providerCalled = true
		return true
	}

	mgr := NewPeriodicMergeManager(
		&mockRecordingListerMultiFormat{},
		&mockMergeStatusUpdater{},
		nil, 10, dataDir, 8*time.Hour, nil,
		WithRecordingEnabledProvider(provider),
	)

	// Run with recording_enabled=true but no segments of any type.
	err := mgr.Run(context.Background(), cameraID, time.Date(2025, 6, 7, 10, 30, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("unexpected error with no segments: %v", err)
	}
	if !providerCalled {
		t.Error("expected recordingEnabledProvider to be called")
	}
	// Verify that audio from providerCalled test is correct
	if mgr.Duration() != 8*time.Hour {
		t.Errorf("expected Duration 8h, got %v", mgr.Duration())
	}
}

func TestPeriodicMerge_RecordingEnabled_True(t *testing.T) {
	t.Helper()
	dataDir := t.TempDir()
	cameraID := "test-cam"

	// Create a timelapse segment with dummy frames (2 segments to avoid the
	// single-segment-copy path in runMergePipeline when FilePath is a directory).
	tlDir := filepath.Join(dataDir, "tl-seg")
	os.MkdirAll(tlDir, 0o755)
	os.WriteFile(filepath.Join(tlDir, "frame_000001.jpg"), []byte("dummy"), 0o644)
	os.WriteFile(filepath.Join(tlDir, "frame_000002.jpg"), []byte("dummy"), 0o644)

	mockLister := &mockRecordingListerMultiFormat{
		timelapseSegments: []model.Recording{
			{ID: "tl-1", CameraID: cameraID, FilePath: tlDir, Format: model.FormatTimelapse, MergeStatus: model.MergeStatusMerged},
			{ID: "tl-2", CameraID: cameraID, FilePath: tlDir, Format: model.FormatTimelapse, MergeStatus: model.MergeStatusMerged},
		},

		videoSegments: []model.Recording{
			{ID: "vid-avi", CameraID: cameraID, FilePath: filepath.Join(dataDir, "dummy.avi"), Format: model.FormatAVI},
		},
	}

	db := newTrackDB()
	merger := &successMerger{delay: 5 * time.Millisecond}
	mgr := NewPeriodicMergeManager(
		mockLister, db, merger, 10, dataDir, 8*time.Hour, nil,
		WithRecordingEnabledProvider(func(cameraID string) bool { return true }),
	)

	// Run with recording_enabled=true. The AVI recording will fail extraction
	// (dummy file is not valid AVI), but the timelapse segment should merge
	// successfully.
	err := mgr.Run(context.Background(), cameraID, time.Date(2025, 6, 7, 10, 30, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify timelapse segments were processed (merged).
	if status := db.GetStatus("tl-1"); status != "daily_merged" && status != "merged" {
		t.Errorf("expected tl-1 status daily_merged or merged, got %q", status)
	}
	if status := db.GetStatus("tl-2"); status != "daily_merged" && status != "merged" {
		t.Errorf("expected tl-2 status daily_merged or merged, got %q", status)
	}
}

func TestPeriodicMerge_RecordingEnabled_False(t *testing.T) {
	t.Helper()
	dataDir := t.TempDir()
	cameraID := "test-cam"
	cameraDir := filepath.Join(dataDir, cameraID)
	os.MkdirAll(cameraDir, 0o755)

	// Create 2 timelapse segments with dummy frames to avoid the
	// single-segment-copy path in runMergePipeline.
	tlDir := filepath.Join(dataDir, "tl-seg")
	os.MkdirAll(tlDir, 0o755)
	os.WriteFile(filepath.Join(tlDir, "frame_000001.jpg"), []byte("dummy"), 0o644)
	os.WriteFile(filepath.Join(tlDir, "frame_000002.jpg"), []byte("dummy"), 0o644)

	// When recording_enabled=false, only timelapse segments are merged (original behavior).
	// The provider returns false, so extractRecordingFrames is never called.
	providerInvoked := false
	db := newTrackDB()
	merger := &successMerger{delay: 5 * time.Millisecond}
	mgr := NewPeriodicMergeManager(
		&mockRecordingListerWithSegments{
			segments: []model.Recording{
				{ID: "tl-1", CameraID: cameraID, FilePath: tlDir, Format: model.FormatTimelapse, MergeStatus: model.MergeStatusMerged},
				{ID: "tl-2", CameraID: cameraID, FilePath: tlDir, Format: model.FormatTimelapse, MergeStatus: model.MergeStatusMerged},
			},
		},
		db, merger, 10, dataDir, 8*time.Hour, nil,
		WithRecordingEnabledProvider(func(cameraID string) bool {
			providerInvoked = true
			return false
		}),
	)

	err := mgr.Run(context.Background(), cameraID, time.Date(2025, 6, 7, 10, 30, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !providerInvoked {
		t.Error("expected recordingEnabledProvider to be called")
	}
	// Verify timelapse segments were processed (original path unchanged).
	if status := db.GetStatus("tl-1"); status != "daily_merged" && status != "merged" {
		t.Errorf("expected tl-1 status daily_merged or merged, got %q", status)
	}
	if status := db.GetStatus("tl-2"); status != "daily_merged" && status != "merged" {
		t.Errorf("expected tl-2 status daily_merged or merged, got %q", status)
	}
}

func TestPeriodicMerge_MixedSources(t *testing.T) {
	t.Helper()
	dataDir := t.TempDir()
	cameraID := "test-cam"
	cameraDir := filepath.Join(dataDir, cameraID)
	os.MkdirAll(cameraDir, 0o755)

	// Create a timelapse segment with dummy frames.
	tlDir := filepath.Join(dataDir, "tl-seg")
	os.MkdirAll(tlDir, 0o755)
	os.WriteFile(filepath.Join(tlDir, "frame_000001.jpg"), []byte("dummy"), 0o644)
	os.WriteFile(filepath.Join(tlDir, "frame_000002.jpg"), []byte("dummy"), 0o644)

	// Mixed sources: timelapse segments + video recordings (H264 + AVI).
	// Video recordings point to non-existent files — extraction will fail
	// gracefully and be logged as warnings. Timelapse segments should still
	// be merged successfully.
	mockLister := &mockRecordingListerMultiFormat{
		timelapseSegments: []model.Recording{
			{ID: "tl-1", CameraID: cameraID, FilePath: tlDir, Format: model.FormatTimelapse, MergeStatus: model.MergeStatusMerged},
			{ID: "tl-2", CameraID: cameraID, FilePath: tlDir, Format: model.FormatTimelapse, MergeStatus: model.MergeStatusMerged},
		},
		videoSegments: []model.Recording{
			{ID: "vid-h264", CameraID: cameraID, FilePath: filepath.Join(dataDir, "rec.h264"), Format: model.FormatH264},
			{ID: "vid-avi", CameraID: cameraID, FilePath: filepath.Join(dataDir, "rec.avi"), Format: model.FormatAVI},
			{ID: "vid-mjpeg", CameraID: cameraID, FilePath: filepath.Join(dataDir, "rec.mjpeg"), Format: model.FormatMJPEG},
		},
	}

	db := newTrackDB()
	merger := &successMerger{delay: 5 * time.Millisecond}
	mgr := NewPeriodicMergeManager(
		mockLister, db, merger, 10, dataDir, 8*time.Hour, nil,
		WithRecordingEnabledProvider(func(cameraID string) bool { return true }),
	)

	err := mgr.Run(context.Background(), cameraID, time.Date(2025, 6, 7, 10, 30, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("unexpected error with mixed sources: %v", err)
	}

	// Verify timelapse segments were processed.
	if status := db.GetStatus("tl-1"); status != "daily_merged" && status != "merged" {
		t.Errorf("expected tl-1 status 'daily_merged' or 'merged', got %q", status)
	}
	if status := db.GetStatus("tl-2"); status != "daily_merged" && status != "merged" {
		t.Errorf("expected tl-2 status 'daily_merged' or 'merged', got %q", status)
	}
}
