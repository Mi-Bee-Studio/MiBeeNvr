package timelapse

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/iobudget"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/stretchr/testify/require"
)

// fakeBudget records Wait calls without blocking (#751 wiring).
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

func (f *fakeBudget) allConsumersTimelapse() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.consum {
		if c != iobudget.ConsumerTimelapse {
			return false
		}
	}
	return true
}

// TestExtractor_BudgetBillsFrameBytes: with a budget installed, MP4 sync-frame
// extraction bills each written frame's bytes under the timelapse consumer.
func TestExtractor_BudgetBillsFrameBytes(t *testing.T) {
	dir := t.TempDir()
	outputDir := filepath.Join(dir, "frames")

	// One H.264 MP4 with keyframes at 0/5/10 (30fps, 15 samples).
	var samples []testSample
	for i := range 15 {
		var samp testSample
		if i%5 == 0 {
			samp = testSample{data: buildH264IDRSample(), isKeyFrame: true, duration: 1}
		} else {
			samp = testSample{data: buildH264NonIDRSample(), isKeyFrame: false, duration: 1}
		}
		samples = append(samples, samp)
	}
	mp4Path := createTestMP4(t, dir, "test.h264.mp4", false, samples, 30)

	fb := &fakeBudget{}
	prev := ioBudget
	SetIOBudget(fb)
	t.Cleanup(func() { SetIOBudget(prev) })

	extractor := NewRecordingFrameExtractor()
	n, err := extractor.ExtractFrames(t.Context(), mp4Path, model.FormatH264, 200*time.Millisecond, outputDir)
	require.NoError(t, err)
	require.Greater(t, n, 0)

	require.Greater(t, fb.count(), 0, "extraction must bill frame writes to the budget")
	require.True(t, fb.allConsumersTimelapse(), "billed under the timelapse consumer label")
	// Every written frame file's bytes must be covered by the bill.
	matches, err := filepath.Glob(filepath.Join(outputDir, "frame_*.h264"))
	require.NoError(t, err)
	var written int64
	for _, m := range matches {
		info, err := os.Stat(m)
		require.NoError(t, err)
		written += info.Size()
	}
	require.GreaterOrEqual(t, fb.total(), written, "billed bytes should cover the written frames")
}

// TestExtractor_BudgetDisabledByDefault: no budget installed → extraction
// works identically (nil fast path).
func TestExtractor_BudgetDisabledByDefault(t *testing.T) {
	require.Nil(t, ioBudget, "package default must be nil (budgeting off)")
}

// TestExtractor_BudgetCancelAborts: budget waits honour ctx cancellation.
func TestExtractor_BudgetCancelAborts(t *testing.T) {
	dir := t.TempDir()
	var samples []testSample
	for i := range 10 {
		var samp testSample
		if i%5 == 0 {
			samp = testSample{data: buildH264IDRSample(), isKeyFrame: true, duration: 1}
		} else {
			samp = testSample{data: buildH264NonIDRSample(), isKeyFrame: false, duration: 1}
		}
		samples = append(samples, samp)
	}
	mp4Path := createTestMP4(t, dir, "test.h264.mp4", false, samples, 30)

	prev := ioBudget
	SetIOBudget(parkingBudget{})
	t.Cleanup(func() { SetIOBudget(prev) })

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	extractor := NewRecordingFrameExtractor()
	_, err := extractor.ExtractFrames(ctx, mp4Path, model.FormatH264, 100*time.Millisecond, filepath.Join(dir, "frames"))
	require.ErrorIs(t, err, context.Canceled)
}

// parkingBudget blocks in Wait until ctx is done.
type parkingBudget struct{}

func (parkingBudget) Wait(ctx context.Context, _ string, _ int64) error {
	<-ctx.Done()
	return ctx.Err()
}
