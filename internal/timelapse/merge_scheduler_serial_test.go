package timelapse

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestMergeScheduler_SerializesFleetBoundary pins #47: aligned boundaries fire
// the whole fleet at once (00:00 default), and concurrent merge pipelines
// saturated the deployment host's disk. Scheduled merges must run ONE AT A
// TIME while every camera still gets its merge.
func TestMergeScheduler_SerializesFleetBoundary(t *testing.T) {
	t.Helper()

	var running, maxRunning atomic.Int32
	var started atomic.Int32
	release := make(chan struct{})

	s := NewMergeScheduler(nil)
	s.SetRunFunc(func(ctx context.Context, cameraID string, refTime time.Time) error {
		cur := running.Add(1)
		for {
			old := maxRunning.Load()
			if cur <= old || maxRunning.CompareAndSwap(old, cur) {
				break
			}
		}
		started.Add(1)
		<-release // hold the slot so overlap would be observable
		running.Add(-1)
		return nil
	})

	now := fixedNow()
	const cams = 4
	s.mu.Lock()
	for i := range cams {
		id := "cam-" + string(rune('a'+i))
		s.addOrUpdateAt(now, id, 8*time.Hour)
		s.entries[id].nextRun = now.Add(-time.Hour)
	}
	s.mu.Unlock()

	count := s.triggerDueAt(context.Background(), now)
	require.Equal(t, cams, count)

	// Exactly one merge may be in flight at any moment.
	require.Eventually(t, func() bool { return started.Load() == 1 },
		2*time.Second, 10*time.Millisecond, "first merge should start")
	require.Equal(t, int32(1), maxRunning.Load(), "merges overlapped before release")

	// Drain one at a time; concurrency cap must hold throughout. Releasing
	// merge i lets merge i+1 start, so the observable count climbs to
	// min(i+1, cams).
	for i := 1; i <= cams; i++ {
		release <- struct{}{}
		want := int32(i + 1)
		if want > cams {
			want = cams
		}
		require.Eventually(t, func() bool { return started.Load() == want },
			2*time.Second, 10*time.Millisecond, "merge should progress after release %d", i)
		require.LessOrEqual(t, int(maxRunning.Load()), 1, "merges overlapped")
	}
	close(release)
}

// TestMergeScheduler_QueuedMergeFailsFastOnCancel: Stop during a backlog must
// not wait for every queued merge — waiting goroutines observe ctx
// cancellation and bow out without running.
func TestMergeScheduler_QueuedMergeFailsFastOnCancel(t *testing.T) {
	t.Helper()

	var ran atomic.Int32
	inFlight := make(chan struct{})
	block := make(chan struct{})

	s := NewMergeScheduler(nil)
	s.SetRunFunc(func(ctx context.Context, cameraID string, refTime time.Time) error {
		ran.Add(1)
		close(inFlight)
		<-block
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	now := fixedNow()
	s.mu.Lock()
	for i := range 3 {
		id := "cam-" + string(rune('a'+i))
		s.addOrUpdateAt(now, id, 8*time.Hour)
		s.entries[id].nextRun = now.Add(-time.Hour)
	}
	s.mu.Unlock()

	// Fire via the real path with a cancellable ctx (as runLoop would).
	count := s.triggerDueAt(ctx, now)
	require.Equal(t, 3, count)

	<-inFlight // first merge is running, two are queued on the serial lock
	cancel()   // what Stop does to the loop's ctx

	const drainTime = 50 * time.Millisecond
	time.Sleep(drainTime)
	close(block) // let the in-flight merge finish

	// The two queued goroutines must have bailed on the cancelled ctx.
	require.Equal(t, int32(1), ran.Load(),
		"queued merges should not run after ctx cancellation")
}
