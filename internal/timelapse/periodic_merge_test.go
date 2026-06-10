package timelapse

import (
"context"
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

	expectedStart := time.Date(2025, 6, 2, 0, 0, 0, 0, time.UTC)  // Monday
	expectedEnd := time.Date(2025, 6, 9, 0, 0, 0, 0, time.UTC)    // next Monday

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
	os.MkdirAll(camDir, 0755)

	// Create segment directories with dummy frames for Go merge.
	segDir1 := filepath.Join(dataDir, "seg-1")
	os.MkdirAll(segDir1, 0755)
	os.WriteFile(filepath.Join(segDir1, "frame_000001.jpg"), []byte("dummy"), 0644)

	segDir2 := filepath.Join(dataDir, "seg-2")
	os.MkdirAll(segDir2, 0755)
	os.WriteFile(filepath.Join(segDir2, "frame_000002.jpg"), []byte("dummy"), 0644)

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
	os.MkdirAll(segDir, 0755)
	os.WriteFile(filepath.Join(segDir, "frame_000001.jpg"), []byte("dummy"), 0644)

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
