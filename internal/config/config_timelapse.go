package config

import (
	"fmt"
	"time"
)

// Timelapse configuration types and scheduling helpers.

// TimeRange defines a start and end time for timelapse scheduling.
type TimeRange struct {
	Start string `yaml:"start,omitempty" json:"start,omitempty"` // HH:MM format (24h)
	End   string `yaml:"end,omitempty" json:"end,omitempty"`     // HH:MM format (24h)
}

// ScheduleConfig defines when timelapse recording should be active.
type ScheduleConfig struct {
	// TimeRanges specifies the time windows for recording (e.g., 09:00-17:00).
	// Multiple ranges are supported and overlapping ranges are auto-merged.
	TimeRanges []TimeRange `yaml:"time_ranges,omitempty" json:"time_ranges,omitempty"`
	// DaysOfWeek restricts recording to specific days (0=Sunday, 1=Monday, ..., 6=Saturday).
	// Empty or nil means all days.
	DaysOfWeek []int `yaml:"days_of_week,omitempty" json:"days_of_week,omitempty"`
}

type CameraTimelapseConfig struct {
	Enabled        bool            `yaml:"enabled" json:"enabled"`                                     // default false
	Interval       string          `yaml:"interval,omitempty" json:"interval,omitempty"`               // snapshot interval, default "30s", min 1s
	FrameSource    string          `yaml:"frame_source,omitempty" json:"frame_source,omitempty"`       // auto, snapshot, rtsp_keyframe, mjpeg — default auto
	SnapshotURL    string          `yaml:"snapshot_url,omitempty" json:"snapshot_url,omitempty"`       // URL for snapshot source (required when frame_source=snapshot)
	Schedule       *ScheduleConfig `yaml:"schedule,omitempty" json:"schedule,omitempty"`               // nil = 24/7 recording
	Paused         bool            `yaml:"paused" json:"paused"`                                       // pause timelapse recording, default false
	DeleteOriginal bool            `yaml:"delete_original,omitempty" json:"delete_original,omitempty"` // remove original segments after timelapse, default false
	MergeEnabled   *bool           `yaml:"merge_enabled,omitempty" json:"merge_enabled,omitempty"`     // auto-detect (nil=auto)
	MergeMode      string          `yaml:"merge_mode,omitempty" json:"merge_mode,omitempty"`           // auto, mp4, jpeg — default auto
	DailyMerge     *bool           `yaml:"daily_merge,omitempty" json:"daily_merge,omitempty"`         // default true
	MergeDuration  string          `yaml:"merge_duration,omitempty" json:"merge_duration,omitempty"`
	MergeOutputFPS int             `yaml:"merge_output_fps,omitempty" json:"merge_output_fps,omitempty"` // default 30, range 1-60
	// RetainIntermediateMP4 controls whether the per-segment .mp4 files
	// produced by rolling merge are kept after a periodic (8h/24h/7d/30d)
	// merge has folded them into a long-window output. Defaults to false
	// (clean up) to reclaim the ~1.5GB/day/camera we observed in production.
	// Set to true to keep them for debugging or re-merge safety. The original
	// raw frame directories (frame_*.h264 / .h265 / .jpg) are always preserved
	// regardless of this flag — only the rolling-merge .mp4 outputs are pruned.
	RetainIntermediateMP4 *bool `yaml:"retain_intermediate_mp4,omitempty" json:"retain_intermediate_mp4,omitempty"`
}

// RetainIntermediateMP4Value returns the effective bool value of
// RetainIntermediateMP4, defaulting to false (clean up) when nil. Callers
// must use this accessor rather than dereferencing the pointer directly.
func (c *CameraTimelapseConfig) RetainIntermediateMP4Value() bool {
	if c == nil || c.RetainIntermediateMP4 == nil {
		return false
	}
	return *c.RetainIntermediateMP4
}

// parseHHMM parses a time string in HH:MM format and returns hours and minutes.
func parseHHMM(s string) (hours, minutes int, err error) {
	if len(s) != 5 || s[2] != ':' {
		return 0, 0, fmt.Errorf("invalid time format %q, expected HH:MM", s)
	}
	hours = int(s[0]-'0')*10 + int(s[1]-'0')
	minutes = int(s[3]-'0')*10 + int(s[4]-'0')
	if hours < 0 || hours > 23 || minutes < 0 || minutes > 59 {
		return 0, 0, fmt.Errorf("invalid time %q, hours must be 00-23, minutes 00-59", s)
	}
	return hours, minutes, nil
}

// mergeTimeRanges merges overlapping or adjacent time ranges and returns a non-overlapping sorted list.
func mergeTimeRanges(ranges []TimeRange) []TimeRange {
	if len(ranges) <= 1 {
		return ranges
	}
	// Convert to minutes since midnight for sorting
	type tr struct {
		start, end int
		original   TimeRange
	}
	parsed := make([]tr, len(ranges))
	for i, r := range ranges {
		sh, sm, _ := parseHHMM(r.Start)
		eh, em, _ := parseHHMM(r.End)
		parsed[i] = tr{start: sh*60 + sm, end: eh*60 + em, original: r}
	}
	// Sort by start time
	for i := 0; i < len(parsed); i++ {
		for j := i + 1; j < len(parsed); j++ {
			if parsed[j].start < parsed[i].start {
				parsed[i], parsed[j] = parsed[j], parsed[i]
			}
		}
	}
	// Merge overlapping/adjacent ranges
	merged := []tr{parsed[0]}
	for i := 1; i < len(parsed); i++ {
		last := &merged[len(merged)-1]
		if parsed[i].start <= last.end {
			// Overlapping or adjacent — extend end if needed
			if parsed[i].end > last.end {
				last.end = parsed[i].end
			}
		} else {
			merged = append(merged, parsed[i])
		}
	}
	// Convert back to TimeRanges
	result := make([]TimeRange, len(merged))
	for i, m := range merged {
		result[i] = TimeRange{
			Start: fmt.Sprintf("%02d:%02d", m.start/60, m.start%60),
			End:   fmt.Sprintf("%02d:%02d", m.end/60, m.end%60),
		}
	}
	return result
}

// ParseMergeDuration parses a MergeDuration value and returns the corresponding
// time.Duration used by the timelapse periodic-merge scheduler.
//
// Supported values (aligned in the configured app timezone by
// internal/timelapse.parseMergeRange / computeNextRun):
//
//   - ""            → default (time.Hour, "1h")
//   - "natural-day" → 24h, aligned to local midnight (the calendar-day window)
//   - "8h"          → 8h, aligned to 00:00 / 08:00 / 16:00 local
//   - "12h"         → 12h, aligned to 00:00 / 12:00 local
//   - "24h"         → 24h, aligned to local midnight (alias of natural-day)
//   - "7d"          → 7×24h, aligned to Monday 00:00 local
//   - "30d"         → 30×24h, aligned to the 1st of the month 00:00 local
//
// Arbitrary Go duration strings (e.g. "30m", "2h", "6h") are also accepted as
// long as they are positive and ≤ 30 days. Sub-hour durations align to
// midnight in fractional-hour buckets.
//
// Windows >1h are supported because timelapse merge — unlike the recording
// rolling-window merge — runs in the configured timezone (not UTC) and
// stores output under per-camera directories (periodic-merge/<camera_id>/),
// so crossing a UTC day boundary has no bucketing or IO-amplification cost.
// The 1h cap that previously applied here was reverted in Timelapse v3.
func ParseMergeDuration(s string) (time.Duration, error) {
	// Named windows — these are the canonical values exposed in the UI
	// dropdowns and match the alignment rules in parseMergeRange /
	// computeNextRun. "" is the "unset" default.
	switch s {
	case "":
		return time.Hour, nil
	case "natural-day", "24h":
		return 24 * time.Hour, nil
	case "8h":
		return 8 * time.Hour, nil
	case "12h":
		return 12 * time.Hour, nil
	case "7d":
		return 7 * 24 * time.Hour, nil
	case "30d":
		return 30 * 24 * time.Hour, nil
	}
	// Any other string must parse as a Go duration.
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid merge duration %q: %w", s, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("invalid merge duration %q: must be a positive duration", s)
	}
	const maxMergeDuration = 30 * 24 * time.Hour
	if d > maxMergeDuration {
		return 0, fmt.Errorf("invalid merge duration %q: must be ≤ 30d (the largest named window)", s)
	}
	return d, nil
}
