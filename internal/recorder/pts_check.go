package recorder

import "log/slog"

const (
	// pts90kHzClockRate is the RTP clock rate for H.264/H.265.
	pts90kHzClockRate = 90000

	// ptsMaxForwardJump is the maximum acceptable forward PTS jump before logging a warning.
	// 10 seconds in 90kHz clock = 900000 ticks.
	ptsMaxForwardJump = 10 * pts90kHzClockRate

	// ptsMaxBackwardJump is the maximum acceptable backward PTS jump before logging a warning.
	// More than 2 frame durations (~4ms at 25fps) backward is considered anomalous.
	ptsMaxBackwardJump = 2 * (pts90kHzClockRate / 25)
)

// ptsAnomalyKind describes the type of PTS anomaly detected.
type ptsAnomalyKind int

const (
	ptsAnomalyNone ptsAnomalyKind = iota
	ptsAnomalyBackwardJump
	ptsAnomalyExcessiveForwardJump
	ptsAnomalyWrapAround
)

// String returns a human-readable name for the anomaly kind.
func (k ptsAnomalyKind) String() string {
	switch k {
	case ptsAnomalyBackwardJump:
		return "backward_jump"
	case ptsAnomalyExcessiveForwardJump:
		return "excessive_forward_jump"
	case ptsAnomalyWrapAround:
		return "wrap_around"
	default:
		return "none"
	}
}

// ptsCheckResult holds the result of a PTS monotonicity check.
type ptsCheckResult struct {
	Anomaly  ptsAnomalyKind
	LastPTS  int64
	CurPTS   int64
	Delta    int64
	IsFirst  bool
}

// checkPTSMonotonicity checks if the current PTS is monotonic relative to the last PTS.
// It detects:
//   - Backward jumps (PTS decreases by more than ptsMaxBackwardJump)
//   - Excessive forward jumps (PTS increases by more than ptsMaxForwardJump)
//   - Wrap-around (large backward jump near the uint32 RTP timestamp boundary)
//
// This is a pure function with no side effects — the caller is responsible for logging.
func checkPTSMonotonicity(lastPTS, curPTS int64) ptsCheckResult {
	if lastPTS == 0 {
		return ptsCheckResult{IsFirst: true}
	}

	delta := curPTS - lastPTS

	// Wrap-around detection: RTP timestamps are 32-bit (uint32), so they wrap at 2^32.
	// A wrap-around manifests as a large negative delta. We detect it by checking
	// if curPTS is much smaller than lastPTS but the absolute difference suggests
	// a wrap (i.e., lastPTS is near the upper boundary and curPTS is near zero).
	if delta < 0 {
		// Check for wrap-around: lastPTS is high, curPTS is low,
		// and the "wrapped" forward delta is reasonable.
		wrappedDelta := curPTS + (1<<32) - lastPTS
		if wrappedDelta > 0 && wrappedDelta < ptsMaxForwardJump && lastPTS > (1<<32)/2 {
			return ptsCheckResult{
				Anomaly: ptsAnomalyWrapAround,
				LastPTS: lastPTS,
				CurPTS:  curPTS,
				Delta:   delta,
			}
		}

		// Genuine backward jump
		if -delta > ptsMaxBackwardJump {
			return ptsCheckResult{
				Anomaly: ptsAnomalyBackwardJump,
				LastPTS: lastPTS,
				CurPTS:  curPTS,
				Delta:   delta,
			}
		}

		// Small backward jitter — within tolerance, no anomaly
		return ptsCheckResult{}
	}

	// Excessive forward jump
	if delta > ptsMaxForwardJump {
		return ptsCheckResult{
			Anomaly: ptsAnomalyExcessiveForwardJump,
			LastPTS: lastPTS,
			CurPTS:  curPTS,
			Delta:   delta,
		}
	}

	return ptsCheckResult{}
}

// logPTSAnomaly logs a warning for the detected PTS anomaly.
func logPTSAnomaly(logger *slog.Logger, cameraID string, result ptsCheckResult) {
	logger.Warn("PTS anomaly detected",
		"camera_id", cameraID,
		"anomaly", result.Anomaly.String(),
		"last_pts", result.LastPTS,
		"current_pts", result.CurPTS,
		"delta", result.Delta,
	)
}
