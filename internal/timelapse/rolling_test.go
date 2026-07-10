package timelapse

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// mockDB implements MergeStatusUpdater for testing.
type mockDB struct {
	mu       sync.Mutex
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

func (m *mockDB) SetMergeResult(_ context.Context, id, mergePath, mergeTier string) error {
	return nil
}

func (m *mockDB) SetMergeError(_ context.Context, ids []string, mergeError string) error {
	return nil
}

func (m *mockDB) UpdateMergeProgress(_ context.Context, _ string, _ int) error {
	return nil
}

func (m *mockDB) UpdateMergeProgressBatch(_ context.Context, _ []string, _ int) error {
	return nil
}

// slowMerger is a mock TimelapseMerger that simulates a slow merge.
type slowMerger struct {
	delay time.Duration
}

func (s *slowMerger) CanMerge() bool  { return true }
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
	mgr := NewRollingMergeManager(merger, db, 10, false)

	tmpDir := t.TempDir()
	segmentDir := filepath.Join(tmpDir, "segment")
	os.MkdirAll(segmentDir, 0o755)
	outputPath := filepath.Join(tmpDir, "output.mp4")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start a merge.
	mgr.StartSegmentMerge(ctx, "cam-1", segmentDir, outputPath, "")

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
	mgr := NewRollingMergeManager(merger, db, 10, false)

	tmpDir := t.TempDir()
	segmentDir := filepath.Join(tmpDir, "segment")
	os.MkdirAll(segmentDir, 0o755)
	outputPath := filepath.Join(tmpDir, "output.mp4")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// StartSegmentMerge should return immediately without blocking.
	start := time.Now()
	mgr.StartSegmentMerge(ctx, "cam-2", segmentDir, outputPath, "")
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
	mgr := NewRollingMergeManager(merger, db, 10, false)

	tmpDir := t.TempDir()
	segmentDir := filepath.Join(tmpDir, "segment")
	os.MkdirAll(segmentDir, 0o755)
	outputPath := filepath.Join(tmpDir, "output.mp4")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start multiple merges.
	mgr.StartSegmentMerge(ctx, "cam-a", segmentDir, outputPath, "")
	mgr.StartSegmentMerge(ctx, "cam-b", segmentDir, outputPath, "")
	mgr.StartSegmentMerge(ctx, "cam-c", segmentDir, outputPath, "")

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
	mgr := NewRollingMergeManager(merger, db, 10, false)

	tmpDir := t.TempDir()
	segmentDir := filepath.Join(tmpDir, "segment")
	os.MkdirAll(segmentDir, 0o755)
	outputPath := filepath.Join(tmpDir, "output.mp4")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start a merge for cam-1.
	mgr.StartSegmentMerge(ctx, "cam-1", segmentDir, outputPath, "")

	// Start another merge for the same camera — should replace the old one.
	mgr.StartSegmentMerge(ctx, "cam-1", segmentDir, outputPath, "")

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
	mgr := NewRollingMergeManager(merger, db, 10, false)

	tmpDir := t.TempDir()
	segmentDir := filepath.Join(tmpDir, "segment")
	os.MkdirAll(segmentDir, 0o755)
	outputPath := filepath.Join(tmpDir, "output.mp4")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr.StartSegmentMerge(ctx, "cam-3", segmentDir, outputPath, "")

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
	mgr := NewRollingMergeManager(merger, db, 10, false)

	tmpDir := t.TempDir()
	segmentDir := filepath.Join(tmpDir, "segment")
	os.MkdirAll(segmentDir, 0o755)
	outputPath := filepath.Join(tmpDir, "output.mp4")

	ctx, cancel := context.WithCancel(context.Background())

	mgr.StartSegmentMerge(ctx, "cam-4", segmentDir, outputPath, "")

	// Cancel the context immediately.
	cancel()

	// Wait a bit for the goroutine to process the cancellation.
	time.Sleep(100 * time.Millisecond)

	// The goroutine should have removed itself.
	if mgr.IsActive("cam-4") {
		t.Fatal("expected cam-4 to be inactive after context cancellation")
	}
}

// zeroFrameMerger returns a MergeResult with 0 FramesMerged.
type zeroFrameMerger struct {
	delay time.Duration
}

func (z *zeroFrameMerger) CanMerge() bool  { return true }
func (z *zeroFrameMerger) Tier() MergeTier { return TierGo }
func (z *zeroFrameMerger) Merge(ctx context.Context, _, _ string, _ int) (*MergeResult, error) {
	select {
	case <-time.After(z.delay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return &MergeResult{Tier: TierGo, FramesMerged: 0, Duration: 0}, nil
}

func TestDeleteOriginal_True(t *testing.T) {
	db := newMockDB()
	// Fast merger so test completes quickly.
	merger := &slowMerger{delay: 10 * time.Millisecond}
	// DeleteOriginal=true.
	mgr := NewRollingMergeManager(merger, db, 10, true)

	tmpDir := t.TempDir()
	segmentDir := filepath.Join(tmpDir, "segment")
	os.MkdirAll(segmentDir, 0o755)

	// Create some dummy source frame files.
	for i := range 3 {
		os.WriteFile(filepath.Join(segmentDir, fmt.Sprintf("frame_%06d.jpg", i+1)), []byte("dummy"), 0o644)
	}
	outputPath := filepath.Join(tmpDir, "output.mp4")

	// Verify files exist before merge.
	if _, err := os.Stat(segmentDir); os.IsNotExist(err) {
		t.Fatal("segment dir should exist before merge")
	}
	entries, err := os.ReadDir(segmentDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("segment dir should have files before merge")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr.StartSegmentMerge(ctx, "cam-del-true", segmentDir, outputPath, "")

	// Wait for merge to complete (10ms delay + 200ms buffer).
	time.Sleep(500 * time.Millisecond)

	// Verify files are deleted.
	if _, err := os.Stat(segmentDir); !os.IsNotExist(err) {
		t.Error("segment dir should be deleted after merge with DeleteOriginal=true")
	}
}

func TestDeleteOriginal_False(t *testing.T) {
	db := newMockDB()
	merger := &slowMerger{delay: 10 * time.Millisecond}
	// DeleteOriginal=false.
	mgr := NewRollingMergeManager(merger, db, 10, false)

	tmpDir := t.TempDir()
	segmentDir := filepath.Join(tmpDir, "segment")
	os.MkdirAll(segmentDir, 0o755)

	// Create some dummy source frame files.
	for i := range 3 {
		os.WriteFile(filepath.Join(segmentDir, fmt.Sprintf("frame_%06d.jpg", i+1)), []byte("dummy"), 0o644)
	}
	outputPath := filepath.Join(tmpDir, "output.mp4")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr.StartSegmentMerge(ctx, "cam-del-false", segmentDir, outputPath, "")

	// Wait for merge to complete.
	time.Sleep(500 * time.Millisecond)

	// Verify files are preserved.
	if _, err := os.Stat(segmentDir); os.IsNotExist(err) {
		t.Error("segment dir should not be deleted when DeleteOriginal=false")
	}
	entries, err := os.ReadDir(segmentDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Error("segment dir should still have files when DeleteOriginal=false")
	}
}

func TestDeleteOriginal_ZeroFrames(t *testing.T) {
	db := newMockDB()
	merger := &zeroFrameMerger{delay: 10 * time.Millisecond}
	// DeleteOriginal=true but merger returns 0 frames.
	mgr := NewRollingMergeManager(merger, db, 10, true)

	tmpDir := t.TempDir()
	segmentDir := filepath.Join(tmpDir, "segment")
	os.MkdirAll(segmentDir, 0o755)

	// Create some dummy source frame files.
	for i := range 3 {
		os.WriteFile(filepath.Join(segmentDir, fmt.Sprintf("frame_%06d.jpg", i+1)), []byte("dummy"), 0o644)
	}
	outputPath := filepath.Join(tmpDir, "output.mp4")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr.StartSegmentMerge(ctx, "cam-zero", segmentDir, outputPath, "")

	// Wait for merge to complete.
	time.Sleep(500 * time.Millisecond)

	// Verify files are NOT deleted (safety: only delete if FramesMerged > 0).
	if _, err := os.Stat(segmentDir); os.IsNotExist(err) {
		t.Error("segment dir should not be deleted when FramesMerged == 0")
	}
	entries, err := os.ReadDir(segmentDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Error("segment dir should still have files when FramesMerged == 0")
	}
}

func TestRollingMergeManager_ProgressCleanup(t *testing.T) {
	db := newMockDB()
	merger := &slowMerger{delay: 10 * time.Millisecond}
	mgr := NewRollingMergeManager(merger, db, 10, false)
	mgr.progressCleanupDelay = 100 * time.Millisecond

	tmpDir := t.TempDir()
	segmentDir := filepath.Join(tmpDir, "segment")
	os.MkdirAll(segmentDir, 0o755)
	outputPath := filepath.Join(tmpDir, "output.mp4")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr.StartSegmentMerge(ctx, "cam-cleanup", segmentDir, outputPath, "")

	// The merge goroutine sleeps 100ms before merging, so the merge completes
	// at roughly t=110ms. With a 100ms cleanup delay, cleanup fires at ~210ms.
	// Check at 200ms — entry should still exist (before cleanup).
	time.Sleep(200 * time.Millisecond)

	info, ok := mgr.GetProgress("cam-cleanup")
	if !ok {
		t.Fatal("expected progress entry to exist after merge completion")
	}
	if info.Status != "completed" {
		t.Fatalf("expected status 'completed', got %q", info.Status)
	}

	// Wait past the cleanup delay (now at 400ms, well past the 210ms cleanup).
	time.Sleep(200 * time.Millisecond)

	// Progress should have been cleaned up.
	_, ok = mgr.GetProgress("cam-cleanup")
	if ok {
		t.Fatal("expected progress entry to be cleaned up after delay")
	}
}

// ============================================================
// Concurrent / Race tests
// ============================================================

// TestRollingMergeManager_ConcurrentMultiCamera verifies that multiple cameras
// can trigger merge simultaneously without data races.
func TestRollingMergeManager_ConcurrentMultiCamera(t *testing.T) {
	db := newMockDB()
	merger := &slowMerger{delay: 20 * time.Millisecond}
	mgr := NewRollingMergeManager(merger, db, 10, false)

	tmpDir := t.TempDir()
	segmentDir := filepath.Join(tmpDir, "segment")
	os.MkdirAll(segmentDir, 0o755)
	outputPath := filepath.Join(tmpDir, "output.mp4")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	numCameras := 10
	for i := range numCameras {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			mgr.StartSegmentMerge(ctx, fmt.Sprintf("cam-%d", id), segmentDir, outputPath, "")
		}(i)
	}
	wg.Wait()

	if mgr.ActiveCount() != numCameras {
		t.Fatalf("expected %d active merges, got %d", numCameras, mgr.ActiveCount())
	}

	// Wait for all merges to complete (100ms pre-merge delay + 20ms merge delay).
	time.Sleep(500 * time.Millisecond)

	if mgr.ActiveCount() != 0 {
		t.Fatalf("expected 0 active merges after completion, got %d", mgr.ActiveCount())
	}

	mgr.StopAll()
}

// TestRollingMergeManager_ConcurrentCancelDuringMerge starts a merge with a slow merger,
// then cancels it from multiple goroutines simultaneously to detect races in StopSegmentMerge.
func TestRollingMergeManager_ConcurrentCancelDuringMerge(t *testing.T) {
	db := newMockDB()
	merger := &slowMerger{delay: 5 * time.Second} // very slow
	mgr := NewRollingMergeManager(merger, db, 10, false)

	tmpDir := t.TempDir()
	segmentDir := filepath.Join(tmpDir, "segment")
	os.MkdirAll(segmentDir, 0o755)
	outputPath := filepath.Join(tmpDir, "output.mp4")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr.StartSegmentMerge(ctx, "cam-cancel", segmentDir, outputPath, "")

	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mgr.StopSegmentMerge("cam-cancel")
		}()
	}
	wg.Wait()

	if mgr.IsActive("cam-cancel") {
		t.Fatal("expected cam-cancel to be inactive after cancellation")
	}
}

// TestRollingMergeManager_ConcurrentProgressReadWrite tests concurrent access
// to the progress map while merges are active and completing.
func TestRollingMergeManager_ConcurrentProgressReadWrite(t *testing.T) {
	db := newMockDB()
	merger := &slowMerger{delay: 50 * time.Millisecond}
	mgr := NewRollingMergeManager(merger, db, 10, false)

	tmpDir := t.TempDir()
	segmentDir := filepath.Join(tmpDir, "segment")
	os.MkdirAll(segmentDir, 0o755)
	outputPath := filepath.Join(tmpDir, "output.mp4")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start a few merges.
	const numCameras = 5
	for i := range numCameras {
		mgr.StartSegmentMerge(ctx, fmt.Sprintf("cam-%d", i), segmentDir, outputPath, "")
	}

	// Read progress from many goroutines while merges are in flight.
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			camID := fmt.Sprintf("cam-%d", idx%numCameras)
			_, ok := mgr.GetProgress(camID)
			_ = ok // just testing concurrent read safety
		}(i)
	}
	wg.Wait()

	// Wait for merges to finish.
	time.Sleep(300 * time.Millisecond)

	// Read progress concurrently after completion.
	for i := range 50 {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			camID := fmt.Sprintf("cam-%d", idx%numCameras)
			_, _ = mgr.GetProgress(camID)
		}(i)
	}
	wg.Wait()

	mgr.StopAll()
}

// TestRollingMergeManager_ConcurrentStopAllAndStart tests that calling StopAll
// while new merges are being started does not race.
func TestRollingMergeManager_ConcurrentStopAllAndStart(t *testing.T) {
	db := newMockDB()
	merger := &slowMerger{delay: 100 * time.Millisecond}
	mgr := NewRollingMergeManager(merger, db, 10, false)

	tmpDir := t.TempDir()
	segmentDir := filepath.Join(tmpDir, "segment")
	os.MkdirAll(segmentDir, 0o755)
	outputPath := filepath.Join(tmpDir, "output.mp4")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup

	// Concurrently start merges and call StopAll.
	for i := range 20 {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			if id%2 == 0 {
				mgr.StartSegmentMerge(ctx, fmt.Sprintf("cam-%d", id), segmentDir, outputPath, "")
			} else {
				mgr.StopAll()
			}
		}(i)
	}
	wg.Wait()

	// Should not panic. Final state may have some active entries.
	mgr.StopAll()
}

// --- Additional edge case tests ---

func TestRollingMergeManager_MergeEmptyDir(t *testing.T) {
	t.Helper()
	db := newMockDB()
	merger := &zeroFrameMerger{delay: 10 * time.Millisecond}
	mgr := NewRollingMergeManager(merger, db, 10, false)

	tmpDir := t.TempDir()
	segmentDir := filepath.Join(tmpDir, "empty-segment")
	os.MkdirAll(segmentDir, 0o755)
	// No files in segment dir — 0 frames.
	outputPath := filepath.Join(tmpDir, "output.mp4")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr.StartSegmentMerge(ctx, "cam-empty", segmentDir, outputPath, "rec-empty")
	// Wait for merge to complete (100ms pre-merge delay + 10ms merge delay).
	time.Sleep(300 * time.Millisecond)

	info, ok := mgr.GetProgress("cam-empty")
	if !ok {
		t.Fatal("expected progress entry for cam-empty")
	}
	t.Logf("empty dir merge: status=%s, frames=%d", info.Status, info.FramesMerged)
}

func TestRollingMergeManager_GetProgressNonExistent(t *testing.T) {
	t.Helper()
	db := newMockDB()
	merger := &slowMerger{delay: 10 * time.Millisecond}
	mgr := NewRollingMergeManager(merger, db, 10, false)

	_, ok := mgr.GetProgress("nonexistent-cam")
	if ok {
		t.Fatal("expected false for non-existent camera progress")
	}
}

func TestRollingMergeManager_RapidStartStop(t *testing.T) {
	t.Helper()
	db := newMockDB()
	merger := &slowMerger{delay: 500 * time.Millisecond}
	mgr := NewRollingMergeManager(merger, db, 10, false)

	tmpDir := t.TempDir()
	segmentDir := filepath.Join(tmpDir, "segment")
	os.MkdirAll(segmentDir, 0o755)
	os.WriteFile(filepath.Join(segmentDir, "frame_000001.jpg"), []byte("dummy"), 0o644)
	outputPath := filepath.Join(tmpDir, "output.mp4")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	for range 10 {
		mgr.StartSegmentMerge(ctx, "cam-rapid", segmentDir, outputPath, "")
		mgr.StopSegmentMerge("cam-rapid")
	}

	if mgr.IsActive("cam-rapid") {
		t.Fatal("expected cam-rapid to be inactive after rapid start/stop")
	}
}
