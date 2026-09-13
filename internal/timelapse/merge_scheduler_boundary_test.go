package timelapse

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestMergeScheduler_TriggerMergesJustClosedWindow guards the window
// off-by-one fix (2026-09-13 backlog incident): the scheduler fires exactly at
// a window boundary (e.g. midnight for natural-day). parseMergeRange maps a
// refTime to the window CONTAINING it, so passing the boundary instant itself
// selects the window that just OPENED — empty, zero segments, no merge row,
// and delete_recordings_after_merge never fires. Two cameras accumulated
// 143GB + 277GB of unconverted backlog over 30 days before anyone noticed.
//
// The trigger must therefore pass boundary−duration: the window that just
// CLOSED and is now final.
func TestMergeScheduler_TriggerMergesJustClosedWindow(t *testing.T) {
	t.Helper()

	for _, dur := range []time.Duration{time.Hour, 8 * time.Hour, 24 * time.Hour} {
		name := dur.String()
		t.Run(name, func(t *testing.T) {
			t.Helper()

			var mu sync.Mutex
			var got time.Time
			var ran int32 = 0
			done := make(chan struct{})

			s := NewMergeScheduler(nil)
			s.SetRunFunc(func(ctx context.Context, cameraID string, refTime time.Time) error {
				mu.Lock()
				got = refTime
				ran++
				mu.Unlock()
				close(done)
				return nil
			})

			// A boundary aligned to the duration grid (fixedNow is a whole hour).
			boundary := fixedNow().Truncate(dur)
			s.mu.Lock()
			s.addOrUpdateAt(boundary, "cam-boundary", dur)
			s.entries["cam-boundary"].nextRun = boundary
			s.mu.Unlock()

			count := s.triggerDueAt(context.Background(), boundary)
			if count != 1 {
				t.Fatalf("triggered %d cameras, want 1", count)
			}

			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("merge goroutine did not execute")
			}

			mu.Lock()
			defer mu.Unlock()
			if ran != 1 {
				t.Fatalf("runFunc called %d times, want 1", ran)
			}
			// refTime must fall inside the just-closed window
			// [boundary-dur, boundary) — start-inclusive, boundary-exclusive
			// (parseMergeRange maps the start instant to the window it opens).
			wantStart := boundary.Add(-dur)
			if got.Before(wantStart) || !got.Before(boundary) {
				t.Fatalf("refTime = %v, want inside just-closed window [%v, %v)",
					got, wantStart, boundary)
			}
		})
	}
}
