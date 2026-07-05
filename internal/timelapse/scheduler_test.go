package timelapse

import (
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
)

// mockNow returns a fixed time for testing.
// June 6, 2026 is a Saturday (weekday=6).
var (
	saturday0630 = time.Date(2026, 6, 6, 6, 30, 0, 0, time.UTC)  // 06:30 Saturday
	saturday1030 = time.Date(2026, 6, 6, 10, 30, 0, 0, time.UTC) // 10:30 Saturday
	saturday0800 = time.Date(2026, 6, 6, 8, 0, 0, 0, time.UTC)   // 08:00 Saturday
	saturday1700 = time.Date(2026, 6, 6, 17, 0, 0, 0, time.UTC)  // 17:00 Saturday
	saturday2359 = time.Date(2026, 6, 6, 23, 59, 0, 0, time.UTC) // 23:59 Saturday
	saturday0000 = time.Date(2026, 6, 6, 0, 0, 0, 0, time.UTC)   // 00:00 Saturday
	sunday0000   = time.Date(2026, 6, 7, 0, 0, 0, 0, time.UTC)   // 00:00 Sunday (day=0)
	monday0900   = time.Date(2026, 6, 8, 9, 0, 0, 0, time.UTC)   // 09:00 Monday (day=1)
)

func TestScheduler_IsRecordingTime(t *testing.T) {
	t.Run("within range + matching day", func(t *testing.T) {
		s := &Scheduler{now: func() time.Time { return saturday1030 }}
		cfg := config.CameraTimelapseConfig{
			Schedule: &config.ScheduleConfig{
				TimeRanges: []config.TimeRange{{Start: "09:00", End: "17:00"}},
				DaysOfWeek: []int{6}, // Saturday
			},
		}
		if !s.IsRecordingTime(cfg) {
			t.Error("expected true: 10:30 Saturday is within 09:00-17:00 on Saturday")
		}
	})

	t.Run("outside time range", func(t *testing.T) {
		s := &Scheduler{now: func() time.Time { return saturday0800 }}
		cfg := config.CameraTimelapseConfig{
			Schedule: &config.ScheduleConfig{
				TimeRanges: []config.TimeRange{{Start: "09:00", End: "17:00"}},
				DaysOfWeek: []int{6},
			},
		}
		if s.IsRecordingTime(cfg) {
			t.Error("expected false: 08:00 is before 09:00-17:00")
		}
	})

	t.Run("wrong day of week", func(t *testing.T) {
		s := &Scheduler{now: func() time.Time { return saturday1030 }}
		cfg := config.CameraTimelapseConfig{
			Schedule: &config.ScheduleConfig{
				TimeRanges: []config.TimeRange{{Start: "09:00", End: "17:00"}},
				DaysOfWeek: []int{1, 2, 3, 4, 5}, // weekdays only
			},
		}
		if s.IsRecordingTime(cfg) {
			t.Error("expected false: Saturday is not in weekdays schedule")
		}
	})

	t.Run("paused returns false", func(t *testing.T) {
		s := &Scheduler{now: func() time.Time { return saturday1030 }}
		cfg := config.CameraTimelapseConfig{
			Paused: true,
			Schedule: &config.ScheduleConfig{
				TimeRanges: []config.TimeRange{{Start: "00:00", End: "23:59"}},
				DaysOfWeek: []int{6},
			},
		}
		if s.IsRecordingTime(cfg) {
			t.Error("expected false: paused overrides schedule")
		}
	})

	t.Run("no schedule returns true (24/7)", func(t *testing.T) {
		s := &Scheduler{now: func() time.Time { return saturday1030 }}
		cfg := config.CameraTimelapseConfig{
			Schedule: nil,
		}
		if !s.IsRecordingTime(cfg) {
			t.Error("expected true: nil schedule means 24/7")
		}
	})

	t.Run("paused with nil schedule returns false", func(t *testing.T) {
		s := NewScheduler(time.UTC)
		cfg := config.CameraTimelapseConfig{
			Paused:   true,
			Schedule: nil,
		}
		if s.IsRecordingTime(cfg) {
			t.Error("expected false: paused overrides even nil schedule")
		}
	})

	t.Run("all-day schedule on matching day", func(t *testing.T) {
		s := &Scheduler{now: func() time.Time { return saturday1030 }}
		cfg := config.CameraTimelapseConfig{
			Schedule: &config.ScheduleConfig{
				TimeRanges: nil, // no time ranges = all day
				DaysOfWeek: []int{6},
			},
		}
		if !s.IsRecordingTime(cfg) {
			t.Error("expected true: empty TimeRanges means all-day on matching day")
		}
	})

	t.Run("all-day schedule on non-matching day", func(t *testing.T) {
		s := &Scheduler{now: func() time.Time { return sunday0000 }}
		cfg := config.CameraTimelapseConfig{
			Schedule: &config.ScheduleConfig{
				TimeRanges: nil,
				DaysOfWeek: []int{6}, // Saturday only
			},
		}
		if s.IsRecordingTime(cfg) {
			t.Error("expected false: Sunday is not Saturday")
		}
	})

	t.Run("at start boundary (09:00 exactly)", func(t *testing.T) {
		s := &Scheduler{now: func() time.Time {
			return time.Date(2026, 6, 6, 9, 0, 0, 0, time.UTC)
		}}
		cfg := config.CameraTimelapseConfig{
			Schedule: &config.ScheduleConfig{
				TimeRanges: []config.TimeRange{{Start: "09:00", End: "17:00"}},
				DaysOfWeek: []int{6},
			},
		}
		if !s.IsRecordingTime(cfg) {
			t.Error("expected true: 09:00 is within [09:00, 17:00)")
		}
	})

	t.Run("at end boundary (17:00 exactly) returns false", func(t *testing.T) {
		s := &Scheduler{now: func() time.Time { return saturday1700 }}
		cfg := config.CameraTimelapseConfig{
			Schedule: &config.ScheduleConfig{
				TimeRanges: []config.TimeRange{{Start: "09:00", End: "17:00"}},
				DaysOfWeek: []int{6},
			},
		}
		if s.IsRecordingTime(cfg) {
			t.Error("expected false: 17:00 is at exclusive end boundary")
		}
	})

	t.Run("midnight boundary — 23:59 is exclusive end of 00:00-23:59", func(t *testing.T) {
		s := &Scheduler{now: func() time.Time { return saturday2359 }}
		cfg := config.CameraTimelapseConfig{
			Schedule: &config.ScheduleConfig{
				TimeRanges: []config.TimeRange{{Start: "00:00", End: "23:59"}},
			},
		}
		// 23:59 is the exclusive end boundary of [00:00, 23:59).
		if s.IsRecordingTime(cfg) {
			t.Error("expected false: 23:59 is exclusive end of [00:00, 23:59)")
		}
	})

	t.Run("midnight boundary — 00:00 within 00:00-23:59", func(t *testing.T) {
		s := &Scheduler{now: func() time.Time { return saturday0000 }}
		cfg := config.CameraTimelapseConfig{
			Schedule: &config.ScheduleConfig{
				TimeRanges: []config.TimeRange{{Start: "00:00", End: "23:59"}},
			},
		}
		if !s.IsRecordingTime(cfg) {
			t.Error("expected true: 00:00 is within [00:00, 23:59)")
		}
	})

	t.Run("midnight boundary — 00:00 after 23:59 end", func(t *testing.T) {
		s := &Scheduler{now: func() time.Time { return saturday0000 }}
		cfg := config.CameraTimelapseConfig{
			Schedule: &config.ScheduleConfig{
				TimeRanges: []config.TimeRange{{Start: "23:00", End: "23:59"}},
			},
		}
		if s.IsRecordingTime(cfg) {
			t.Error("expected false: 00:00 is after 23:59 end")
		}
	})

	t.Run("multiple time ranges — second range match", func(t *testing.T) {
		s := &Scheduler{now: func() time.Time { return saturday1030 }}
		cfg := config.CameraTimelapseConfig{
			Schedule: &config.ScheduleConfig{
				TimeRanges: []config.TimeRange{
					{Start: "06:00", End: "08:00"},
					{Start: "09:00", End: "17:00"},
				},
				DaysOfWeek: []int{6},
			},
		}
		if !s.IsRecordingTime(cfg) {
			t.Error("expected true: 10:30 is within second range")
		}
	})

	t.Run("multiple time ranges — gap between ranges", func(t *testing.T) {
		s := &Scheduler{now: func() time.Time {
			return time.Date(2026, 6, 6, 8, 30, 0, 0, time.UTC)
		}}
		cfg := config.CameraTimelapseConfig{
			Schedule: &config.ScheduleConfig{
				TimeRanges: []config.TimeRange{
					{Start: "06:00", End: "08:00"},
					{Start: "09:00", End: "17:00"},
				},
				DaysOfWeek: []int{6},
			},
		}
		if s.IsRecordingTime(cfg) {
			t.Error("expected false: 08:30 is in gap between ranges")
		}
	})

	t.Run("schedule with DaysOfWeek empty means all days", func(t *testing.T) {
		s := &Scheduler{now: func() time.Time { return saturday1030 }}
		cfg := config.CameraTimelapseConfig{
			Schedule: &config.ScheduleConfig{
				TimeRanges: []config.TimeRange{{Start: "09:00", End: "17:00"}},
				DaysOfWeek: nil, // all days
			},
		}
		if !s.IsRecordingTime(cfg) {
			t.Error("expected true: empty DaysOfWeek means all days allowed")
		}
	})
}

func TestScheduler_NextTransition(t *testing.T) {
	t.Run("currently recording — next transition at range end", func(t *testing.T) {
		s := &Scheduler{now: func() time.Time { return saturday1030 }}
		cfg := config.CameraTimelapseConfig{
			Schedule: &config.ScheduleConfig{
				TimeRanges: []config.TimeRange{{Start: "09:00", End: "17:00"}},
				DaysOfWeek: []int{6},
			},
		}
		// 17:00 - 10:30 = 6h30m
		got := s.NextTransition(cfg)
		want := 6*time.Hour + 30*time.Minute
		if got != want {
			t.Errorf("NextTransition = %v, want %v", got, want)
		}
	})

	t.Run("not recording — next transition at range start", func(t *testing.T) {
		s := &Scheduler{now: func() time.Time { return saturday0800 }}
		cfg := config.CameraTimelapseConfig{
			Schedule: &config.ScheduleConfig{
				TimeRanges: []config.TimeRange{{Start: "09:00", End: "17:00"}},
				DaysOfWeek: []int{6},
			},
		}
		// 09:00 - 08:00 = 1h
		got := s.NextTransition(cfg)
		want := 1 * time.Hour
		if got != want {
			t.Errorf("NextTransition = %v, want %v", got, want)
		}
	})

	t.Run("no schedule returns 0", func(t *testing.T) {
		s := NewScheduler(time.UTC)
		cfg := config.CameraTimelapseConfig{Schedule: nil}
		got := s.NextTransition(cfg)
		if got != 0 {
			t.Errorf("NextTransition for nil schedule = %v, want 0", got)
		}
	})

	t.Run("paused returns 0", func(t *testing.T) {
		s := NewScheduler(time.UTC)
		cfg := config.CameraTimelapseConfig{Paused: true}
		got := s.NextTransition(cfg)
		if got != 0 {
			t.Errorf("NextTransition when paused = %v, want 0", got)
		}
	})

	t.Run("empty TimeRanges returns 0 (all day)", func(t *testing.T) {
		s := NewScheduler(time.UTC)
		cfg := config.CameraTimelapseConfig{
			Schedule: &config.ScheduleConfig{
				TimeRanges: nil,
				DaysOfWeek: []int{6},
			},
		}
		got := s.NextTransition(cfg)
		if got != 0 {
			t.Errorf("NextTransition with empty TimeRanges = %v, want 0", got)
		}
	})

	t.Run("crossing day boundary — Saturday to Monday", func(t *testing.T) {
		sat2300 := time.Date(2026, 6, 6, 23, 0, 0, 0, time.UTC)
		s := &Scheduler{now: func() time.Time { return sat2300 }}
		cfg := config.CameraTimelapseConfig{
			Schedule: &config.ScheduleConfig{
				TimeRanges: []config.TimeRange{{Start: "09:00", End: "17:00"}},
				DaysOfWeek: []int{1, 2, 3, 4, 5}, // Mon-Fri
			},
		}
		// Currently not recording (Saturday). Next: Monday 09:00.
		// Sat 23:00 -> Sun 00:00 = 1h + Sun 00:00 -> Mon 00:00 = 24h + Mon 00:00 -> 09:00 = 9h
		got := s.NextTransition(cfg)
		want := 34 * time.Hour
		if got != want {
			t.Errorf("NextTransition = %v, want %v", got, want)
		}
	})

	t.Run("end of day — next transition next week", func(t *testing.T) {
		sat2200 := time.Date(2026, 6, 6, 22, 0, 0, 0, time.UTC)
		s := &Scheduler{now: func() time.Time { return sat2200 }}
		cfg := config.CameraTimelapseConfig{
			Schedule: &config.ScheduleConfig{
				TimeRanges: []config.TimeRange{{Start: "09:00", End: "17:00"}},
				DaysOfWeek: []int{6}, // Saturday only
			},
		}
		// Sat 22:00 (not recording, past range) -> next Sat 09:00 = 6d + 11h = 155h
		// Sat 22:00 to Sun 00:00 = 2h + 6d (Sun-Mon-Tue-Wed-Thu-Fri-Sat) to next Sat 00:00 = 144h + 9h = 155h
		got := s.NextTransition(cfg)
		want := 155 * time.Hour
		if got != want {
			t.Errorf("NextTransition = %v, want %v", got, want)
		}
	})

	t.Run("between two ranges on same day — next at second range start", func(t *testing.T) {
		eight30 := time.Date(2026, 6, 6, 8, 30, 0, 0, time.UTC)
		s := &Scheduler{now: func() time.Time { return eight30 }}
		cfg := config.CameraTimelapseConfig{
			Schedule: &config.ScheduleConfig{
				TimeRanges: []config.TimeRange{
					{Start: "06:00", End: "08:00"},
					{Start: "09:00", End: "17:00"},
				},
				DaysOfWeek: []int{6},
			},
		}
		// 09:00 - 08:30 = 30m
		got := s.NextTransition(cfg)
		want := 30 * time.Minute
		if got != want {
			t.Errorf("NextTransition = %v, want %v", got, want)
		}
	})

	t.Run("at exact end boundary — next is next day start", func(t *testing.T) {
		s := &Scheduler{now: func() time.Time { return saturday1700 }}
		cfg := config.CameraTimelapseConfig{
			Schedule: &config.ScheduleConfig{
				TimeRanges: []config.TimeRange{{Start: "09:00", End: "17:00"}},
				DaysOfWeek: []int{6, 0}, // Saturday and Sunday
			},
		}
		// Currently not recording (at end boundary). Next: Sunday 09:00.
		// Sat 17:00 -> Sun 00:00 = 7h + Sun 00:00 -> 09:00 = 9h = 16h
		got := s.NextTransition(cfg)
		want := 16 * time.Hour
		if got != want {
			t.Errorf("NextTransition = %v, want %v", got, want)
		}
	})
}

func TestScheduler_Timezone(t *testing.T) {
	t.Helper()
	// Create scheduler with Asia/Shanghai timezone (UTC+8)
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("failed to load Asia/Shanghai: %v", err)
	}
	s := NewScheduler(loc)

	// Monday 09:00 Shanghai = Monday 01:00 UTC
	// Create a fixed time function for testing with Shanghai timezone
	monSH := time.Date(2026, 6, 8, 9, 0, 0, 0, loc) // Monday 09:00 Shanghai
	s.now = func() time.Time { return monSH }

	// Schedule that allows Monday 09:00-17:00 Shanghai time
	cfg := config.CameraTimelapseConfig{
		Schedule: &config.ScheduleConfig{
			DaysOfWeek: []int{1}, // Monday
			TimeRanges: []config.TimeRange{
				{Start: "09:00", End: "17:00"},
			},
		},
	}

	if !s.IsRecordingTime(cfg) {
		t.Error("expected recording time: Monday 09:00 Shanghai should match Monday 09:00-17:00")
	}

	// Tuesday 07:00 Shanghai = Monday 23:00 UTC (different day!)
	tuesSH := time.Date(2026, 6, 9, 7, 0, 0, 0, loc) // Tuesday 07:00 Shanghai
	s.now = func() time.Time { return tuesSH }

	if s.IsRecordingTime(cfg) {
		t.Error("expected NOT recording time: Tuesday 07:00 Shanghai, schedule is Monday only")
	}
}
