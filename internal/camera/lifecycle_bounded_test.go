package camera

import (
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestRunBoundedLimitsConcurrency pins the boot bring-up pool (#48): at most
// `limit` jobs run at once, jobs start in submission order, and the call
// returns only after everything finished.
func TestRunBoundedLimitsConcurrency(t *testing.T) {
	t.Parallel()

	const jobsN = 8
	var running, maxRunning atomic.Int32
	var orderMu sync.Mutex
	started := make([]int, 0, jobsN)

	jobs := make([]func(), 0, jobsN)
	for i := range jobsN {
		jobs = append(jobs, func() {
			cur := running.Add(1)
			for {
				old := maxRunning.Load()
				if cur <= old || maxRunning.CompareAndSwap(old, cur) {
					break
				}
			}
			orderMu.Lock()
			started = append(started, i)
			orderMu.Unlock()
			time.Sleep(60 * time.Millisecond) // hold the slot long enough to observe the cap
			running.Add(-1)
		})
	}

	done := make(chan struct{})
	go func() {
		runBounded(3, jobs)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runBounded did not finish")
	}

	require.LessOrEqual(t, int(maxRunning.Load()), 3, "concurrency cap exceeded")
	require.Greater(t, int(maxRunning.Load()), 1, "jobs did not overlap at all — pool degenerated to serial")
	require.Len(t, started, jobsN, "all jobs must run exactly once")
	// Submission order holds for the head of the fleet: the FIRST WAVE is
	// exactly jobs 0..limit-1 (in some scheduling order within the wave) —
	// no later-submitted job may start before the pool head has.
	head := append([]int(nil), started[:3]...)
	sort.Ints(head)
	require.Equal(t, []int{0, 1, 2}, head, "first wave must be the first submitted jobs")
}

// TestRunBoundedLimitFloor: a non-positive limit must not deadlock or
// no-op — it floors to 1.
func TestRunBoundedLimitFloor(t *testing.T) {
	t.Parallel()
	var ran atomic.Int32
	runBounded(0, []func(){func() { ran.Add(1) }, func() { ran.Add(1) }})
	require.Equal(t, int32(2), ran.Load())
}
