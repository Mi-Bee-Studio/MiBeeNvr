// Package timelapse provides types and interfaces for timelapse recording and segment merging.
package timelapse

import (
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
)

// Scheduler evaluates timelapse recording schedules based on current time.
// Thread-safe when used read-only after config load.
type Scheduler struct {
	// now returns the current time. If nil, time.Now().In(s.loc) is used.
	// Exposed as a field for testing only — do not set in production code.
	now func() time.Time
	loc *time.Location
}

// NewScheduler creates a new Scheduler with default time source.
func NewScheduler(loc *time.Location) *Scheduler {
	if loc == nil {
		loc = time.Local
	}
	return &Scheduler{loc: loc}
}

// SetClockForTesting injects a fixed time source for deterministic tests.
// Pass nil to restore the default (time.Now().In(s.loc)). Test-only helper —
// do not call in production code. Solves wall-clock flakiness where a schedule
// window (e.g. 00:00-00:01) deterministically matches when CI runs at midnight
// UTC (issue #151).
func (s *Scheduler) SetClockForTesting(now func() time.Time) {
	s.now = now
}

// IsRecordingTime reports whether timelapse recording should be active
// based on the current time in the scheduler's timezone and the given schedule configuration.
//
// Rules:
//   - If Paused is true, always returns false.
//   - If Schedule is nil (no schedule), returns true (24/7 recording).
//   - If Schedule exists but DaysOfWeek is empty, all days are allowed.
//   - If Schedule exists but TimeRanges is empty, recording is all-day on allowed days.
//   - Otherwise, returns true only if the current day matches DaysOfWeek
//     AND current time falls within any configured TimeRange.
func (s *Scheduler) IsRecordingTime(cfg config.CameraTimelapseConfig) bool {
	return s.isRecordingTimeAt(s.getNow(), cfg)
}

func (s *Scheduler) isRecordingTimeAt(now time.Time, cfg config.CameraTimelapseConfig) bool {
	// Paused overrides everything.
	if cfg.Paused {
		return false
	}
	// No schedule means 24/7 recording; no time ranges means all-day on the
	// allowed days.
	return cfg.Schedule.ActiveAt(now)
}

// IsScheduleActive checks if the current time falls within the given ScheduleConfig.
// This is the generic version of IsRecordingTime that works with any ScheduleConfig
// (not just CameraTimelapseConfig), enabling reuse for recording schedules.
func (s *Scheduler) IsScheduleActive(schedule *config.ScheduleConfig) bool {
	// nil or no time ranges means 24/7 active.
	if schedule == nil || len(schedule.TimeRanges) == 0 {
		return true
	}
	return schedule.ActiveAt(s.getNow())
}

// NextTransition returns the duration until the next schedule state change
// (recording ↔ not recording). Returns 0 when:
//   - The schedule is nil (always recording — no transitions).
//   - The schedule is paused (always not recording — no transitions).
//   - No future transition is found within 7 days.
//
// The duration is computed at minute granularity matching the schedule
// definition (HH:MM).
func (s *Scheduler) NextTransition(cfg config.CameraTimelapseConfig) time.Duration {
	now := s.getNow()

	if cfg.Paused || cfg.Schedule == nil || len(cfg.Schedule.TimeRanges) == 0 {
		return 0
	}

	// Walk minute by minute (a schedule only changes on HH:MM boundaries)
	// up to a week ahead; cheap, and correct for overnight and per-range days.
	base := now.Truncate(time.Minute)
	current := cfg.Schedule.ActiveAt(base)
	for m := 1; m <= 8*24*60; m++ {
		if cfg.Schedule.ActiveAt(base.Add(time.Duration(m)*time.Minute)) != current {
			return time.Duration(m) * time.Minute
		}
	}
	return 0
}

// getNow returns the current time in the scheduler's configured timezone,
// or the injected mock time for testing.
func (s *Scheduler) getNow() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now().In(s.loc)
}
