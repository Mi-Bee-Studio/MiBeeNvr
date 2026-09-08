// SPDX-License-Identifier: MIT

package pixgate

import "time"

// statsInterval is how often the per-camera pixgate stats line is emitted
// (#699): the sampler's normal path is silent by design (per-sample decision
// logs were removed as noise), which left field diagnosis blind — a camera
// stuck in TIMELAPSE through traffic had no journal evidence of whether the
// sampler was running, how many samples validated, or what the last FG area
// was. One INFO line per interval restores that visibility cheaply.
const statsInterval = 60 * time.Second

// pixgateStats accumulates sampler counters between stats emissions. Not
// goroutine-safe: the sample callback that feeds it runs on one sampler
// goroutine per camera.
type pixgateStats struct {
	lastEmit time.Time

	samples     int // frames processed (excluding prime frames)
	valid       int // samples that count as activity evidence
	active      int // samples that confirmed activity (fired the trigger)
	lastAreaPct float64
}

// pixgateStatsLine is the snapshot handed to the logger when the interval
// elapses.
type pixgateStatsLine struct {
	Samples     int
	Valid       int
	Active      int
	LastAreaPct float64
}

// observe records one processed sample and reports whether the interval
// elapsed (only with actual samples — an idle camera doesn't spam zeros).
func (s *pixgateStats) observe(now time.Time, areaPct float64, valid, active bool) (pixgateStatsLine, bool) {
	s.samples++
	if valid {
		s.valid++
	}
	if active {
		s.active++
	}
	s.lastAreaPct = areaPct
	if !s.lastEmit.IsZero() && now.Sub(s.lastEmit) < statsInterval {
		return pixgateStatsLine{}, false
	}
	line := pixgateStatsLine{
		Samples:     s.samples,
		Valid:       s.valid,
		Active:      s.active,
		LastAreaPct: s.lastAreaPct,
	}
	s.lastEmit = now
	s.samples, s.valid, s.active = 0, 0, 0
	return line, true
}
