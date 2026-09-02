package motion

import (
	"testing"
)

// staticSeries builds a flat series — a static scene where every P frame
// carries the same tiny residual.
func staticSeries(n int) []FrameSample {
	s := make([]FrameSample, n)
	for i := range s {
		s[i] = FrameSample{Size: 800 + uint32(i%5)*10} // 800..840 jitter
	}
	return s
}

func TestScoreSamples_StaticScene(t *testing.T) {
	res := ScoreSamples(staticSeries(600), DefaultOptions())
	if res.Score != 0 {
		t.Fatalf("static scene should score 0, got %v", res.Score)
	}
	if len(res.Flags) != 1 || res.Flags[0] != "static" {
		t.Fatalf("expected [static], got %v", res.Flags)
	}
	if res.PCount != 600 {
		t.Fatalf("PCount = %d, want 600", res.PCount)
	}
}

func TestScoreSamples_ActiveScene(t *testing.T) {
	// 600 frames at 20fps = 30s; 10% of frames are motion spikes (person
	// walking through) — well above the 2% static threshold. 10% spikes on
	// the smooth-saturation map (1-exp(-0.1*4)) ≈ 0.33.
	s := staticSeries(600)
	for i := 100; i < 160; i += 1 {
		s[i].Size = 4000 // ~5x baseline: motion burst, below scene-cut line
	}
	res := ScoreSamples(s, DefaultOptions())
	if res.Score < 0.30 {
		t.Fatalf("active scene should score high, got %v", res.Score)
	}
	if res.Flags[0] != "motion" {
		t.Fatalf("expected motion flag, got %v", res.Flags)
	}
}

func TestScoreSamples_ShortBurstStillMotion(t *testing.T) {
	// A single 0.5s burst (10 of 600 frames = 1.7%... just below 2%? No:
	// 12 frames = 2%) — use 15 frames to sit clearly above threshold.
	s := staticSeries(600)
	for i := 200; i < 215; i++ {
		s[i].Size = 7000
	}
	res := ScoreSamples(s, DefaultOptions())
	if res.Flags[0] != "motion" {
		t.Fatalf("short burst should classify as motion, got %v (score=%v)", res.Flags, res.Score)
	}
}

func TestScoreSamples_TinyBurstStaysStatic(t *testing.T) {
	// 4 spike frames of 600 = 0.67% < 2% static threshold — sensor noise.
	s := staticSeries(600)
	for i := 300; i < 304; i++ {
		s[i].Size = 5000
	}
	res := ScoreSamples(s, DefaultOptions())
	if res.Flags[0] != "static" {
		t.Fatalf("noise-level burst should stay static, got %v (score=%v)", res.Flags, res.Score)
	}
}

func TestScoreSamples_SceneCut(t *testing.T) {
	// One massive discontinuity in an otherwise calm series.
	s := staticSeries(600)
	s[300].Size = 90000 // > 8x median(≈820)
	res := ScoreSamples(s, DefaultOptions())
	found := false
	for _, f := range res.Flags {
		if f == "scene_cut" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected scene_cut flag, got %v", res.Flags)
	}
}

func TestScoreSamples_TooFewSamplesIsStatic(t *testing.T) {
	// Sparse adaptive-timelapse segment: 1 IDR every 30s → 1-2 samples.
	s := []FrameSample{{Size: 30000}, {Size: 30000}}
	res := ScoreSamples(s, DefaultOptions())
	if res.Score != 0 || res.Flags[0] != "static" {
		t.Fatalf("sparse segment should be static 0, got score=%v flags=%v", res.Score, res.Flags)
	}
}

func TestScoreSamples_KeyframesExcludedFromBaseline(t *testing.T) {
	// Same activity level as ActiveScene, but now 1-in-20 samples are big IDR
	// keyframes. The baseline must ignore them so the P-frame spikes still
	// stand out.
	s := make([]FrameSample, 0, 600)
	for i := range 600 {
		if i%20 == 0 {
			s = append(s, FrameSample{Size: 30000, IsKeyframe: true})
			continue
		}
		sz := uint32(800 + (i%5)*10)
		if i >= 100 && i < 160 {
			sz = 4000
		}
		s = append(s, FrameSample{Size: sz})
	}
	res := ScoreSamples(s, DefaultOptions())
	if res.Flags[0] != "motion" || res.Score < 0.30 {
		t.Fatalf("keyframe-polluted series should still detect motion, got flags=%v score=%v", res.Flags, res.Score)
	}
	found := false
	for _, f := range res.Flags {
		if f == "scene_cut" {
			found = true
		}
	}
	if found {
		t.Fatalf("IDR keyframes must not trigger scene_cut (they're excluded), got %v", res.Flags)
	}
	if res.PCount != 570 {
		t.Fatalf("PCount = %d, want 570 (600-30 keyframes)", res.PCount)
	}
}

func TestScoreSamples_UltraStableStreamNoFalseSpikes(t *testing.T) {
	// Perfectly flat stream: MAD=0 exercises the relative-floor path. Without
	// the floor every +1-byte frame would be an infinite-MAD spike; with it,
	// spikes require >median + 3*10%*median = 1.3x median.
	s := make([]FrameSample, 500)
	for i := range s {
		s[i].Size = 1000
	}
	res := ScoreSamples(s, DefaultOptions())
	if res.Flags[0] != "static" || res.Score != 0 {
		t.Fatalf("ultra-stable stream should be static 0, got flags=%v score=%v", res.Flags, res.Score)
	}
}

func TestScoreSamples_ScoreClampedToOne(t *testing.T) {
	// Every frame a spike → ratio 1.0 → gain 4 → must clamp at 1.
	s := make([]FrameSample, 200)
	base := 800.0
	for i := range s {
		// Alternate around a low median with all frames above threshold.
		s[i].Size = uint32(base + float64(i%2)*2000)
	}
	// median≈1800, floor≈180, threshold≈1800+540=2340 → half the frames spike.
	res := ScoreSamples(s, DefaultOptions())
	if res.Score > 1 {
		t.Fatalf("score must clamp at 1, got %v", res.Score)
	}
}

// TestScoreSamples_LowBitrateConfidenceDiscount reproduces the 2026-08-31
// field finding (issue #634): an empty stairwell camera crushing its bitrate
// to ~293 B/frame measured a 0.93 score — the relative spike ratio saturated
// on rate-control jitter. The score stays (it measures SOMETHING), but the
// absolute-size confidence must zero it out for consumers.
func TestScoreSamples_LowBitrateConfidenceDiscount(t *testing.T) {
	// ~293 B/frame median with jittery sizes — the night stairwell pattern.
	s := make([]FrameSample, 400)
	for i := range s {
		base := 260.0
		jitter := float64(i%7) * 25 // 0..150 bytes jitter
		s[i].Size = uint32(base + jitter)
	}
	res := ScoreSamples(s, DefaultOptions())
	if res.Confidence != 0 {
		t.Fatalf("293B-median segment must have zero confidence, got %v", res.Confidence)
	}
	if res.Score*res.Confidence != 0 {
		t.Fatalf("effective score must be 0, got %v×%v", res.Score, res.Confidence)
	}
}

// TestScoreSamples_HealthyBitrateFullConfidence: a 2K stream at 2 Mbps /
// 20 fps carries ~12.5 KB per P-frame — well above the trust anchor.
func TestScoreSamples_HealthyBitrateFullConfidence(t *testing.T) {
	s := make([]FrameSample, 300)
	for i := range s {
		s[i].Size = uint32(12500 + i%13*300)
	}
	res := ScoreSamples(s, DefaultOptions())
	if res.Confidence != 1 {
		t.Fatalf("12.5KB-median segment must have full confidence, got %v", res.Confidence)
	}
}

// TestScoreSamples_ConfidenceRampMidpoint: 800 B median sits exactly halfway
// between the 400/1200 anchors.
func TestScoreSamples_ConfidenceRampMidpoint(t *testing.T) {
	s := make([]FrameSample, 200)
	for i := range s {
		s[i].Size = uint32(800 + i%5*10)
	}
	res := ScoreSamples(s, DefaultOptions())
	if res.Confidence < 0.47 || res.Confidence > 0.53 {
		t.Fatalf("800B-median confidence should be ~0.5, got %v", res.Confidence)
	}
}

// TestScoreSamples_ConfidenceDisabled: zero bounds keep the pre-#634
// behavior (full trust) for callers that opt out.
func TestScoreSamples_ConfidenceDisabled(t *testing.T) {
	s := make([]FrameSample, 100)
	for i := range s {
		s[i].Size = uint32(300 + i%3*40)
	}
	opts := DefaultOptions()
	opts.AbsMedianFloor = 0
	opts.AbsMedianFull = 0
	res := ScoreSamples(s, opts)
	if res.Confidence != 1 {
		t.Fatalf("disabled bounds must yield confidence 1, got %v", res.Confidence)
	}
}

// TestScoreSamples_TooFewSamplesFullConfidence: the below-sample-floor
// "static by construction" branch must not be discounted.
func TestScoreSamples_TooFewSamplesFullConfidence(t *testing.T) {
	s := []FrameSample{{Size: 100}, {Size: 100}, {Size: 100}}
	res := ScoreSamples(s, DefaultOptions())
	if res.Confidence != 1 || res.Flags[0] != "static" {
		t.Fatalf("few-sample segment: got confidence=%v flags=%v", res.Confidence, res.Flags)
	}
}

// TestScoreSamples_SmoothSaturationNeverPins: the 2026-09-01 exponential
// map must never return exactly 1.0 and must stay strictly monotonic — the
// old linear×Gain+clamp pinned everything above 25% spikes at 1.0, which is
// exactly where night-noise vs rush-hour needed resolution.
func TestScoreSamples_SmoothSaturationNeverPins(t *testing.T) {
	mk := func(burst int) []FrameSample {
		// Contiguous bursts (not lattices — the notch would flatten those).
		s := staticSeries(600)
		for i := 100; i < 100+burst; i++ {
			s[i].Size = 4000
		}
		return s
	}
	prev := -1.0
	// Burst must stay under half the series or the median itself moves
	// (inherent to any relative metric).
	for _, burst := range []int{15, 60, 150, 250} {
		res := ScoreSamples(mk(burst), DefaultOptions())
		if res.Score >= 1 {
			t.Fatalf("burst=%d: score must stay below 1, got %v", burst, res.Score)
		}
		if res.Score <= prev {
			t.Fatalf("monotonicity broken at burst=%d: %v after %v", burst, res.Score, prev)
		}
		prev = res.Score
	}
}

// TestScoreSamples_PeriodicRefreshNotched reproduces the 2026-09-01 field
// finding: a smart codec emits one 3-4x-median "P" frame every 2.0s (every
// 20th sample at 10fps) with zero motion in the scene. The lattice is
// isolated single frames — notched away, the segment reads static.
func TestScoreSamples_PeriodicRefreshNotched(t *testing.T) {
	s := staticSeries(600)
	for i := 5; i < len(s); i += 20 {
		s[i].Size = 4200 // isolated 5x frames on a 20-sample lattice
	}
	res := ScoreSamples(s, DefaultOptions())
	if res.Flags[0] != "static" || res.Score > 0.01 {
		// The lattice's FIRST frame has no predecessor to prove periodicity,
		// so one spike legitimately survives the notch.
		t.Fatalf("pure refresh lattice should read static ~0, got flags=%v score=%v", res.Flags, res.Score)
	}

	// Opt-out keeps the raw behavior for comparison.
	opts := DefaultOptions()
	opts.NotchPeriodic = false
	raw := ScoreSamples(s, opts)
	if raw.Flags[0] != "motion" {
		t.Fatalf("without notch the lattice must classify as motion, got %v", raw.Flags)
	}
}

// TestScoreSamples_MotionBurstNotNotched: adjacent spike runs are real
// motion and must never be removed by the periodic notch, no matter how
// regular the burst spacing looks under autocorrelation.
func TestScoreSamples_MotionBurstNotNotched(t *testing.T) {
	s := staticSeries(600)
	for i := 100; i < 130; i++ { // one contiguous 30-frame run
		s[i].Size = 4200
	}
	res := ScoreSamples(s, DefaultOptions())
	if res.Flags[0] != "motion" || res.Score < 0.15 {
		t.Fatalf("adjacent motion burst must survive notching, got flags=%v score=%v", res.Flags, res.Score)
	}
}
