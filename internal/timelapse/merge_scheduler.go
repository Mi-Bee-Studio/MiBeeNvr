// Package timelapse provides merge scheduling for timelapse recordings.
package timelapse

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// MergeRunFunc is called by MergeScheduler when a merge is due for a camera.
// The refTime is the UTC boundary time that triggered the merge.
type MergeRunFunc func(ctx context.Context, cameraID string, refTime time.Time) error

type mergeEntry struct {
	duration time.Duration
	nextRun  time.Time
}

// MergeScheduler schedules periodic merge operations for timelapse cameras.
// It maintains per-camera entries with configurable intervals and computes
// next run times based on aligned UTC boundaries. A single goroutine drives
// the loop, waking up at the earliest next run time across all cameras.
//
// Thread-safe for AddOrUpdate/Remove from any goroutine.
type MergeScheduler struct {
	mu      sync.Mutex
	entries map[string]*mergeEntry // cameraID → entry
	runFunc MergeRunFunc
	loc     *time.Location // timezone for window alignment
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	updated chan struct{} // signals entry changes to wake up the loop
}

// NewMergeScheduler creates a new MergeScheduler with no entries.
// If loc is nil, UTC is used for window alignment.
// Call SetRunFunc before Start to set the merge callback.
func NewMergeScheduler(loc *time.Location) *MergeScheduler {
	if loc == nil {
		loc = time.UTC
	}
	return &MergeScheduler{
		entries: make(map[string]*mergeEntry),
		updated: make(chan struct{}, 1),
		loc:     loc,
	}
}

// SetRunFunc sets the function to call when a merge is due.
// Must be called before Start.
func (s *MergeScheduler) SetRunFunc(fn MergeRunFunc) {
	s.runFunc = fn
}

// AddOrUpdate adds or updates a camera's merge schedule.
// The next run time is computed as the next aligned boundary in the configured timezone.
func (s *MergeScheduler) AddOrUpdate(cameraID string, duration time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.addOrUpdateAt(time.Now().In(s.loc), cameraID, duration)
}

// addOrUpdateAt is the internal version that accepts a fixed time for deterministic testing.
func (s *MergeScheduler) addOrUpdateAt(now time.Time, cameraID string, duration time.Duration) {
	s.entries[cameraID] = &mergeEntry{
		duration: duration,
		nextRun:  computeNextRun(now, duration, s.loc),
	}

	slog.Debug("merge scheduler: added/updated camera",
		"camera_id", cameraID,
		"duration", duration,
		"next_run", s.entries[cameraID].nextRun.Format(time.RFC3339),
	)

	// Wake up the loop to recalculate
	select {
	case s.updated <- struct{}{}:
	default:
	}
}

// Remove removes a camera from the merge schedule.
func (s *MergeScheduler) Remove(cameraID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.entries[cameraID]; ok {
		delete(s.entries, cameraID)
		slog.Debug("merge scheduler: removed camera", "camera_id", cameraID)

		// Wake up the loop
		select {
		case s.updated <- struct{}{}:
		default:
		}
	}
}

// GetDuration returns the configured merge duration for a camera.
// Returns false if the camera is not found.
func (s *MergeScheduler) GetDuration(cameraID string) (time.Duration, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.entries[cameraID]
	if !ok {
		return 0, false
	}
	return entry.duration, true
}

// Start begins the scheduler loop in a background goroutine.
// Call Stop to terminate the loop.
func (s *MergeScheduler) Start(ctx context.Context) {
	s.ctx, s.cancel = context.WithCancel(ctx)
	s.wg.Add(1)
	go s.runLoop()
}

// Stop terminates the scheduler loop and waits for it to finish.
func (s *MergeScheduler) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
	s.wg.Wait()
}

// TriggerDue immediately runs merge for all cameras whose nextRun has passed.
// Returns the number of cameras triggered. Used for testing.
// Does NOT block on merge completion — merges run in background goroutines.
func (s *MergeScheduler) TriggerDue(ctx context.Context) int {
	return s.triggerDueAt(ctx, time.Now().In(s.loc))
}

func (s *MergeScheduler) triggerDueAt(ctx context.Context, now time.Time) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.runFunc == nil {
		return 0
	}

	triggered := 0
	for id, entry := range s.entries {
		if !entry.nextRun.IsZero() && (now.After(entry.nextRun) || now.Equal(entry.nextRun)) {
			triggered++
			// Run merge in background — do not block the loop
			go func(camID string, refTime time.Time) {
				if err := s.runFunc(ctx, camID, refTime); err != nil {
					slog.Error("merge scheduler: merge failed",
						"camera_id", camID,
						"ref_time", refTime.Format(time.RFC3339),
						"error", err,
					)
				}
			}(id, now)

			// Recompute next run after this one
		entry.nextRun = computeNextRun(now, entry.duration, s.loc)
			slog.Debug("merge scheduler: triggered merge",
				"camera_id", id,
				"duration", entry.duration,
				"next_run", entry.nextRun.Format(time.RFC3339),
			)
		}
	}
	return triggered
}

// runLoop is the main scheduler loop.
// It finds the earliest next run time across all cameras, sleeps until then,
// triggers due merges, and repeats.
func (s *MergeScheduler) runLoop() {
	defer s.wg.Done()

	slog.Info("merge scheduler: loop started")
	defer slog.Info("merge scheduler: loop stopped")

	for {
		s.mu.Lock()
		// Find the earliest next run time
		var earliest time.Time
		for _, entry := range s.entries {
			if entry.nextRun.IsZero() {
			entry.nextRun = computeNextRun(time.Now().In(s.loc), entry.duration, s.loc)
			}
			if earliest.IsZero() || entry.nextRun.Before(earliest) {
				earliest = entry.nextRun
			}
		}
		s.mu.Unlock()

		// Determine how long to wait
		var waitDuration time.Duration
		if earliest.IsZero() {
			// No entries — check again in 1 minute
			waitDuration = time.Minute
		} else {
			waitDuration = time.Until(earliest)
			if waitDuration < 0 {
				waitDuration = 0
			}
		}

		// Wait for either the timer or an update signal
		select {
		case <-s.ctx.Done():
			return
		case <-s.updated:
			// Entries changed — recalculate immediately
			continue
		case <-time.After(waitDuration):
			// Time to check for due merges
		}

		// Trigger all due cameras
	s.triggerDueAt(s.ctx, time.Now().In(s.loc))
	}
}

// computeNextRun computes the next aligned boundary for the given duration
// in the provided timezone, strictly after (or at) now.
//
// Alignment rules:
//   - 8h:  next 00:00/08:00/16:00 local time
//   - 12h: next 00:00/12:00 local time
//   - 24h: next 00:00 local time
//   - 7d:  next Monday 00:00 local time
//   - 30d: 1st of next month 00:00 local time
func computeNextRun(now time.Time, dur time.Duration, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.UTC
	}
	now = now.In(loc)
	year, month, day := now.Date()

	// Calendar-month alignment for 30d.
	if dur == 30*24*time.Hour {
		next := time.Date(year, month+1, 1, 0, 0, 0, 0, loc)
		if !next.After(now) {
			next = time.Date(year, month+2, 1, 0, 0, 0, 0, loc)
		}
		return next
	}

	// Weekly alignment (7d): next Monday 00:00 local time.
	if dur == 7*24*time.Hour {
		weekday := now.Weekday()
		daysUntilMonday := (1 - int(weekday) + 7) % 7
		if daysUntilMonday == 0 {
			// Today is Monday; check if we're past midnight
			hour, minute, _ := now.Clock()
			if hour > 0 || minute > 0 {
				daysUntilMonday = 7
			}
		}
		next := time.Date(year, month, day+daysUntilMonday, 0, 0, 0, 0, loc)
		return next
	}

	// Duration-based alignment: find next time-of-day boundary.
	// For 24h: next midnight local
	// For 12h: next midnight or noon local
	// For 8h:  next 00:00/08:00/16:00 local
	durHours := int(dur.Hours())
	hour := now.Hour()
	alignedHour := ((hour / durHours) + 1) * durHours
	if alignedHour >= 24 {
		// Roll over to next day
		next := time.Date(year, month, day+1, 0, 0, 0, 0, loc)
		return next
	}
	next := time.Date(year, month, day, alignedHour, 0, 0, 0, loc)
	return next
}
