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

import (
	"math"
	"sort"
)

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
	// MedianP is the absolute dimension of the segment: the median P-frame
	// size in bytes. The spike metric is purely RELATIVE (median + factor×
	// dispersion), so its meaning collapses when the encoder starves — a
	// night-mode camera crushing its bitrate to a few hundred bytes per frame
	// measures encoder/rate-control jitter, not activity (field data
	// 2026-08-31: an empty stairwell at ~293 B/frame scored 0.93).
	MedianP float64
	// Confidence discounts Score on absolute-size grounds: 1 = the relative
	// metric is trustworthy, 0 = the segment's frames are too small for a
	// relative spike ratio to mean anything. Consumers that rank or display
	// activity should use Score × Confidence (issue #634).
	Confidence float64
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
	// Gain maps spike fraction onto the score via 1-exp(-ratio*Gain): a
	// smooth-saturating curve (Gain 4 ≈ 0.63 at a 25% spike fraction) that,
	// unlike the pre-2026-09-01 linear ×Gain+clamp, never pins at exactly
	// 1.0 — night-noise and rush-hour stay distinguishable.
	Gain float64
	// SceneCutFactor: a single sample larger than median*SceneCutFactor marks
	// a bitrate discontinuity (scene change / exposure step).
	SceneCutFactor float64
	// AbsMedianFloor is the median P-frame byte size at or below which the
	// relative spike metric is considered meaningless (Confidence 0). Field
	// calibration (issue #634): an empty night stairwell at ~293 B/frame
	// scored 0.93 (pure rate-control jitter), while the same camera at
	// ~1400 B/frame scored a truthful 0.28.
	AbsMedianFloor float64
	// AbsMedianFull is the median P-frame byte size at or above which the
	// metric is fully trusted (Confidence 1). Between Floor and Full the
	// confidence ramps linearly.
	AbsMedianFull float64
	// NotchPeriodic removes encoder-refresh spikes before the ratio: isolated
	// (non-adjacent) spikes continuing a strong periodic lattice are treated
	// as periodic intra-refresh and dropped. Field data 2026-09-01: a smart
	// codec emitted one 3-4x-median "P" frame every 2.0s — the segment's
	// largest frames carried zero motion. Motion bursts (adjacent spike runs)
	// are never notched; genuinely periodic scene change (blinking LED) is —
	// accepted, the score triages non-repetitive change.
	NotchPeriodic bool
}

// DefaultOptions returns the tuned defaults: 3.0 MAD spike factor (robust
// against periodic IDR outliers), 2% static threshold, gain 4. The absolute
// confidence ramp anchors (400–1200 B median P size) are calibrated on the
// same field data as issue #634: below ~400 B/frame a starving encoder's
// jitter dominates any real signal; by ~1200 B/frame real motion spikes are
// an order of magnitude above the noise floor. Encoder-refresh notching is
// on by default (2026-09-01 field data).
func DefaultOptions() Options {
	return Options{
		MinPFrameSamples: 12,
		SpikeFactor:      3.0,
		StaticRatio:      0.02,
		Gain:             4.0,
		SceneCutFactor:   8.0,
		AbsMedianFloor:   400,
		AbsMedianFull:    1200,
		NotchPeriodic:    true,
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
		res.Confidence = 1 // static-by-construction; no relative metric involved
		return res
	}

	med := median(p)
	if med <= 0 {
		res.Flags = []string{"static"}
		res.Confidence = 1
		return res
	}
	res.MedianP = med
	res.Confidence = absConfidence(med, opts.AbsMedianFloor, opts.AbsMedianFull)
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
	spikeIdx := make([]bool, len(p))
	for i, v := range p {
		if v > sceneCutThreshold {
			sceneCut = true
		}
		if v > spikeThreshold {
			spikes++
			spikeIdx[i] = true
		}
	}
	if opts.NotchPeriodic {
		spikes = countTrue(notchPeriodic(spikeIdx))
	}

	ratio := float64(spikes) / float64(len(p))
	// Smooth saturation (2026-09-01): the old ratio×Gain with a hard clamp
	// pinned every segment above 25% spikes at exactly 1.0, destroying
	// resolution exactly where triage needs it (night-noise vs real rush
	// hour both read 0.95+). The exponential map keeps the same ordering and
	// the same "gain" feel (Gain 4 ≈ 0.63 at 25% spikes) but never pins.
	res.Score = 1 - math.Exp(-ratio*opts.Gain)
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

// absConfidence maps the absolute median P-frame size onto [0,1]: 0 at or
// below floor, 1 at or above full, linear between. Non-positive bounds
// disable the discount (always 1) — callers that want the pre-#634 behavior.
func absConfidence(medianP, floor, full float64) float64 {
	if floor <= 0 || full <= floor {
		return 1
	}
	if medianP <= floor {
		return 0
	}
	if medianP >= full {
		return 1
	}
	return (medianP - floor) / (full - floor)
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

// notchPeriodic drops encoder-refresh spikes from a spike indicator (see
// Options.NotchPeriodic). Only ISOLATED spikes (no neighbor within ±1 sample
// — real motion arrives as adjacent runs) participate: the isolated set is
// autocorrelated over integer lags [4, n/3], and when a dominant period
// carries ≥5 continuations and ≥20% of all spikes, every isolated spike that
// follows another at ~that lag (±1 jitter) is dropped. Consecutive motion
// bursts can therefore never be notched, and a lone 4-frame blip stays below
// the adoption bar.
func notchPeriodic(ind []bool) []bool {
	total := countTrue(ind)
	if total < 5 {
		return ind
	}
	// Isolated spikes only.
	var iso []int
	for i, v := range ind {
		if !v {
			continue
		}
		adj := (i > 0 && ind[i-1]) || (i+1 < len(ind) && ind[i+1])
		if !adj {
			iso = append(iso, i)
		}
	}
	if len(iso) < 5 {
		return ind
	}
	maxLag := len(ind) / 3
	bestLag, bestCont := 0, 0
	for lag := 4; lag <= maxLag; lag++ {
		cont := 0
		for _, p := range iso {
			if hasPosNear(iso, p-lag) {
				cont++
			}
		}
		if cont > bestCont {
			bestLag, bestCont = lag, cont
		}
	}
	if bestLag == 0 || bestCont < 5 || bestCont*5 < total {
		return ind
	}
	out := append([]bool(nil), ind...)
	for _, p := range iso {
		if hasPosNear(iso, p-bestLag) {
			out[p] = false
		}
	}
	return out
}

// hasPosNear reports whether sorted ascending positions contain target ±1.
func hasPosNear(sorted []int, target int) bool {
	lo, hi := 0, len(sorted)
	for lo < hi {
		mid := (lo + hi) / 2
		if sorted[mid] < target {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	for i := lo; i < len(sorted) && sorted[i] <= target+1; i++ {
		if sorted[i] >= target-1 {
			return true
		}
	}
	return false
}

func countTrue(ind []bool) int {
	n := 0
	for _, v := range ind {
		if v {
			n++
		}
	}
	return n
}
