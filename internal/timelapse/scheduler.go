// Package timelapse provides types and interfaces for timelapse recording and segment merging.
package timelapse

import (
	"sort"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
)

// Scheduler evaluates timelapse recording schedules based on current time.
// Thread-safe when used read-only after config load.
type Scheduler struct {
	// now returns the current time. If nil, time.Now().UTC() is used.
	// Exposed as a field for testing only — do not set in production code.
	now func() time.Time
}

// NewScheduler creates a new Scheduler with default time source.
func NewScheduler() *Scheduler {
	return &Scheduler{}
}

// IsRecordingTime reports whether timelapse recording should be active
// based on the current UTC time and the given schedule configuration.
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

	// No schedule means 24/7 recording.
	if cfg.Schedule == nil {
		return true
	}

	// Check day-of-week restriction.
	if len(cfg.Schedule.DaysOfWeek) > 0 {
		weekday := int(now.Weekday()) // 0=Sunday, 1=Monday, …, 6=Saturday
		dayMatch := false
		for _, d := range cfg.Schedule.DaysOfWeek {
			if d == weekday {
				dayMatch = true
				break
			}
		}
		if !dayMatch {
			return false
		}
	}

	// No time ranges means all-day recording on allowed days.
	if len(cfg.Schedule.TimeRanges) == 0 {
		return true
	}

	currentMinutes := now.Hour()*60 + now.Minute()

	for _, tr := range cfg.Schedule.TimeRanges {
		startH, startM := parseHHMM(tr.Start)
		endH, endM := parseHHMM(tr.End)
		startMinutes := startH*60 + startM
		endMinutes := endH*60 + endM

		if currentMinutes >= startMinutes && currentMinutes < endMinutes {
			return true
		}
	}

	return false
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

	currentMins := now.Hour()*60 + now.Minute()

	// Scan up to 7 days ahead for the next transition.
	for dayOffset := 0; dayOffset <= 7; dayOffset++ {
		checkDate := now.AddDate(0, 0, dayOffset)
		checkDay := int(checkDate.Weekday())

		dayAllowed := len(cfg.Schedule.DaysOfWeek) == 0
		if !dayAllowed {
			for _, d := range cfg.Schedule.DaysOfWeek {
				if d == checkDay {
					dayAllowed = true
					break
				}
			}
		}

		// Start scanning from the beginning of the day for future days,
		// or from the current minute for today.
		checkStartMins := 0
		if dayOffset == 0 {
			checkStartMins = currentMins
		}

		type transition struct {
			minutes int  // minutes from midnight
			turnsOn bool // true = state becomes recording
		}

		var transitions []transition

		if dayAllowed {
			for _, tr := range cfg.Schedule.TimeRanges {
				startH, startM := parseHHMM(tr.Start)
				endH, endM := parseHHMM(tr.End)
				startMinutes := startH*60 + startM
				endMinutes := endH*60 + endM
				transitions = append(transitions, transition{startMinutes, true})
				transitions = append(transitions, transition{endMinutes, false})
			}
		}

		// Sort transitions by time of day.
		sort.Slice(transitions, func(i, j int) bool {
			return transitions[i].minutes < transitions[j].minutes
		})

		for _, t := range transitions {
			if t.minutes > checkStartMins {
				totalMins := dayOffset*24*60 + t.minutes - currentMins
				return time.Duration(totalMins) * time.Minute
			}
		}
	}

	return 0
}

// getNow returns the current UTC time, or the injected mock time for testing.
func (s *Scheduler) getNow() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now().UTC()
}

// parseHHMM parses a validated HH:MM string into hours and minutes.
// The input MUST already be validated by config validation.
func parseHHMM(s string) (hours, minutes int) {
	if len(s) != 5 || s[2] != ':' {
		return 0, 0
	}
	hours = int(s[0]-'0')*10 + int(s[1]-'0')
	minutes = int(s[3]-'0')*10 + int(s[4]-'0')
	return hours, minutes
}
