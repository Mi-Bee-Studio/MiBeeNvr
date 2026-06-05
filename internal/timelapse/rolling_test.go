package timelapse

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
	)

// mockDB implements MergeStatusUpdater for testing.
type mockDB struct {
	mu      sync.Mutex
	statuses map[string]string
}

func newMockDB() *mockDB {
	return &mockDB{statuses: make(map[string]string)}
}

func (m *mockDB) SetMergeStatus(_ context.Context, ids []string, status string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range ids {
		m.statuses[id] = status
	}
	return nil
}

func (m *mockDB) GetStatus(id string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.statuses[id]
}

// slowMerger is a mock TimelapseMerger that simulates a slow merge.
type slowMerger struct {
	delay time.Duration
}

func (s *slowMerger) CanMerge() bool { return true }
func (s *slowMerger) Tier() MergeTier { return TierGo }
func (s *slowMerger) Merge(ctx context.Context, _, _ string, _ int) (*MergeResult, error) {
	select {
	case <-time.After(s.delay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return &MergeResult{Tier: TierGo, FramesMerged: 10, Duration: 1.0}, nil
}

func TestRollingMergeManager_StartStopLifecycle(t *testing.T) {
	db := newMockDB()
	merger := &slowMerger{delay: 50 * time.Millisecond}
	mgr := NewRollingMergeManager(merger, db, 10)

	tmpDir := t.TempDir()
	segmentDir := filepath.Join(tmpDir, "segment")
	os.MkdirAll(segmentDir, 0755)
	outputPath := filepath.Join(tmpDir, "output.mp4")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start a merge.
	mgr.StartSegmentMerge(ctx, "cam-1", segmentDir, outputPath)

	// Should be active immediately.
	if !mgr.IsActive("cam-1") {
		t.Fatal("expected cam-1 to be active")
	}
	if mgr.ActiveCount() != 1 {
		t.Fatalf("expected 1 active merge, got %d", mgr.ActiveCount())
	}

	// Stop the merge.
	mgr.StopSegmentMerge("cam-1")

	// Should no longer be active.
	if mgr.IsActive("cam-1") {
		t.Fatal("expected cam-1 to be inactive after stop")
	}
	if mgr.ActiveCount() != 0 {
		t.Fatalf("expected 0 active merges, got %d", mgr.ActiveCount())
	}
}

func TestRollingMergeManager_AsyncDoesNotBlock(t *testing.T) {
	db := newMockDB()
	// Use a very slow merger to prove StartSegmentMerge is non-blocking.
	merger := &slowMerger{delay: 500 * time.Millisecond}
	mgr := NewRollingMergeManager(merger, db, 10)

	tmpDir := t.TempDir()
	segmentDir := filepath.Join(tmpDir, "segment")
	os.MkdirAll(segmentDir, 0755)
	outputPath := filepath.Join(tmpDir, "output.mp4")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// StartSegmentMerge should return immediately without blocking.
	start := time.Now()
	mgr.StartSegmentMerge(ctx, "cam-2", segmentDir, outputPath)
	elapsed := time.Since(start)

	if elapsed > 50*time.Millisecond {
		t.Fatalf("StartSegmentMerge blocked for %v, expected non-blocking", elapsed)
	}

	// Clean up.
	mgr.StopSegmentMerge("cam-2")
}

func TestRollingMergeManager_StopAll(t *testing.T) {
	db := newMockDB()
	merger := &slowMerger{delay: 500 * time.Millisecond}
	mgr := NewRollingMergeManager(merger, db, 10)

	tmpDir := t.TempDir()
	segmentDir := filepath.Join(tmpDir, "segment")
	os.MkdirAll(segmentDir, 0755)
	outputPath := filepath.Join(tmpDir, "output.mp4")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start multiple merges.
	mgr.StartSegmentMerge(ctx, "cam-a", segmentDir, outputPath)
	mgr.StartSegmentMerge(ctx, "cam-b", segmentDir, outputPath)
	mgr.StartSegmentMerge(ctx, "cam-c", segmentDir, outputPath)

	if mgr.ActiveCount() != 3 {
		t.Fatalf("expected 3 active merges, got %d", mgr.ActiveCount())
	}

	// Stop all.
	mgr.StopAll()

	if mgr.ActiveCount() != 0 {
		t.Fatalf("expected 0 active merges after StopAll, got %d", mgr.ActiveCount())
	}
}

func TestRollingMergeManager_ReplaceActiveMerge(t *testing.T) {
	db := newMockDB()
	merger := &slowMerger{delay: 500 * time.Millisecond}
	mgr := NewRollingMergeManager(merger, db, 10)

	tmpDir := t.TempDir()
	segmentDir := filepath.Join(tmpDir, "segment")
	os.MkdirAll(segmentDir, 0755)
	outputPath := filepath.Join(tmpDir, "output.mp4")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start a merge for cam-1.
	mgr.StartSegmentMerge(ctx, "cam-1", segmentDir, outputPath)

	// Start another merge for the same camera — should replace the old one.
	mgr.StartSegmentMerge(ctx, "cam-1", segmentDir, outputPath)

	// Still only one active merge.
	if mgr.ActiveCount() != 1 {
		t.Fatalf("expected 1 active merge after replacement, got %d", mgr.ActiveCount())
	}

	mgr.StopAll()
}

func TestRollingMergeManager_MergeCompletes(t *testing.T) {
	db := newMockDB()
	// Fast merger so the test completes quickly.
	merger := &slowMerger{delay: 10 * time.Millisecond}
	mgr := NewRollingMergeManager(merger, db, 10)

	tmpDir := t.TempDir()
	segmentDir := filepath.Join(tmpDir, "segment")
	os.MkdirAll(segmentDir, 0755)
	outputPath := filepath.Join(tmpDir, "output.mp4")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr.StartSegmentMerge(ctx, "cam-3", segmentDir, outputPath)

	// Wait for the merge to complete.
	time.Sleep(200 * time.Millisecond)

	// After completion, the goroutine should have removed itself.
	if mgr.IsActive("cam-3") {
		t.Fatal("expected cam-3 to be inactive after merge completion")
	}
}

func TestRollingMergeManager_ContextCancellation(t *testing.T) {
	db := newMockDB()
	// Very slow merger.
	merger := &slowMerger{delay: 5 * time.Second}
	mgr := NewRollingMergeManager(merger, db, 10)

	tmpDir := t.TempDir()
	segmentDir := filepath.Join(tmpDir, "segment")
	os.MkdirAll(segmentDir, 0755)
	outputPath := filepath.Join(tmpDir, "output.mp4")

	ctx, cancel := context.WithCancel(context.Background())

	mgr.StartSegmentMerge(ctx, "cam-4", segmentDir, outputPath)

	// Cancel the context immediately.
	cancel()

	// Wait a bit for the goroutine to process the cancellation.
	time.Sleep(100 * time.Millisecond)

	// The goroutine should have removed itself.
	if mgr.IsActive("cam-4") {
		t.Fatal("expected cam-4 to be inactive after context cancellation")
	}
}
