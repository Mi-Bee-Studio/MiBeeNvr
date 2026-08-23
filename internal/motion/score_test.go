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
	// walking through) — well above the 2% static threshold. 10% × gain 4 →
	// score saturates at 0.4.
	s := staticSeries(600)
	for i := 100; i < 160; i += 1 {
		s[i].Size = 4000 // ~5x baseline: motion burst, below scene-cut line
	}
	res := ScoreSamples(s, DefaultOptions())
	if res.Score < 0.35 {
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
	if res.Flags[0] != "motion" || res.Score < 0.35 {
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
