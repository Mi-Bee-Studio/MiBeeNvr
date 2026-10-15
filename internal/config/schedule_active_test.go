package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 2026-10-09 is a Friday.
func at(day, hh, mm int) time.Time {
	return time.Date(2026, 10, day, hh, mm, 0, 0, time.UTC)
}

func TestScheduleConfig_ActiveAt(t *testing.T) {
	t.Parallel()

	var nilSched *ScheduleConfig
	require.True(t, nilSched.ActiveAt(at(9, 3, 0)), "nil schedule = 24/7")

	// Weekdays from 15:00, weekends from 07:00, both until 23:00.
	weekly := &ScheduleConfig{TimeRanges: []TimeRange{
		{Start: "15:00", End: "23:00", DaysOfWeek: []int{1, 2, 3, 4, 5}},
		{Start: "07:00", End: "23:00", DaysOfWeek: []int{0, 6}},
	}}
	// Overnight Fri/Sat nights; the morning part belongs to the start day.
	overnight := &ScheduleConfig{TimeRanges: []TimeRange{{Start: "22:00", End: "06:00", DaysOfWeek: []int{5, 6}}}}
	// End 00:00 = until midnight.
	untilMidnight := &ScheduleConfig{TimeRanges: []TimeRange{{Start: "07:00", End: "00:00"}}}
	// Start == end = a full 24h; schedule-level days apply to ranges without their own.
	allDaySat := &ScheduleConfig{TimeRanges: []TimeRange{{Start: "00:00", End: "00:00"}}, DaysOfWeek: []int{6}}
	// No ranges = all day on the allowed days.
	daysOnly := &ScheduleConfig{DaysOfWeek: []int{0}}

	cases := []struct {
		name  string
		sched *ScheduleConfig
		t     time.Time
		want  bool
	}{
		{"weekday before 15:00", weekly, at(9, 10, 0), false},
		{"weekday after 15:00", weekly, at(9, 15, 0), true},
		{"weekday end exclusive", weekly, at(9, 23, 0), false},
		{"saturday morning", weekly, at(10, 7, 0), true},
		{"saturday before 07:00", weekly, at(10, 6, 59), false},
		{"sunday afternoon", weekly, at(11, 14, 0), true},

		{"fri night", overnight, at(9, 23, 30), true},
		{"sat early morning (fri range)", overnight, at(10, 3, 0), true},
		{"sun early morning (sat range)", overnight, at(11, 5, 59), true},
		{"sun 06:00 end exclusive", overnight, at(11, 6, 0), false},
		{"fri early morning (thu not allowed)", overnight, at(9, 3, 0), false},
		{"sun night (sun not allowed)", overnight, at(11, 22, 0), false},

		{"until midnight: 23:59", untilMidnight, at(9, 23, 59), true},
		{"until midnight: 00:00", untilMidnight, at(10, 0, 0), false},
		{"until midnight: 07:00", untilMidnight, at(10, 7, 0), true},

		{"full day sat 00:00", allDaySat, at(10, 0, 0), true},
		{"full day sat 23:59", allDaySat, at(10, 23, 59), true},
		{"full day: sun 00:00", allDaySat, at(11, 0, 0), false},
		{"full day: fri 23:59", allDaySat, at(9, 23, 59), false},

		{"days only: sunday", daysOnly, at(11, 12, 0), true},
		{"days only: monday", daysOnly, at(12, 12, 0), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, tc.sched.ActiveAt(tc.t))
		})
	}
}
