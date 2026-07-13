package timelapse

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

type mockRecordingLister struct{}

func (m *mockRecordingLister) ListRecordings(ctx context.Context, filter model.RecordingFilter) ([]model.Recording, error) {
	return nil, nil
}

type mockMergeStatusUpdater struct{}

func (m *mockMergeStatusUpdater) SetMergeStatus(ctx context.Context, ids []string, status string) error {
	return nil
}

func (m *mockMergeStatusUpdater) SetMergeResult(_ context.Context, id, mergePath, mergeTier string) error {
	return nil
}

func (m *mockMergeStatusUpdater) SetMergeError(_ context.Context, ids []string, mergeError string) error {
	return nil
}

func (m *mockMergeStatusUpdater) UpdateMergeProgress(_ context.Context, _ string, _ int) error {
	return nil
}

func (m *mockMergeStatusUpdater) UpdateMergeProgressBatch(_ context.Context, _ []string, _ int) error {
	return nil
}

func TestNewDailyMergeManager(t *testing.T) {
	t.Helper()
	m := NewDailyMergeManager(&mockRecordingLister{}, &mockMergeStatusUpdater{}, nil, 10, "/tmp/test-data", nil)
	if m == nil {
		t.Fatal("expected non-nil DailyMergeManager")
	}
}

func TestDailyMergeManager_Run(t *testing.T) {
	t.Helper()
	m := NewDailyMergeManager(&mockRecordingLister{}, &mockMergeStatusUpdater{}, nil, 10, "/tmp/test-data", nil)
	err := m.Run(context.Background(), "test-cam", "2026-01-01")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// TestDailyWire — integration wiring tests for DailyMergeManager and RollingMergeManager.
// ---------------------------------------------------------------------------

// mockRecordingListerWithSegments returns a fixed set of segments for testing.
type mockRecordingListerWithSegments struct {
	segments []model.Recording
}

func (m *mockRecordingListerWithSegments) ListRecordings(_ context.Context, filter model.RecordingFilter) ([]model.Recording, error) {
	return m.segments, nil
}

// trackDB records all merge status updates for verification.
type trackDB struct {
	mu       sync.Mutex
	statuses map[string]string                      // recordingID -> status
	errors   map[string]string                      // recordingID -> error message
	results  map[string]struct{ path, tier string } // recordingID -> result
	progress map[string]int                         // recordingID -> progress
}

func newTrackDB() *trackDB {
	return &trackDB{
		statuses: make(map[string]string),
		errors:   make(map[string]string),
		results:  make(map[string]struct{ path, tier string }),
		progress: make(map[string]int),
	}
}

func (d *trackDB) SetMergeStatus(_ context.Context, ids []string, status string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, id := range ids {
		d.statuses[id] = status
	}
	return nil
}

func (d *trackDB) SetMergeResult(_ context.Context, id, mergePath, mergeTier string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.statuses[id] = "merged"
	d.results[id] = struct{ path, tier string }{mergePath, mergeTier}
	return nil
}

func (d *trackDB) SetMergeError(_ context.Context, ids []string, mergeError string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, id := range ids {
		d.statuses[id] = "failed"
		d.errors[id] = mergeError
	}
	return nil
}

func (d *trackDB) UpdateMergeProgress(ctx context.Context, id string, progress int) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.progress[id] = progress
	if progress >= 100 {
		d.statuses[id] = "merged"
	} else if progress > 0 {
		d.statuses[id] = "merging"
	} else {
		d.statuses[id] = "pending"
	}
	return nil
}

func (d *trackDB) UpdateMergeProgressBatch(ctx context.Context, ids []string, progress int) error {
	for _, id := range ids {
		if err := d.UpdateMergeProgress(ctx, id, progress); err != nil {
			return err
		}
	}
	return nil
}

func (d *trackDB) GetStatus(id string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.statuses[id]
}

func (d *trackDB) GetError(id string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.errors[id]
}

func (d *trackDB) GetResult(id string) (path, tier string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	r, ok := d.results[id]
	if !ok {
		return "", ""
	}
	return r.path, r.tier
}

// errorMerger is a mock TimelapseMerger that always returns an error.
type errorMerger struct{}

func (e *errorMerger) CanMerge() bool  { return true }
func (e *errorMerger) Tier() MergeTier { return TierGo }
func (e *errorMerger) Merge(_ context.Context, _, _ string, _ int) (*MergeResult, error) {
	return nil, fmt.Errorf("merge failed: test error")
}

// successMerger is a mock TimelapseMerger that always succeeds.
type successMerger struct{ delay time.Duration }

func (s *successMerger) CanMerge() bool  { return true }
func (s *successMerger) Tier() MergeTier { return TierGo }
func (s *successMerger) Merge(ctx context.Context, _, outputPath string, _ int) (*MergeResult, error) {
	select {
	case <-time.After(s.delay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	// Create a dummy output file so post-merge verification passes.
	if outputPath != "" {
		os.WriteFile(outputPath, []byte("merged"), 0o644)
	}
	return &MergeResult{Tier: TierGo, FramesMerged: 10, Duration: 5.0, OutputPath: outputPath}, nil
}

func TestDailyWire_DailyMergeRun(t *testing.T) {
	t.Helper()
	// Create a mock lister that returns segments for a date.
	mockLister := &mockRecordingListerWithSegments{}
	updater := &mockMergeStatusUpdater{}
	mgr := NewDailyMergeManager(mockLister, updater, nil, 10, t.TempDir(), nil)
	err := mgr.Run(context.Background(), "test-cam", "2026-06-06")
	// No segments should be found — Run should succeed without error.
	if err != nil {
		t.Fatalf("DailyMergeManager.Run failed: %v", err)
	}
}

func TestDailyWire_RollingMergeDBUpdate(t *testing.T) {
	t.Helper()
	db := newTrackDB()
	merger := &successMerger{delay: 10 * time.Millisecond}
	mgr := NewRollingMergeManager(merger, db, 10, false)

	tmpDir := t.TempDir()
	segmentDir := filepath.Join(tmpDir, "segment")
	os.MkdirAll(segmentDir, 0o755)
	outputPath := filepath.Join(tmpDir, "output.mp4")

	// Create a dummy file so the merger has something to process.
	os.WriteFile(filepath.Join(segmentDir, "frame_000001.jpg"), []byte("dummy"), 0o644)

	recordingID := "test-recording-001"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr.StartSegmentMerge(ctx, "cam-test", segmentDir, outputPath, recordingID)

	// Wait for merge to complete.
	time.Sleep(200 * time.Millisecond)

	// Verify DB status updated to 'merged'.
	if status := db.GetStatus(recordingID); status != "merged" {
		t.Errorf("expected merge_status 'merged', got %q", status)
	}

	// Verify merge_path and merge_tier.
	path, tier := db.GetResult(recordingID)
	if path != outputPath {
		t.Errorf("expected merge_path %q, got %q", outputPath, path)
	}
	if tier != "go" {
		t.Errorf("expected merge_tier 'go', got %q", tier)
	}
}

func TestDailyWire_RollingMergeDBFailure(t *testing.T) {
	t.Helper()
	db := newTrackDB()
	merger := &errorMerger{}
	mgr := NewRollingMergeManager(merger, db, 10, false)

	tmpDir := t.TempDir()
	segmentDir := filepath.Join(tmpDir, "segment")
	os.MkdirAll(segmentDir, 0o755)
	outputPath := filepath.Join(tmpDir, "output.mp4")

	recordingID := "test-recording-002"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr.StartSegmentMerge(ctx, "cam-fail", segmentDir, outputPath, recordingID)

	// Wait for merge to fail.
	time.Sleep(200 * time.Millisecond)

	// Verify DB status updated to 'failed'.
	if status := db.GetStatus(recordingID); status != "failed" {
		t.Errorf("expected merge_status 'failed', got %q", status)
	}

	// Verify merge_error is set.
	if errMsg := db.GetError(recordingID); errMsg == "" {
		t.Error("expected merge_error to be set, got empty string")
	}
}
