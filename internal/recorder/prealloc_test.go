package recorder

import "testing"

func TestSegmentPreallocEstimate(t *testing.T) {
	cases := []struct {
		name string
		last int64
		want int64
	}{
		{"no history", 0, 0},
		{"tiny segment skipped", 1 << 20, 0},
		{"below floor skipped", 4<<20 - 1, 0},
		{"at floor", 4 << 20, 4<<20 + 4<<20/10},
		{"typical 30s H.264 (~100MiB)", 100 << 20, 110 << 20},
		{"capped at 512MiB", 2 << 30, 512 << 20},
	}
	for _, tc := range cases {
		if got := segmentPreallocEstimate(tc.last); got != tc.want {
			t.Errorf("%s: last=%d got %d, want %d", tc.name, tc.last, got, tc.want)
		}
	}
}
