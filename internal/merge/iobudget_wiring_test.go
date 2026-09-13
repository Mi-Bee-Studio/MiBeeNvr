package merge

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/iobudget"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/stretchr/testify/require"
)

// fakeBudget records Wait calls without blocking — proves merge bills its
// sample streaming to the shared budget (#751).
type fakeBudget struct {
	mu     sync.Mutex
	calls  []int64
	consum []string
}

func (f *fakeBudget) Wait(_ context.Context, consumer string, n int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, n)
	f.consum = append(f.consum, consumer)
	return nil
}

func (f *fakeBudget) total() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	var total int64
	for _, n := range f.calls {
		total += n
	}
	return total
}

func (f *fakeBudget) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeBudget) allConsumersMerge() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.consum {
		if c != iobudget.ConsumerMerge {
			return false
		}
	}
	return true
}

// TestMergeMP4Segments_BudgetBillsSampleBytes: with a budget installed, the
// MP4 merge must bill (at least) the mdat payload bytes it streams, under the
// merge consumer label.
func TestMergeMP4Segments_BudgetBillsSampleBytes(t *testing.T) {
	dir := t.TempDir()
	sps := []byte{0x67, 0x42, 0x00, 0x0a, 0xe2, 0x40, 0x40, 0x04, 0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x00, 0xc8, 0x40}
	pps := []byte{0x68, 0xce, 0x38, 0x80}
	idrNAL := []byte{0x65, 0x88, 0x80, 0x40}
	pNAL := []byte{0x41, 0x10, 0x00, 0x0c}

	seg1 := createH264SegmentWithSamples(t, dir, "seg1.mp4", sps, pps, [][]byte{idrNAL, pNAL})
	info1, err := ParseSegment(seg1)
	require.NoError(t, err)

	fb := &fakeBudget{}
	prev := ioBudget
	SetIOBudget(fb)
	t.Cleanup(func() { SetIOBudget(prev) })

	outputPath := dir + "/merged.mp4"
	_, err = MergeMP4Segments(context.Background(), []*SegmentInfo{info1}, outputPath)
	require.NoError(t, err)

	require.Greater(t, fb.count(), 0, "merge must bill sample streaming to the budget")
	require.True(t, fb.allConsumersMerge(), "billed under the merge consumer label")

	// Billed bytes must cover the mdat payload: 2 samples × (4-byte length
	// prefix + payload).
	wantPayload := int64((4 + len(idrNAL)) + (4 + len(pNAL)))
	require.GreaterOrEqual(t, fb.total(), wantPayload, "billed bytes should cover the streamed sample payload")
}

// TestMergeMP4Segments_BudgetDisabledByDefault: without SetIOBudget the merge
// path must not touch any limiter (nil fast path).
func TestMergeMP4Segments_BudgetDisabledByDefault(t *testing.T) {
	require.Nil(t, ioBudget, "package default must be nil (budgeting off)")
}

// parkingBudget blocks in Wait until ctx is done, then returns its error —
// simulates a saturated budget during shutdown.
type parkingBudget struct{}

func (parkingBudget) Wait(ctx context.Context, _ string, _ int64) error {
	<-ctx.Done()
	return ctx.Err()
}

// TestMergeMP4Segments_BudgetCancelAborts: a cancelled budget wait aborts the
// merge with the ctx error — pacing is cancellable, matching the merge loop's
// existing ctx semantics.
func TestMergeMP4Segments_BudgetCancelAborts(t *testing.T) {
	dir := t.TempDir()
	sps := []byte{0x67, 0x42, 0x00, 0x0a, 0xe2, 0x40, 0x40, 0x04, 0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x00, 0xc8, 0x40}
	pps := []byte{0x68, 0xce, 0x38, 0x80}
	idrNAL := []byte{0x65, 0x88, 0x80, 0x40}

	seg1 := createH264SegmentWithSamples(t, dir, "seg1.mp4", sps, pps, [][]byte{idrNAL})
	info1, err := ParseSegment(seg1)
	require.NoError(t, err)

	prev := ioBudget
	SetIOBudget(parkingBudget{})
	t.Cleanup(func() { SetIOBudget(prev) })

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	outputPath := dir + "/merged.mp4"
	_, err = MergeMP4Segments(ctx, []*SegmentInfo{info1}, outputPath)
	require.ErrorIs(t, err, context.Canceled)
}

// TestMergeAVISegments_BudgetBillsChunkBytes mirrors the MP4 test for the
// AVI streaming path: a successful merge bills its movi chunk bytes.
func TestMergeAVISegments_BudgetBillsChunkBytes(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "store")
	store, err := storage.NewManager(storeDir)
	require.NoError(t, err)

	path := createTestAVI(t, dir, "seg.avi", 64, 48, 4, false)
	now := time.Now()
	segments := []*model.Recording{{
		ID:         "seg1",
		CameraID:   "cam1",
		FilePath:   path,
		Format:     model.FormatAVI,
		StartedAt:  now.Add(-time.Hour),
		EndedAt:    now,
		Duration:   3600.0,
		FileSize:   fileSize(t, path),
		FrameCount: 4,
	}}

	fb := &fakeBudget{}
	prev := ioBudget
	SetIOBudget(fb)
	t.Cleanup(func() { SetIOBudget(prev) })

	merged, _, err := MergeAVISegments(t.Context(), segments, store, "cam1")
	require.NoError(t, err)
	require.NotNil(t, merged)

	require.Greater(t, fb.count(), 0, "AVI merge must bill chunk streaming to the budget")
	require.True(t, fb.allConsumersMerge(), "billed under the merge consumer label")
}
