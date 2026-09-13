package recorder

import "testing"

func TestSegmentPreallocEstimate(t *testing.T) {
	def := PreallocParams{} // zero value = documented defaults
	cases := []struct {
		name string
		last int64
		want int64
	}{
		{"no history", 0, 0},
		{"tiny segment skipped", 1 << 20, 0},
		{"below floor skipped", 4<<20 - 1, 0},
		{"at floor (+10%)", 4 << 20, 4<<20 + 4<<20/10},
		{"typical 30s H.264 (~100MiB)", 100 << 20, 110 << 20},
		{"capped at 512MiB", 2 << 30, 512 << 20},
	}
	for _, tc := range cases {
		if got := segmentPreallocEstimate(tc.last, def); got != tc.want {
			t.Errorf("%s: last=%d got %d, want %d", tc.name, tc.last, got, tc.want)
		}
	}
}

func TestSegmentPreallocEstimate_CustomParams(t *testing.T) {
	p := PreallocParams{HeadroomPercent: 25, MinBytes: 8 << 20, MaxBytes: 64 << 20}
	if got := segmentPreallocEstimate(16<<20, p); got != 20<<20 {
		t.Errorf("custom headroom: got %d, want 20MiB (+25%%)", got)
	}
	if got := segmentPreallocEstimate(4<<20, p); got != 0 {
		t.Errorf("below custom floor: got %d, want 0", got)
	}
	if got := segmentPreallocEstimate(1<<30, p); got != 64<<20 {
		t.Errorf("custom cap: got %d, want 64MiB", got)
	}
}

func TestSegmentPreallocEstimate_Disabled(t *testing.T) {
	// storage.prealloc_enabled: false disables entirely.
	p := PreallocParams{Disabled: true}
	if got := segmentPreallocEstimate(100<<20, p); got != 0 {
		t.Errorf("disabled prealloc: got %d, want 0", got)
	}
}
