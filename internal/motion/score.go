// Package motion implements compressed-domain boring-segment detection
// (issue #435): it scores recorded MP4 segments for visual activity using
// ONLY the per-frame byte-size series from the MP4 sample table — no pixel
// decoding, no AI models, no new dependencies.
//
// Principle: H.264/H.265 are differential codecs. A P-frame encodes only the
// residual against its reference frame, so its byte size is a direct proxy
// for how much changed since the previous frame:
//
//	static scene:  I:30000  P:800  P:750  P:820  ...  (floor-level, ~zero variance)
//	activity:      I:30000  P:800  P:5200 P:9800 ...  (spikes = motion bursts)
//	scene cut:     ... P:900  P:68000 P:12000 ...      (discontinuity)
//
// The per-frame sizes live in the MP4 stsz box, which internal/merge's
// ParseSegment already parses — reading them never touches media data, making
// the score effectively free (microseconds per segment).
//
// Scoring is deliberately a TRIAGE signal, not an alarm: it answers "is this
// segment worth keeping / watching?", never "what moved". Distinguishing a
// person from a swaying tree requires semantic models, which stay in the
// browser (ONNX) and the external Vision service by design.
package motion

import "sort"

// FrameSample is one video sample from an MP4 sample table.
type FrameSample struct {
	Size uint32 // stsz size, bytes
	// IsKeyframe comes from the stss sync-sample table. Files without stss
	// leave it false everywhere; the scorer then treats all samples uniformly
	// (periodic IDR outliers are absorbed by the median baseline).
	IsKeyframe bool
}

// Result is the scored outcome for one segment.
type Result struct {
	// Score is a normalized activity score in [0,1]: 0 = fully static,
	// 1 = heavily active. Segments with too few analyzable frames score 0.
	Score float64
	// Flags carries the activity vocabulary: model.ActivityFlagStatic /
	// Motion / SceneCut — exactly one of static|motion plus optionally
	// scene_cut.
	Flags []string
	// PCount is the number of non-keyframe samples the score is based on.
	PCount int
}

// Options tunes the scorer. Zero value is unusable — use DefaultOptions.
type Options struct {
	// MinPFrameSamples is the minimum analyzable frame count for a confident
	// score. Below it the segment is classified static (a segment with almost
	// no P frames — e.g. sparse adaptive-timelapse output — is uninteresting
	// by construction).
	MinPFrameSamples int
	// SpikeFactor: a sample is a spike when size > median + SpikeFactor*floor.
	SpikeFactor float64
	// StaticRatio: fraction of spike samples below which the segment is
	// classified static.
	StaticRatio float64
	// Gain maps spike fraction to the [0,1] score: score = min(1, ratio*Gain).
	// Gain 4 means a 25% spike fraction already saturates the score.
	Gain float64
	// SceneCutFactor: a single sample larger than median*SceneCutFactor marks
	// a bitrate discontinuity (scene change / exposure step).
	SceneCutFactor float64
}

// DefaultOptions returns the tuned defaults: 3.0 MAD spike factor (robust
// against periodic IDR outliers), 2% static threshold, gain 4.
func DefaultOptions() Options {
	return Options{
		MinPFrameSamples: 12,
		SpikeFactor:      3.0,
		StaticRatio:      0.02,
		Gain:             4.0,
		SceneCutFactor:   8.0,
	}
}

// ScoreSamples scores one segment's sample series.
func ScoreSamples(samples []FrameSample, opts Options) Result {
	p := make([]float64, 0, len(samples))
	for _, s := range samples {
		if !s.IsKeyframe {
			p = append(p, float64(s.Size))
		}
	}
	res := Result{PCount: len(p)}
	if len(p) < opts.MinPFrameSamples {
		res.Flags = []string{"static"}
		return res
	}

	med := median(p)
	if med <= 0 {
		res.Flags = []string{"static"}
		return res
	}
	mad := medianAbsDev(p, med)
	// Relative floor: on ultra-stable streams MAD collapses toward zero and
	// any tiny fluctuation would classify as a spike. Floor the dispersion at
	// 10% of the median so "spike" keeps meaning "notably bigger than usual".
	floor := mad
	if minFloor := med * 0.10; floor < minFloor {
		floor = minFloor
	}

	spikeThreshold := med + opts.SpikeFactor*floor
	sceneCutThreshold := med * opts.SceneCutFactor
	spikes := 0
	sceneCut := false
	for _, v := range p {
		if v > sceneCutThreshold {
			sceneCut = true
		}
		if v > spikeThreshold {
			spikes++
		}
	}

	ratio := float64(spikes) / float64(len(p))
	res.Score = ratio * opts.Gain
	if res.Score > 1 {
		res.Score = 1
	}
	if ratio < opts.StaticRatio {
		res.Flags = []string{"static"}
	} else {
		res.Flags = []string{"motion"}
	}
	if sceneCut {
		res.Flags = append(res.Flags, "scene_cut")
	}
	return res
}

// median returns the median of vals (copies before sorting — caller's slice
// is untouched).
func median(vals []float64) float64 {
	s := append([]float64(nil), vals...)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

// medianAbsDev returns the median absolute deviation from med.
func medianAbsDev(vals []float64, med float64) float64 {
	dev := make([]float64, len(vals))
	for i, v := range vals {
		d := v - med
		if d < 0 {
			d = -d
		}
		dev[i] = d
	}
	return median(dev)
}
