package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateSchedule(t *testing.T) {
	t.Parallel()

	require.NoError(t, ValidateSchedule("s", nil))

	cases := []struct {
		name    string
		sched   ScheduleConfig
		wantErr string
	}{
		{"bad day", ScheduleConfig{DaysOfWeek: []int{7}}, "s.days_of_week[0] must be between 0 (Sunday) and 6 (Saturday)"},
		{"bad start", ScheduleConfig{TimeRanges: []TimeRange{{Start: "9am", End: "18:00"}}}, "s.time_ranges[0].start invalid time"},
		{"bad end", ScheduleConfig{TimeRanges: []TimeRange{{Start: "09:00", End: "25:00"}}}, "s.time_ranges[0].end invalid time"},
		{"bad range day", ScheduleConfig{TimeRanges: []TimeRange{{Start: "09:00", End: "18:00", DaysOfWeek: []int{-1}}}}, "s.time_ranges[0].days_of_week[0] must be between 0 (Sunday) and 6 (Saturday)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateSchedule("s", &tc.sched)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.wantErr)
		})
	}

	// Overlapping ranges are merged in place.
	s := &ScheduleConfig{TimeRanges: []TimeRange{{Start: "13:00", End: "18:00"}, {Start: "09:00", End: "14:00"}}}
	require.NoError(t, ValidateSchedule("s", s))
	require.Equal(t, []TimeRange{{Start: "09:00", End: "18:00"}}, s.TimeRanges)

	// Overnight ranges are valid; they and ranges with their own days are kept
	// as-is, after the merged plain ranges.
	s = &ScheduleConfig{TimeRanges: []TimeRange{
		{Start: "22:00", End: "06:00"},
		{Start: "07:00", End: "23:00", DaysOfWeek: []int{0, 6}},
		{Start: "15:00", End: "18:00"},
		{Start: "17:00", End: "23:00"},
	}}
	require.NoError(t, ValidateSchedule("s", s))
	require.Equal(t, []TimeRange{
		{Start: "15:00", End: "23:00"},
		{Start: "22:00", End: "06:00"},
		{Start: "07:00", End: "23:00", DaysOfWeek: []int{0, 6}},
	}, s.TimeRanges)
}
