// Package pixgate — pixel-domain fine gating (#636).
//
// The compressed-domain activity signal cannot separate "rain / water glare
// / swaying foliage" from "person" (both are real pixel motion — issue #435
// field data). This package adds the industry-standard middle layer
// (Blue Iris / ZoneMinder / Milestone dynamic-sensitivity class): a low-rate
// sampled DECODE of the camera's SUB-stream feeding a classic-CV gate —
// background model + threshold + connected components + minimum blob area +
// ROI masks + temporal persistence. Rain reads as small scattered short-lived
// blobs; a person reads as one large persistent blob. No AI, no Vision
// dependency, and the decode cost is bounded by design: the sub-stream is
// 480p-class H.264 and we sample ~1 frame per second (~10-20ms software
// decode, <2% of one core per camera).
//
// The zero-decode principle is deliberately breached here in the narrowest
// possible way: ffmpeg (already an optional dependency for transcoding)
// scales the sub-stream to a fixed 160×120 gray grid before Go ever sees a
// pixel, and only cameras with pixgate explicitly enabled pay the cost.

package pixgate

import "sort"

// GridW/GridH is the fixed analysis grid ffmpeg scales every sample to.
// 160×120 keeps the pure-Go CV at ~19k pixels/frame (sub-millisecond) and
// makes masks resolution-independent (normalized coordinates).
const (
	GridW = 160
	GridH = 120
)

// Mask is one exclusion polygon in normalized [0,1] coordinates (≥3 points).
// Pixels inside any mask are removed from the foreground before blob
// analysis — the cheap answer to "the sky/street/puddle always shimmers".
type Mask [][2]float64

// EngineConfig tunes the CV gate. Zero values become defaults.
type EngineConfig struct {
	// MinAreaPct is the largest-blob area (as % of the grid) that counts as
	// activity. Default 1.5 — a person at mid-range on a 480p sub-stream
	// covers ~2-8%; rain streaks and compression shimmer stay well under.
	MinAreaPct float64
	// Threshold is the per-pixel difference threshold (0-255). Default 28.
	Threshold uint8
	// PersistHits is the consecutive active samples required to arm (default
	// 2 ≈ 2s at 1fps); PersistMisses the consecutive quiet samples to release
	// (default 3).
	PersistHits   int
	PersistMisses int
	// Alpha is the background learning rate per sample (default 0.05);
	// foreground pixels learn at Alpha/5 so a standing person is not
	// absorbed into the background for the first few minutes.
	Alpha float64
	// GhostSamples suppresses a STATIC foreground blob that persists this
	// many consecutive samples (centroid within ±0.03 normalized and area
	// within [0.6, 1.67]× of the streak's opening blob): the model is
	// re-primed from the current frame, so a switched-on light, a parked
	// car, or a lens water drop stops triggering instead of firing forever.
	// A moving object never qualifies (its centroid drifts). 0 disables;
	// the manager derives it from ghost_secs / sample rate (default 300s).
	GhostSamples int
	// NoiseFactor scales the measured sensor-noise median into the effective
	// per-pixel threshold (noise-tune, motion-project's technique): while the
	// gate is quiet the median |gray-bg| is tracked and the threshold rides
	// at max(Threshold, NoiseFactor×noise), capped by ThresholdMax. Night
	// gain grain raises the floor so shimmer stops reading as foreground;
	// real motion edges stay orders above grain. Default 4 (≈ >99.9% of
	// Gaussian noise). Zero → default.
	NoiseFactor float64
	// ThresholdMax caps the noise-tuned threshold (default 56): beyond it,
	// genuine mid-contrast motion would start leaking through.
	ThresholdMax uint8
	// Masks are exclusion polygons (normalized coordinates).
	Masks []Mask
}

func (c *EngineConfig) normalize() {
	if c.MinAreaPct <= 0 {
		c.MinAreaPct = 1.5
	}
	if c.Threshold == 0 {
		c.Threshold = 28
	}
	if c.PersistHits <= 0 {
		c.PersistHits = 2
	}
	if c.PersistMisses <= 0 {
		c.PersistMisses = 3
	}
	if c.Alpha <= 0 || c.Alpha > 1 {
		c.Alpha = 0.05
	}
	if c.NoiseFactor <= 0 {
		c.NoiseFactor = 4
	}
	if c.ThresholdMax == 0 {
		c.ThresholdMax = 56
	}
}

// illumStep is the mean-luma deviation (from its slow EMA) beyond which a
// frame is treated as a global illumination event (IR-cut flip, exposure /
// gain step). Three consecutive deviations confirm a persistent step and
// re-prime the model; singles just ride through suppressed — a headlight
// sweep must not arm the gate.
const illumStep = 12.0

// EngineResult reports one sample's outcome.
type EngineResult struct {
	// Active is the persistence-filtered activity verdict.
	Active bool
	// BlobAreaPct is the current largest-blob area as % of the grid
	// (pre-persistence — diagnostics and tuning).
	BlobAreaPct float64
	// Flood marks a whole-frame change (lighting step / IR cut flip /
	// encoder reset): the frame is treated as quiet and the background model
	// is re-primed, so global changes never read as activity.
	Flood bool
	// Ghost marks a static-foreground absorption: the largest blob had not
	// moved for GhostSamples consecutive samples, so the model was re-primed
	// from this frame and the gate returns quiet. Observability signal —
	// the difference between "activity ended" and "scene changed".
	Ghost bool
	// CX/CY is the largest blob's centroid in normalized coordinates.
	CX, CY float64
}

// Engine is the stateful CV gate for one camera. Not goroutine-safe — one
// Engine per sampler goroutine.
type Engine struct {
	cfg    EngineConfig
	bg     []float32
	fg     []bool
	mask   []bool
	primed bool
	hits   int
	misses int
	active bool

	// Ghost-streak state: consecutive samples whose largest blob stayed put
	// (see EngineConfig.GhostSamples).
	ghostStreak int
	gCX, gCY    float64
	gAreaPct    float64

	// noise-tune state (EngineConfig.NoiseFactor): EMA of the median
	// |gray-bg| over quiet samples; diffSamples is a reusable scratch buffer.
	noise      float32
	diffSample []float32

	// illumination-step state: slow EMA of frame mean luma + consecutive
	// deviation counter (illumStep).
	meanEMA float32
	meanDev int
}

// NewEngine builds an engine; cfg zero values are defaulted.
func NewEngine(cfg EngineConfig) *Engine {
	cfg.normalize()
	e := &Engine{
		cfg:        cfg,
		bg:         make([]float32, GridW*GridH),
		fg:         make([]bool, GridW*GridH),
		mask:       buildMaskBitmap(cfg.Masks),
		diffSample: make([]float32, 0, GridW*GridH/4+1),
	}
	return e
}

// Reprime forces the background model to re-learn from the next frame and
// drops all persistence state — used after PTZ moves and external
// suppression windows (the scene legitimately changed).
func (e *Engine) Reprime() {
	e.primed = false
	e.hits, e.misses, e.active = 0, 0, false
	e.ghostStreak, e.meanDev = 0, 0
	e.noise = 0
}

// Process ingests one gray frame (GridW×GridH bytes) and returns the verdict.
func (e *Engine) Process(gray []byte) EngineResult {
	if len(gray) < GridW*GridH {
		return EngineResult{}
	}
	mean := frameMean(gray)
	if !e.primed {
		primeBackground(e.bg, gray)
		e.primed = true
		e.meanEMA = mean
		return EngineResult{}
	}

	// 0) Illumination-step guard: a frame whose mean luma deviates far from
	// its slow EMA is a global illumination event (IR-cut flip, exposure /
	// gain step), not activity. Transients ride through suppressed; three
	// consecutive deviations confirm a persistent step and re-prime.
	if dev := float64(mean - e.meanEMA); dev > illumStep || dev < -illumStep {
		e.meanDev++
		if e.meanDev >= 3 {
			primeBackground(e.bg, gray)
			e.meanEMA = mean
			e.meanDev = 0
			e.ghostStreak = 0
			e.hits, e.misses, e.active = 0, 0, false
			e.noise = 0
			return EngineResult{Flood: true}
		}
		return EngineResult{Flood: true}
	}
	e.meanDev = 0
	e.meanEMA += (mean - e.meanEMA) * 0.1

	// 1) Difference + threshold (masks zeroed out). The threshold rides on
	// the measured noise floor (noise-tune): night gain grain lifts it so
	// shimmer stops reading as foreground.
	fgCount := 0
	th := e.cfg.Threshold
	if n := float32(e.cfg.NoiseFactor) * e.noise; n > float32(th) {
		if n > float32(e.cfg.ThresholdMax) {
			n = float32(e.cfg.ThresholdMax)
		}
		th = uint8(n)
	}
	e.diffSample = e.diffSample[:0]
	for i := 0; i < GridW*GridH; i++ {
		d := float32(gray[i]) - e.bg[i]
		if d < 0 {
			d = -d
		}
		if i&3 == 0 {
			e.diffSample = append(e.diffSample, d)
		}
		if d > float32(th) && !e.mask[i] {
			e.fg[i] = true
			fgCount++
		} else {
			e.fg[i] = false
		}
	}

	// 2) Flood guard: a global change is illumination, not activity.
	if fgCount > GridW*GridH*65/100 {
		primeBackground(e.bg, gray)
		e.ghostStreak = 0
		e.meanEMA, e.meanDev, e.noise = mean, 0, 0
		return EngineResult{Flood: true}
	}

	// 3) Connected components (row-run union) → largest blob.
	area, cx, cy := largestBlob(e.fg)
	areaPct := float64(area) / float64(GridW*GridH) * 100

	// 3b) Ghost suppression: a blob that has not moved for GhostSamples
	// consecutive samples is a scene change, not activity — absorb it.
	if e.cfg.GhostSamples > 0 && area > 0 {
		if e.ghostStreak > 0 &&
			absF(cx-e.gCX) <= 0.03 && absF(cy-e.gCY) <= 0.03 &&
			areaPct >= e.gAreaPct*0.6 && areaPct <= e.gAreaPct*1.667 {
			e.ghostStreak++
		} else {
			e.ghostStreak = 1
			e.gCX, e.gCY, e.gAreaPct = cx, cy, areaPct
		}
		if e.ghostStreak >= e.cfg.GhostSamples {
			primeBackground(e.bg, gray)
			e.ghostStreak = 0
			e.hits, e.misses, e.active = 0, 0, false
			return EngineResult{Ghost: true, BlobAreaPct: areaPct, CX: cx, CY: cy}
		}
	} else {
		e.ghostStreak = 0
	}

	// 4) Background adaptation: learn fast from quiet pixels, slowly from
	// foreground (a standing person must not become background within the
	// first minutes). The accumulator is float — an integer bg would only
	// step when (gray-bg)*alpha ≥ 1, a deadband of 1/alpha = 20 (quiet) to
	// 100 (foreground) gray levels that permanently freezes any static
	// change above the 28 threshold into an eternal trigger.
	bgAlpha := float32(e.cfg.Alpha)
	fgAlpha := bgAlpha / 5
	for i := 0; i < GridW*GridH; i++ {
		a := bgAlpha
		if e.fg[i] {
			a = fgAlpha
		}
		e.bg[i] += (float32(gray[i]) - e.bg[i]) * a
	}

	// 5) Persistence hysteresis.
	sampleActive := areaPct >= e.cfg.MinAreaPct
	switch {
	case sampleActive:
		e.hits++
		e.misses = 0
	case !sampleActive:
		e.misses++
		e.hits = 0
	}
	if !e.active && e.hits >= e.cfg.PersistHits {
		e.active = true
	}
	if e.active && e.misses >= e.cfg.PersistMisses {
		e.active = false
	}

	// 6) Noise-tune: while the gate is quiet, fold the sampled |gray-bg|
	// median into the noise EMA. Motion frames never touch it (a moving
	// object would inflate the "noise" floor and mask itself).
	if !sampleActive && fgCount < GridW*GridH/20 {
		m := medianF32(e.diffSample)
		e.noise += (m - e.noise) * 0.1
	}

	return EngineResult{
		Active:      e.active,
		BlobAreaPct: areaPct,
		CX:          cx,
		CY:          cy,
	}
}

// frameMean returns the mean luma of a grid frame.
func frameMean(gray []byte) float32 {
	var sum float64
	for _, v := range gray[:GridW*GridH] {
		sum += float64(v)
	}
	return float32(sum / float64(GridW*GridH))
}

// medianF32 sorts vals in place and returns the median (small scratch use).
func medianF32(vals []float32) float32 {
	if len(vals) == 0 {
		return 0
	}
	sl := make([]float64, len(vals))
	for i, v := range vals {
		sl[i] = float64(v)
	}
	sort.Float64s(sl)
	n := len(sl)
	if n%2 == 1 {
		return float32(sl[n/2])
	}
	return float32((sl[n/2-1] + sl[n/2]) / 2)
}

// primeBackground resets the model to the current frame.
func primeBackground(bg []float32, gray []byte) {
	for i := 0; i < len(bg); i++ {
		bg[i] = float32(gray[i])
	}
}

func absF(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// buildMaskBitmap rasterizes the exclusion polygons once (even-odd rule).
func buildMaskBitmap(masks []Mask) []bool {
	m := make([]bool, GridW*GridH)
	if len(masks) == 0 {
		return m
	}
	for y := 0; y < GridH; y++ {
		ny := (float64(y) + 0.5) / GridH
		for x := 0; x < GridW; x++ {
			nx := (float64(x) + 0.5) / GridW
			for _, poly := range masks {
				if pointInPolygon(nx, ny, poly) {
					m[y*GridW+x] = true
					break
				}
			}
		}
	}
	return m
}

// pointInPolygon: even-odd ray casting.
func pointInPolygon(x, y float64, poly Mask) bool {
	inside := false
	n := len(poly)
	j := n - 1
	for i := 0; i < n; i++ {
		xi, yi := poly[i][0], poly[i][1]
		xj, yj := poly[j][0], poly[j][1]
		if (yi > y) != (yj > y) && x < (xj-xi)*(y-yi)/(yj-yi)+xi {
			inside = !inside
		}
		j = i
	}
	return inside
}

// largestBlob finds the biggest 4-connected component of fg and returns its
// pixel area plus normalized centroid. Row-run union: O(foreground).
func largestBlob(fg []bool) (area int, cx, cy float64) {
	type run struct {
		x0, x1, y int // x range inclusive
		parent    int // union-find index
	}
	var runs []run
	var curRow, prevRow []int // run indices of the current / previous row

	find := func(i int) int {
		for runs[i].parent != i {
			runs[i].parent = runs[runs[i].parent].parent
			i = runs[i].parent
		}
		return i
	}
	union := func(a, b int) {
		ra, rb := find(a), find(b)
		if ra != rb {
			runs[rb].parent = ra
		}
	}

	for y := 0; y < GridH; y++ {
		curRow = curRow[:0]
		x := 0
		for x < GridW {
			if !fg[y*GridW+x] {
				x++
				continue
			}
			x0 := x
			for x < GridW && fg[y*GridW+x] {
				x++
			}
			runs = append(runs, run{x0: x0, x1: x - 1, y: y, parent: len(runs)})
			idx := len(runs) - 1
			curRow = append(curRow, idx)
			for _, pi := range prevRow {
				if runs[pi].x1 >= x0 && runs[pi].x0 <= x-1 {
					union(idx, pi)
				}
			}
		}
		prevRow = append(prevRow[:0], curRow...)
	}

	rootArea := make(map[int]int, len(runs))
	rootSX := make(map[int]float64, len(runs))
	rootSY := make(map[int]float64, len(runs))
	for i := range runs {
		r := find(i)
		n := runs[i].x1 - runs[i].x0 + 1
		rootArea[r] += n
		rootSX[r] += float64(runs[i].x0+runs[i].x1+1) / 2 * float64(n)
		rootSY[r] += float64(runs[i].y) * float64(n)
	}
	best, bestA := -1, 0
	for r, a := range rootArea {
		if a > bestA {
			bestA, best = a, r
		}
	}
	if best < 0 {
		return 0, 0, 0
	}
	return bestA, rootSX[best] / float64(bestA) / GridW, rootSY[best] / float64(bestA) / GridH
}
