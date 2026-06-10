package timelapse

import (
"context"
"fmt"
"sync"
"sync/atomic"
"testing"
"time"

"github.com/stretchr/testify/require"
)

// fixedNow returns a fixed UTC time for deterministic testing:
// Sunday 2026-06-07 09:30:00 UTC
func fixedNow() time.Time {
	return time.Date(2026, 6, 7, 9, 30, 0, 0, time.UTC)
}

// --- computeNextRun unit tests ---

func TestComputeNextRun_8h(t *testing.T) {
	t.Helper()
	// 09:30 UTC on any day → next boundary: 16:00 UTC the same day
	now := fixedNow()
	next := computeNextRun(now, 8*time.Hour, nil)

	expected := time.Date(2026, 6, 7, 16, 0, 0, 0, time.UTC)
	if !next.Equal(expected) {
		t.Errorf("computeNextRun(09:30, 8h) = %v, want %v (16:00)", next, expected)
	}
}

func TestComputeNextRun_8h_Afternoon(t *testing.T) {
	t.Helper()
	// 17:00 UTC → next boundary: 00:00 next day
	now := time.Date(2026, 6, 7, 17, 0, 0, 0, time.UTC)
	next := computeNextRun(now, 8*time.Hour, nil)

	expected := time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC)
	if !next.Equal(expected) {
		t.Errorf("computeNextRun(17:00, 8h) = %v, want %v (00:00 next day)", next, expected)
	}
}

func TestComputeNextRun_8h_AtBoundary(t *testing.T) {
	t.Helper()
	// Exactly 08:00 UTC → next should be 16:00 UTC (not 08:00 again)
	now := time.Date(2026, 6, 7, 8, 0, 0, 0, time.UTC)
	next := computeNextRun(now, 8*time.Hour, nil)

	expected := time.Date(2026, 6, 7, 16, 0, 0, 0, time.UTC)
	if !next.Equal(expected) {
		t.Errorf("computeNextRun(08:00, 8h) = %v, want %v (16:00)", next, expected)
	}
}

func TestComputeNextRun_8h_FirstBlock(t *testing.T) {
	t.Helper()
	// 01:00 UTC → next boundary: 08:00 UTC same day
	now := time.Date(2026, 6, 7, 1, 0, 0, 0, time.UTC)
	next := computeNextRun(now, 8*time.Hour, nil)

	expected := time.Date(2026, 6, 7, 8, 0, 0, 0, time.UTC)
	if !next.Equal(expected) {
		t.Errorf("computeNextRun(01:00, 8h) = %v, want %v (08:00)", next, expected)
	}
}

func TestComputeNextRun_8h_ScheduledAtBoundaries(t *testing.T) {
	t.Helper()
	tests := []struct {
		name     string
		now      time.Time
		expected time.Time
	}{
		{
			name:     "00:00 boundary → 08:00",
			now:      time.Date(2026, 6, 7, 0, 0, 0, 0, time.UTC),
			expected: time.Date(2026, 6, 7, 8, 0, 0, 0, time.UTC),
		},
		{
			name:     "08:00 boundary → 16:00",
			now:      time.Date(2026, 6, 7, 8, 0, 0, 0, time.UTC),
			expected: time.Date(2026, 6, 7, 16, 0, 0, 0, time.UTC),
		},
		{
			name:     "16:00 boundary → 00:00 next day",
			now:      time.Date(2026, 6, 7, 16, 0, 0, 0, time.UTC),
			expected: time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			next := computeNextRun(tt.now, 8*time.Hour, nil)
			if !next.Equal(tt.expected) {
				t.Errorf("computeNextRun(%s, 8h) = %v, want %v", tt.now.Format(time.RFC3339), next, tt.expected)
			}
		})
	}
}

func TestComputeNextRun_12h(t *testing.T) {
	t.Helper()
	// 09:30 UTC → next boundary: 12:00 UTC same day
	now := fixedNow()
	next := computeNextRun(now, 12*time.Hour, nil)

	expected := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	if !next.Equal(expected) {
		t.Errorf("computeNextRun(09:30, 12h) = %v, want %v (12:00)", next, expected)
	}
}

func TestComputeNextRun_12h_Afternoon(t *testing.T) {
	t.Helper()
	// 14:00 UTC → next boundary: 00:00 next day
	now := time.Date(2026, 6, 7, 14, 0, 0, 0, time.UTC)
	next := computeNextRun(now, 12*time.Hour, nil)

	expected := time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC)
	if !next.Equal(expected) {
		t.Errorf("computeNextRun(14:00, 12h) = %v, want %v (00:00 next day)", next, expected)
	}
}

func TestComputeNextRun_12h_ScheduledAtBoundaries(t *testing.T) {
	t.Helper()
	tests := []struct {
		name     string
		now      time.Time
		expected time.Time
	}{
		{
			name:     "00:00 boundary → 12:00",
			now:      time.Date(2026, 6, 7, 0, 0, 0, 0, time.UTC),
			expected: time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC),
		},
		{
			name:     "12:00 boundary → 00:00 next day",
			now:      time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC),
			expected: time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			next := computeNextRun(tt.now, 12*time.Hour, nil)
			if !next.Equal(tt.expected) {
				t.Errorf("computeNextRun(%s, 12h) = %v, want %v", tt.now.Format(time.RFC3339), next, tt.expected)
			}
		})
	}
}

func TestComputeNextRun_24h(t *testing.T) {
	t.Helper()
	now := fixedNow()
	next := computeNextRun(now, 24*time.Hour, nil)

	expected := time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC)
	if !next.Equal(expected) {
		t.Errorf("computeNextRun(09:30, 24h) = %v, want %v (00:00 next day)", next, expected)
	}
}

func TestComputeNextRun_7d(t *testing.T) {
	t.Helper()
	// Sunday 09:30 UTC → next Monday 00:00 UTC
	now := fixedNow() // Sunday
	next := computeNextRun(now, 7*24*time.Hour, nil)

	expected := time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC) // Monday
	if !next.Equal(expected) {
		t.Errorf("computeNextRun(Sunday 09:30, 7d) = %v, want %v (Monday 00:00)", next, expected)
	}
}

func TestComputeNextRun_7d_OnMonday(t *testing.T) {
	t.Helper()
	// Monday 03:00 UTC → next Monday 00:00 UTC
	now := time.Date(2026, 6, 8, 3, 0, 0, 0, time.UTC) // Monday
	next := computeNextRun(now, 7*24*time.Hour, nil)

	expected := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC) // next Monday
	if !next.Equal(expected) {
		t.Errorf("computeNextRun(Monday 03:00, 7d) = %v, want %v (next Monday 00:00)", next, expected)
	}
}

func TestComputeNextRun_30d(t *testing.T) {
	t.Helper()
	now := time.Date(2026, 6, 15, 9, 30, 0, 0, time.UTC)
	next := computeNextRun(now, 30*24*time.Hour, nil)

	expected := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	if !next.Equal(expected) {
		t.Errorf("computeNextRun(June 15, 30d) = %v, want %v (July 1 00:00)", next, expected)
	}
}

func TestComputeNextRun_30d_ExactFirst(t *testing.T) {
	t.Helper()
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	next := computeNextRun(now, 30*24*time.Hour, nil)

	expected := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	if !next.Equal(expected) {
		t.Errorf("computeNextRun(June 1 00:00, 30d) = %v, want %v (July 1 00:00)", next, expected)
	}
}

func TestComputeNextRun_30d_YearBoundary(t *testing.T) {
	t.Helper()
	now := time.Date(2026, 12, 15, 9, 30, 0, 0, time.UTC)
	next := computeNextRun(now, 30*24*time.Hour, nil)

	expected := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	if !next.Equal(expected) {
		t.Errorf("computeNextRun(Dec 15, 30d) = %v, want %v (Jan 1)", next, expected)
	}
}

// --- MergeScheduler tests ---

func TestNewMergeScheduler(t *testing.T) {
	t.Helper()
	s := NewMergeScheduler(nil)
	require.NotNil(t, s)
}

func TestMergeScheduler_AddOrUpdate(t *testing.T) {
	t.Helper()
	s := NewMergeScheduler(nil)
	s.AddOrUpdate("cam-1", 8*time.Hour)

	dur, ok := s.GetDuration("cam-1")
	require.True(t, ok)
	require.Equal(t, 8*time.Hour, dur)
}

func TestMergeScheduler_GetDuration_NotFound(t *testing.T) {
	t.Helper()
	s := NewMergeScheduler(nil)
	_, ok := s.GetDuration("nonexistent")
	require.False(t, ok)
}

func TestMergeScheduler_Remove(t *testing.T) {
	t.Helper()
	s := NewMergeScheduler(nil)
	s.AddOrUpdate("cam-1", 8*time.Hour)
	s.Remove("cam-1")

	_, ok := s.GetDuration("cam-1")
	require.False(t, ok)
}

func TestMergeScheduler_UpdateDuration(t *testing.T) {
	t.Helper()
	s := NewMergeScheduler(nil)

	s.AddOrUpdate("cam-1", 8*time.Hour)
	dur, _ := s.GetDuration("cam-1")
	require.Equal(t, 8*time.Hour, dur)

	s.AddOrUpdate("cam-1", 12*time.Hour)
	dur, _ = s.GetDuration("cam-1")
	require.Equal(t, 12*time.Hour, dur)
}

func TestMergeScheduler_8h_ScheduleAtBoundaries(t *testing.T) {
	t.Helper()
	// At 00:00 → next is 08:00
	now := time.Date(2026, 6, 7, 0, 0, 0, 0, time.UTC)
	next := computeNextRun(now, 8*time.Hour, nil)
	require.True(t, next.Equal(time.Date(2026, 6, 7, 8, 0, 0, 0, time.UTC)),
		"00:00 → should be 08:00, got %v", next)
}

func TestMergeScheduler_8h_AddOrUpdate(t *testing.T) {
	t.Helper()
	s := NewMergeScheduler(nil)
	now := fixedNow() // Sunday 09:30 UTC

	s.mu.Lock()
	s.addOrUpdateAt(now, "cam-8h", 8*time.Hour)
	s.mu.Unlock()

	// nextRun should be 16:00 UTC
	dur, ok := s.GetDuration("cam-8h")
	require.True(t, ok)
	require.Equal(t, 8*time.Hour, dur)

	s.mu.Lock()
	entry := s.entries["cam-8h"]
	s.mu.Unlock()
	expected := time.Date(2026, 6, 7, 16, 0, 0, 0, time.UTC)
	if !entry.nextRun.Equal(expected) {
		t.Errorf("8h camera nextRun = %v, want %v (16:00 UTC)", entry.nextRun, expected)
	}
}

func TestMergeScheduler_24h_AddOrUpdate(t *testing.T) {
	t.Helper()
	s := NewMergeScheduler(nil)
	now := fixedNow() // Sunday 09:30 UTC

	s.mu.Lock()
	s.addOrUpdateAt(now, "cam-24h", 24*time.Hour)
	s.mu.Unlock()

	s.mu.Lock()
	entry := s.entries["cam-24h"]
	s.mu.Unlock()
	expected := time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC)
	if !entry.nextRun.Equal(expected) {
		t.Errorf("24h camera nextRun = %v, want %v (00:00 next day)", entry.nextRun, expected)
	}
}

func TestMergeScheduler_7d_AddOrUpdate(t *testing.T) {
	t.Helper()
	now := fixedNow() // Sunday 09:30 UTC
	s := NewMergeScheduler(nil)

	s.mu.Lock()
	s.addOrUpdateAt(now, "cam-7d", 7*24*time.Hour)
	s.mu.Unlock()

	s.mu.Lock()
	entry := s.entries["cam-7d"]
	s.mu.Unlock()
	expected := time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC) // Monday
	if !entry.nextRun.Equal(expected) {
		t.Errorf("7d camera nextRun = %v, want %v (Monday 00:00)", entry.nextRun, expected)
	}
}

func TestMergeScheduler_30d_AddOrUpdate(t *testing.T) {
	t.Helper()
	now := time.Date(2026, 6, 15, 9, 30, 0, 0, time.UTC)
	s := NewMergeScheduler(nil)

	s.mu.Lock()
	s.addOrUpdateAt(now, "cam-30d", 30*24*time.Hour)
	s.mu.Unlock()

	s.mu.Lock()
	entry := s.entries["cam-30d"]
	s.mu.Unlock()
	expected := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	if !entry.nextRun.Equal(expected) {
		t.Errorf("30d camera nextRun = %v, want %v (July 1 00:00)", entry.nextRun, expected)
	}
}

func TestMergeScheduler_RescheduleOnUpdate(t *testing.T) {
	t.Helper()
	now := fixedNow() // 09:30 UTC
	s := NewMergeScheduler(nil)

	// Add with 8h → next should be 16:00
	s.mu.Lock()
	s.addOrUpdateAt(now, "cam-flex", 8*time.Hour)
	s.mu.Unlock()

	entry := s.entries["cam-flex"]
	expected8h := time.Date(2026, 6, 7, 16, 0, 0, 0, time.UTC)
	if !entry.nextRun.Equal(expected8h) {
		t.Fatalf("before update: want %v, got %v", expected8h, entry.nextRun)
	}

	// Update to 12h → next should now be 12:00
	s.mu.Lock()
	s.addOrUpdateAt(now, "cam-flex", 12*time.Hour)
	s.mu.Unlock()

	entry = s.entries["cam-flex"]
	expected12h := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	if !entry.nextRun.Equal(expected12h) {
		t.Errorf("after update to 12h: want %v, got %v", expected12h, entry.nextRun)
	}
}

func TestMergeScheduler_TriggerDue(t *testing.T) {
	t.Helper()
	var triggered int32
	s := NewMergeScheduler(nil)
	s.SetRunFunc(func(ctx context.Context, cameraID string, refTime time.Time) error {
		atomic.AddInt32(&triggered, 1)
		return nil
	})

	now := fixedNow()
	s.mu.Lock()
	s.addOrUpdateAt(now, "cam-1", 8*time.Hour)
	// Set nextRun to the past to force immediate trigger
	s.entries["cam-1"].nextRun = now.Add(-time.Hour)
	s.mu.Unlock()

	count := s.triggerDueAt(context.Background(), now)
	require.Equal(t, 1, count)
	// triggerDueAt runs merge in a goroutine, so wait for it
	require.Eventually(t, func() bool {
		return atomic.LoadInt32(&triggered) == 1
	}, time.Second, 10*time.Millisecond, "merge goroutine should have executed")
}

func TestMergeScheduler_TriggerDue_NotDue(t *testing.T) {
	t.Helper()
	var triggered int32
	s := NewMergeScheduler(nil)
	s.SetRunFunc(func(ctx context.Context, cameraID string, refTime time.Time) error {
		atomic.AddInt32(&triggered, 1)
		return nil
	})

	now := fixedNow()
	s.mu.Lock()
	s.addOrUpdateAt(now, "cam-1", 8*time.Hour)
	// nextRun is in the future (16:00)
	s.mu.Unlock()

	count := s.triggerDueAt(context.Background(), now)
	require.Equal(t, 0, count)
	require.Equal(t, int32(0), atomic.LoadInt32(&triggered))
}

func TestMergeScheduler_StartStop(t *testing.T) {
	t.Helper()
	s := NewMergeScheduler(nil)
	ctx, cancel := context.WithCancel(context.Background())
	s.Start(ctx)
	s.Stop()
	cancel()
}

func TestMergeScheduler_MultipleCameras(t *testing.T) {
	t.Helper()
	now := fixedNow() // Sunday 09:30 UTC
	s := NewMergeScheduler(nil)

	s.mu.Lock()
	s.addOrUpdateAt(now, "cam-8h", 8*time.Hour)
	s.addOrUpdateAt(now, "cam-24h", 24*time.Hour)
	s.addOrUpdateAt(now, "cam-7d", 7*24*time.Hour)
	s.addOrUpdateAt(now, "cam-30d", 30*24*time.Hour)
	s.mu.Unlock()

	// Verify durations
	dur, ok := s.GetDuration("cam-8h")
	require.True(t, ok)
	require.Equal(t, 8*time.Hour, dur)

	dur, ok = s.GetDuration("cam-24h")
	require.True(t, ok)
	require.Equal(t, 24*time.Hour, dur)

	dur, ok = s.GetDuration("cam-7d")
	require.True(t, ok)
	require.Equal(t, 7*24*time.Hour, dur)

	dur, ok = s.GetDuration("cam-30d")
	require.True(t, ok)
	require.Equal(t, 30*24*time.Hour, dur)

	// Verify next run times
	checkNext := func(cameraID string, expected time.Time) {
		s.mu.Lock()
		entry := s.entries[cameraID]
		s.mu.Unlock()
		if entry == nil {
			t.Errorf("%s: entry not found", cameraID)
			return
		}
		if !entry.nextRun.Equal(expected) {
			t.Errorf("%s nextRun = %v, want %v", cameraID, entry.nextRun, expected)
		}
	}

	checkNext("cam-8h", time.Date(2026, 6, 7, 16, 0, 0, 0, time.UTC))
	checkNext("cam-24h", time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC))
	checkNext("cam-7d", time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC))
	checkNext("cam-30d", time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC))
}

func TestMergeScheduler_RemoveCamera(t *testing.T) {
	t.Helper()
	s := NewMergeScheduler(nil)
	s.AddOrUpdate("cam-1", 8*time.Hour)
	s.AddOrUpdate("cam-2", 24*time.Hour)

	require.Len(t, s.entries, 2)

	s.Remove("cam-1")
	require.Len(t, s.entries, 1)

	_, ok := s.GetDuration("cam-1")
	require.False(t, ok)

	_, ok = s.GetDuration("cam-2")
	require.True(t, ok)
}

func TestMergeScheduler_ConcurrentAddRemove(t *testing.T) {
	t.Helper()
	s := NewMergeScheduler(nil)

	done := make(chan struct{})
	go func() {
		for i := 0; i < 50; i++ {
			s.AddOrUpdate("cam-concurrent", 8*time.Hour)
		}
		done <- struct{}{}
	}()
	go func() {
		for i := 0; i < 50; i++ {
			s.Remove("cam-concurrent")
			s.AddOrUpdate("cam-concurrent", 24*time.Hour)
		}
		done <- struct{}{}
	}()
	<-done
	<-done

	// Should not panic and should have consistent state
	_, ok := s.GetDuration("cam-concurrent")
	require.True(t, ok)
}

// ============================================================
// Concurrent / Race tests
// ============================================================

// TestMergeScheduler_ConcurrentTriggerDuringLoop calls TriggerDue from multiple goroutines
// while the scheduler loop is running, to exercise concurrent entry iteration.
func TestMergeScheduler_ConcurrentTriggerDuringLoop(t *testing.T) {
	t.Helper()
	s := NewMergeScheduler(nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var triggered atomic.Int32
	s.SetRunFunc(func(ctx context.Context, cameraID string, refTime time.Time) error {
		triggered.Add(1)
		return nil
	})

	// Add multiple cameras.
	for i := 0; i < 5; i++ {
		s.AddOrUpdate(fmt.Sprintf("cam-%d", i), 8*time.Hour)
	}

	s.Start(ctx)

	// Call TriggerDue from many goroutines concurrently.
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.TriggerDue(ctx)
		}()
	}
	wg.Wait()

	s.Stop()
	// Should not panic; at least one trigger may have fired.
}

// TestMergeScheduler_ConcurrentAddMultipleCameras adds many cameras concurrently
// while also removing them, to exercise the entries map under high contention.
func TestMergeScheduler_ConcurrentAddMultipleCameras(t *testing.T) {
	t.Helper()
	s := NewMergeScheduler(nil)

	var wg sync.WaitGroup
	// Add many cameras from different goroutines.
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			camID := fmt.Sprintf("cam-%d", id)
			if id%4 == 0 {
				s.AddOrUpdate(camID, 8*time.Hour)
			} else if id%4 == 1 {
				s.Remove(camID)
			} else if id%4 == 2 {
				s.GetDuration(camID)
			} else {
				s.AddOrUpdate(camID, 24*time.Hour)
				s.Remove(camID)
			}
		}(i)
	}
	wg.Wait()

	// Should not panic.
}
