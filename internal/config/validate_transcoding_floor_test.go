package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestValidateTranscodingMinSegmentDuration pins the #848 floor bounds:
// 0 (off) and 1..3600 are valid; negative and >3600 are rejected.
func TestValidateTranscodingMinSegmentDuration(t *testing.T) {
	valid := []int{0, 1, 30, 3600}
	for _, v := range valid {
		cfg := &Config{}
		cfg.ApplyDefaults()
		cfg.Transcoding.MinSegmentDurationS = v
		require.NoError(t, Validate(cfg), "value %d should be valid", v)
	}

	for _, v := range []int{-1, 3601} {
		cfg := &Config{}
		cfg.ApplyDefaults()
		cfg.Transcoding.MinSegmentDurationS = v
		err := Validate(cfg)
		require.ErrorContains(t, err, "transcoding.min_segment_duration_s", "value %d should be rejected", v)
	}
}
