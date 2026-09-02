package pixgate

import (
	"testing"
)

func frame(fill byte) []byte { return bytesRepeat(fill, GridW*GridH) }

func bytesRepeat(b byte, n int) []byte {
	f := make([]byte, n)
	for i := range f {
		f[i] = b
	}
	return f
}

// drawBlock paints a bright block into a gray frame.
func drawBlock(f []byte, x0, y0, w, h int, val byte) {
	for y := y0; y < y0+h && y < GridH; y++ {
		for x := x0; x < x0+w && x < GridW; x++ {
			f[y*GridW+x] = val
		}
	}
}

// TestEngine_StaticSceneStaysQuiet: priming + quiet frames never arm.
func TestEngine_StaticSceneStaysQuiet(t *testing.T) {
	e := NewEngine(EngineConfig{MinAreaPct: 1.5})
	e.Process(frame(100)) // prime
	for range 20 {
		r := e.Process(frame(100))
		if r.Active {
			t.Fatalf("static frame armed activity: %+v", r)
		}
	}
}

// TestEngine_RainNoiseStaysQuiet: scattered small speckles (rain-streak
// shape) never reach the minimum blob area even with persistence.
func TestEngine_RainNoiseStaysQuiet(t *testing.T) {
	e := NewEngine(EngineConfig{MinAreaPct: 1.5}) // 1.5% of 19200 = 288 px
	base := frame(100)
	e.Process(base)
	for i := range 30 {
		f := frame(100)
		// ~40 scattered 2×2 speckles = 160 px total, largest blob 4 px.
		for s := range 40 {
			drawBlock(f, (s*13+i*7)%GridW, (s*29)%GridH, 2, 2, 220)
		}
		if r := e.Process(f); r.Active {
			t.Fatalf("rain speckle frame armed activity: %+v", r)
		}
	}
}

// TestEngine_MovingPersonArms: a person-sized blob (3% of grid) held for
// PersistHits samples arms the gate, and releasing it releases after misses.
func TestEngine_MovingPersonArms(t *testing.T) {
	e := NewEngine(EngineConfig{MinAreaPct: 1.5, PersistHits: 2, PersistMisses: 2})
	e.Process(frame(100))
	act := func(i int) EngineResult {
		f := frame(100)
		drawBlock(f, 40+i, 50, 30, 20, 220) // 600 px ≈ 3.1%
		return e.Process(f)
	}
	if r := act(0); r.Active {
		t.Fatal("first active sample must not arm immediately")
	}
	r := act(1)
	if !r.Active {
		t.Fatalf("second consecutive sample must arm: %+v", r)
	}
	if r.BlobAreaPct < 3.0 {
		t.Fatalf("blob area = %.2f%%, want ≥3%%", r.BlobAreaPct)
	}
	if r.CX < 0.3 || r.CX > 0.5 {
		t.Fatalf("centroid cx=%.2f, want ~0.4", r.CX)
	}
	// Quiet frames release after PersistMisses (2): the first miss keeps
	// the gate armed, the second releases it.
	if r := e.Process(frame(100)); !r.Active {
		t.Fatal("must stay active on the first miss (hysteresis)")
	}
	if r := e.Process(frame(100)); r.Active {
		t.Fatal("must release on the second consecutive miss")
	}
	if r := e.Process(frame(100)); r.Active {
		t.Fatal("must stay released")
	}
}

// TestEngine_MaskExcludesRegion: the same person-sized blob inside an
// excluded polygon never arms.
func TestEngine_MaskExcludesRegion(t *testing.T) {
	// Mask the left third of the frame (sky column).
	e := NewEngine(EngineConfig{
		MinAreaPct: 1.5,
		Masks:      []Mask{{{0, 0}, {0.4, 0}, {0.4, 1}, {0, 1}}},
	})
	e.Process(frame(100))
	for range 6 {
		f := frame(100)
		drawBlock(f, 10, 50, 30, 20, 220) // inside mask
		if r := e.Process(f); r.Active || r.BlobAreaPct > 0.1 {
			t.Fatalf("masked blob must not count: %+v", r)
		}
	}
}

// TestEngine_FloodRefreshesBackground: a whole-frame brightness step is
// treated as quiet and re-primes the model.
func TestEngine_FloodRefreshesBackground(t *testing.T) {
	e := NewEngine(EngineConfig{MinAreaPct: 1.5})
	e.Process(frame(100))
	r := e.Process(frame(210)) // global illumination step
	if !r.Flood {
		t.Fatalf("global change must read as flood: %+v", r)
	}
	if r.Active {
		t.Fatal("flood must not arm activity")
	}
	// After the re-prime, quiet frames at the new level stay quiet.
	for range 5 {
		if r := e.Process(frame(210)); r.Active {
			t.Fatalf("post-flood quiet frame armed: %+v", r)
		}
	}
}

// TestEngine_StaticSceneChangeIsAbsorbed — the field bug (M5 2026-09-01): a
// light switched on mid-run (static +60..+120 diff) armed the gate and NEVER
// released, pinning the main stream in full-rate for hours. Root cause: the
// uint8 background accumulator truncated (gray-bg)*alpha < 1 to 0 — a
// deadband of 1/alpha gray levels (20 quiet / 100 foreground) that froze any
// static change above the 28 threshold into an eternal trigger. With the
// float accumulator the model converges and the gate returns to quiet.
func TestEngine_StaticSceneChangeIsAbsorbed(t *testing.T) {
	for _, tc := range []struct {
		name  string
		level byte // block brightness on a 100 background
	}{
		{"bright_light_diff120", 220}, // above the old foreground deadband
		{"dim_light_diff60", 160},     // INSIDE the old deadband [28,100]
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := NewEngine(EngineConfig{MinAreaPct: 1.5}) // no ghost timeout: adaptation alone must converge
			e.Process(frame(100))
			lit := func() []byte {
				f := frame(100)
				drawBlock(f, 110, 10, 30, 20, tc.level) // top-right corner, ~3.1%
				return f
			}
			armed := false
			quietAt := -1
			for i := range 400 {
				r := e.Process(lit())
				if r.Active {
					armed = true
				} else if armed && quietAt < 0 {
					quietAt = i
				}
			}
			if !armed {
				t.Fatal("static scene change must arm briefly before absorption")
			}
			if quietAt < 0 {
				t.Fatalf("static scene change never absorbed after 400 samples (the eternal-ghost bug)")
			}
			// And it stays quiet.
			for range 5 {
				if r := e.Process(lit()); r.Active {
					t.Fatalf("re-armed after absorption at sample %d: %+v", quietAt, r)
				}
			}
		})
	}
}

// TestEngine_LightOffAbsorbed: the darkening direction — uint8(negative
// float) is implementation-defined, the old code could never adapt a light
// switching OFF either. Same convergence expectation.
func TestEngine_LightOffAbsorbed(t *testing.T) {
	e := NewEngine(EngineConfig{MinAreaPct: 1.5})
	base := frame(200)
	e.Process(base)
	dark := func() []byte {
		f := frame(200)
		drawBlock(f, 110, 10, 30, 20, 90) // light off: -110 diff
		return f
	}
	armed := false
	for range 400 {
		r := e.Process(dark())
		if r.Active {
			armed = true
		}
	}
	if !armed {
		t.Fatal("light-off must arm briefly")
	}
	for range 5 {
		if r := e.Process(dark()); r.Active {
			t.Fatal("light-off must be absorbed (darkening adaptation broken)")
		}
	}
}

// TestEngine_BackgroundConvergesExactly: the quiet-pixel deadband is gone —
// a sub-threshold difference (25 < 28) keeps the gate quiet but the model
// must still converge to the new level (old code stalled at ~19-20 residual,
// which would turn eternal if the threshold were ever configured below 20).
func TestEngine_BackgroundConvergesExactly(t *testing.T) {
	e := NewEngine(EngineConfig{MinAreaPct: 1.5})
	e.Process(frame(100))
	for range 100 {
		if r := e.Process(frame(125)); r.Active {
			t.Fatalf("sub-threshold step must stay quiet: %+v", r)
		}
	}
	resid := 0.0
	for i := range e.bg {
		d := float64(125) - float64(e.bg[i])
		if d < 0 {
			d = -d
		}
		if d > resid {
			resid = d
		}
	}
	if resid > 5 {
		t.Fatalf("background residual after 100 quiet samples = %.2f, want <5 (deadband)", resid)
	}
}

// TestEngine_GhostTimeoutAbsorbsStaticBlob: even if adaptation were slow or
// the source flickery, a blob static for GhostSamples consecutive samples is
// force-absorbed (Ghost=true, gate quiet) — the deterministic bound.
func TestEngine_GhostTimeoutAbsorbsStaticBlob(t *testing.T) {
	e := NewEngine(EngineConfig{MinAreaPct: 1.5, GhostSamples: 10})
	e.Process(frame(100))
	lit := func() []byte {
		f := frame(100)
		drawBlock(f, 110, 10, 30, 20, 220)
		return f
	}
	sawGhost := false
	for range 15 {
		r := e.Process(lit())
		if r.Ghost {
			sawGhost = true
			if r.Active {
				t.Fatalf("ghost absorption must return quiet: %+v", r)
			}
		}
	}
	if !sawGhost {
		t.Fatal("static blob must hit the ghost timeout by sample 10")
	}
	for range 5 {
		if r := e.Process(lit()); r.Active {
			t.Fatalf("post-ghost frames must stay quiet: %+v", r)
		}
	}
}

// TestEngine_MovingBlobNeverGhostSuppressed: a blob that keeps moving (6px/
// sample = 0.0375 normalized > the 0.03 stillness radius) never qualifies as
// a ghost, no matter how long the activity lasts.
func TestEngine_MovingBlobNeverGhostSuppressed(t *testing.T) {
	e := NewEngine(EngineConfig{MinAreaPct: 1.5, GhostSamples: 10})
	e.Process(frame(100))
	for i := range 20 {
		f := frame(100)
		drawBlock(f, 10+i*6, 50, 30, 20, 220)
		r := e.Process(f)
		if r.Ghost {
			t.Fatalf("moving blob ghost-suppressed at sample %d", i)
		}
		if i >= 2 && !r.Active {
			t.Fatalf("moving blob must stay active: sample %d: %+v", i, r)
		}
	}
}

// TestEngine_StandingPersonDuration codifies the deliberate trade-off: a
// person who STANDS STILL is kept active for ~1 minute (the foreground
// α/5 learning resists absorption) but is eventually absorbed — a static
// scene is timelapse territory; the 24/7 sub-layer keeps recording them.
func TestEngine_StandingPersonDuration(t *testing.T) {
	e := NewEngine(EngineConfig{MinAreaPct: 1.5})
	e.Process(frame(100))
	person := func() []byte {
		f := frame(100)
		drawBlock(f, 60, 50, 30, 20, 220)
		return f
	}
	for i := range 60 {
		r := e.Process(person())
		if i >= 2 && !r.Active {
			t.Fatalf("standing person absorbed too fast: quiet at sample %d", i)
		}
	}
	for i := 60; i < 400; i++ {
		if r := e.Process(person()); !r.Active {
			return // absorbed — expected within ~3 minutes
		}
	}
	t.Fatal("standing person never absorbed within 400 samples")
}

// noisyFrame returns a frame with deterministic ±amp sensor grain around
// fill — the night-gain-noise repro (2026-09-01: grain lifts |gray-bg| past
// the base threshold on a large pixel fraction).
func noisyFrame(fill byte, salt uint32, amp int) []byte {
	f := make([]byte, GridW*GridH)
	s := salt*2654435761 + 7
	for i := range f {
		s = s*1103515245 + 12345
		f[i] = fill + byte((s>>16)%uint32(2*amp+1)) - byte(amp)
	}
	return f
}

// TestEngine_NoiseTuneSuppressesNightGrain: ±30 grain on every frame would
// read as whole-frame motion against the fixed 28 threshold; the noise-tuned
// threshold rides above the measured median |gray-bg| and the gate stays
// quiet. Real motion (+120 edges) still arms through the capped threshold.
func TestEngine_NoiseTuneSuppressesNightGrain(t *testing.T) {
	e := NewEngine(EngineConfig{MinAreaPct: 1.5, PersistHits: 2})
	e.Process(frame(100))
	for i := range 30 {
		res := e.Process(noisyFrame(100, uint32(i+1), 30))
		if res.Active {
			t.Fatalf("noise-tuned gate armed on pure grain at sample %d", i)
		}
	}

	// Real motion through the tuned threshold: a +120 block dominates the
	// (capped) threshold from any side.
	moving := noisyFrame(100, 99, 30)
	drawBlock(moving, 40, 50, 30, 20, 220)
	armed := false
	for i := range 3 {
		f := noisyFrame(100, uint32(200+i), 30)
		drawBlock(f, 40+i*10, 50, 30, 20, 220)
		if e.Process(f).Active {
			armed = true
			break
		}
	}
	if !armed {
		t.Fatal("real motion must arm through the noise-tuned threshold")
	}
}

// TestEngine_IlluminationStepFloodsNotArms: a mean-luma jump beyond the
// step band (IR-cut flip / exposure step) must read as Flood with the gate
// never arming, and the model must settle onto the new illumination.
func TestEngine_IlluminationStepFloodsNotArms(t *testing.T) {
	e := NewEngine(EngineConfig{MinAreaPct: 1.5, PersistHits: 2})
	e.Process(frame(100))
	e.Process(frame(100))
	for i := range 3 {
		res := e.Process(frame(140))
		if res.Active {
			t.Fatalf("illumination step armed the gate at sample %d", i)
		}
		if !res.Flood {
			t.Fatalf("illumination step must report Flood, sample %d", i)
		}
	}
	// 3 consecutive deviations confirmed the step and re-primed: the very
	// next frame of the new illumination is quiet again.
	for i := range 3 {
		if res := e.Process(frame(140)); res.Active || res.Flood {
			t.Fatalf("post-step frames must be quiet, sample %d: active=%v flood=%v", i, res.Active, res.Flood)
		}
	}
}

// TestEngine_ReprimeReadoptsScene: Reprime (PTZ / suppression path) makes
// the very next frame re-seed the model — a wholesale different scene
// afterwards reads quiet instead of one giant foreground.
func TestEngine_ReprimeReadoptsScene(t *testing.T) {
	e := NewEngine(EngineConfig{MinAreaPct: 1.5, PersistHits: 2})
	e.Process(frame(100))
	e.Process(frame(100))
	e.Reprime()
	e.Process(frame(180)) // prime frame (zero result)
	for i := range 3 {
		if res := e.Process(frame(180)); res.Active || res.Flood {
			t.Fatalf("reprime must readopt the scene, sample %d: active=%v flood=%v", i, res.Active, res.Flood)
		}
	}
}
