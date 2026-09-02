// replay.go — offline gate replay for the evaluation framework (#639).
//
// The adaptive tracker's behavior is a pure function of the P-frame size
// series it observes (plus config), so a finished recording's MP4 sample
// table is enough to answer "what would the gate have done with these
// settings?" — no camera, no decode, microseconds per segment. The
// eval-replay CLI (cmd/mibee-nvr/eval_replay.go) drives this over a golden
// corpus so every future gate/scorer change ships with a before/after table
// instead of a field gamble.

package recorder

import (
	"io"
	"log/slog"
	"time"
)

// ReplayFrame is one compressed-domain observation for gate replay: the byte
// size and keyframe flag of one video sample, as parsed from an MP4 stsz/stss
// table (merge.ParseSegmentNoProbe).
type ReplayFrame struct {
	Size       int
	IsKeyframe bool
}

// ReplayStats summarizes a gate replay over a frame series.
type ReplayStats struct {
	Frames     int
	TLFrames   int     // frames observed while in TIMELAPSE
	TLShare    float64 // TLFrames / Frames — the sparse-write share
	Switches   int     // mode transitions (TL↔NORMAL churn)
	NoiseFloor float64 // final effective absolute noise floor (#635)
}

// ReplayAdaptive feeds a frame-size series through a fresh adaptiveTracker at
// a uniform frame interval and returns the mode statistics. Times are
// synthetic (t0 + i×interval); interval should approximate the source
// recording's real cadence (frames / duration) so CalmThreshold and burst
// windows replay faithfully. The tracker's GOP ring is exercised too (exits
// flush internally), but disk-write accounting is out of scope here.
func ReplayAdaptive(frames []ReplayFrame, interval time.Duration, cfg AdaptiveConfig) ReplayStats {
	tr := newAdaptiveTracker(cfg, "replay", slog.New(slog.NewTextHandler(io.Discard, nil)))
	t0 := time.Now()
	st := ReplayStats{Frames: len(frames)}
	if interval <= 0 {
		interval = 50 * time.Millisecond // 20fps default
	}
	prev := tr.mode
	nalu := make([]byte, 0, 64<<10)
	for i, f := range frames {
		if cap(nalu) < f.Size {
			nalu = make([]byte, f.Size)
		}
		nalu = nalu[:f.Size]
		tr.observe(nalu, f.IsKeyframe, t0.Add(time.Duration(i)*interval))
		if tr.mode == adaptiveTimelapse {
			st.TLFrames++
		}
		if tr.mode != prev {
			st.Switches++
			prev = tr.mode
		}
	}
	if st.Frames > 0 {
		st.TLShare = float64(st.TLFrames) / float64(st.Frames)
	}
	st.NoiseFloor = tr.noiseFloor()
	return st
}
