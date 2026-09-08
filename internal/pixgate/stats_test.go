// SPDX-License-Identifier: MIT

package pixgate

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPixgateStatsEmitsPerInterval(t *testing.T) {
	var s pixgateStats
	start := time.Now()

	// The first observed sample emits immediately — instant proof the
	// sampler is live (#699: "is pixgate even running?" is the first field
	// question) — and initializes the window.
	first, emit := s.observe(start, 1.0, true, false)
	require.True(t, emit)
	require.Equal(t, 1, first.Samples)
	require.Equal(t, 1, first.Valid)
	require.Equal(t, 0, first.Active)
	require.Equal(t, 1.0, first.LastAreaPct)

	// Samples inside the window accumulate silently.
	for i := 1; i <= 5; i++ {
		line, emit := s.observe(start.Add(time.Duration(i)*time.Second), 2.0, true, i == 3)
		require.False(t, emit)
		require.Empty(t, line)
	}

	// Crossing the interval emits the accumulated counters and resets.
	line, emit := s.observe(start.Add(statsInterval+time.Second), 3.5, true, true)
	require.True(t, emit)
	require.Equal(t, 6, line.Samples, "5 in-window samples + the crossing one")
	require.Equal(t, 6, line.Valid)
	require.Equal(t, 2, line.Active, "the i==3 sample and the crossing one")
	require.Equal(t, 3.5, line.LastAreaPct)

	// Counters reset after emission.
	line2, emit := s.observe(start.Add(statsInterval+2*time.Second), 0.5, false, false)
	require.False(t, emit)
	require.Empty(t, line2)

	line3, emit := s.observe(start.Add(2*statsInterval+2*time.Second), 0.5, false, false)
	require.True(t, emit)
	require.Equal(t, 2, line3.Samples, "post-reset window counts fresh samples only")
	require.Equal(t, 0, line3.Valid)
	require.Equal(t, 0, line3.Active)
}
