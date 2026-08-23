package recorder

import (
	"bytes"
	"io"
	"log/slog"
	"testing"
	"time"
)

func testAdaptiveLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// feedCalm simulates `n` P-frames of a static scene at 20fps starting at t0.
func feedCalm(t *testing.T, tr *adaptiveTracker, t0 time.Time, n int) time.Time {
	t.Helper()
	for i := range n {
		now := t0.Add(time.Duration(i) * 50 * time.Millisecond)
		buf := make([]byte, 800)
		buf[0] = 0x41 // non-IDR
		tr.observe(buf, false, now)
	}
	return t0.Add(time.Duration(n) * 50 * time.Millisecond)
}

func TestAdaptiveTracker_EntersTimelapseAfterCalm(t *testing.T) {
	cfg := AdaptiveConfig{CalmThreshold: 60 * time.Second, TimelapseInterval: 30 * time.Second, SpikeFactor: 3.0, MaxGOPBuffer: 16 << 20}
	tr := newAdaptiveTracker(cfg, "cam", testAdaptiveLogger())

	// 30s of calm — below the 60s threshold: must stay NORMAL.
	t0 := time.Now()
	end := feedCalm(t, tr, t0, 600) // 600 frames @ 20fps = 30s
	if tr.mode != adaptiveNormal {
		t.Fatalf("after 30s calm (< 60s threshold) mode = %v, want normal", tr.mode)
	}

	// Another 35s of calm — crosses 60s: must switch to TIMELAPSE.
	feedCalm(t, tr, end, 700)
	if tr.mode != adaptiveTimelapse {
		t.Fatalf("after 65s calm mode = %v, want timelapse", tr.mode)
	}
}

func TestAdaptiveTracker_SpikeReturnsFlushAndResumesNormal(t *testing.T) {
	cfg := AdaptiveConfig{CalmThreshold: 10 * time.Second, TimelapseInterval: 30 * time.Second, SpikeFactor: 3.0, MaxGOPBuffer: 16 << 20}
	tr := newAdaptiveTracker(cfg, "cam", testAdaptiveLogger())

	// Calm long enough to enter TIMELAPSE, with a GOP started by an IDR so
	// the flush has a complete reference chain.
	t0 := time.Now()
	idr := bytes.Repeat([]byte{0x65}, 30000) // IDR NAL, 30KB
	tr.observe(idr, true, t0)
	end := feedCalm(t, tr, t0.Add(50*time.Millisecond), 500) // 25s calm > 10s
	if tr.mode != adaptiveTimelapse {
		t.Fatalf("mode = %v, want timelapse after sustained calm", tr.mode)
	}
	if n := len(tr.gop); n != 501 { // IDR + 500 P frames retained
		t.Fatalf("gop ring = %d frames, want 501", n)
	}

	// A few more sparse-mode frames get retained, then a spike.
	mid := end.Add(100 * time.Millisecond)
	for i := range 5 {
		buf := make([]byte, 800)
		buf[0] = 0x41
		tr.observe(buf, false, mid.Add(time.Duration(i)*50*time.Millisecond))
	}
	spike := bytes.Repeat([]byte{0x41}, 40000) // ~50x baseline → clear spike
	gotSpike, flush := tr.observe(spike, false, mid.Add(300*time.Millisecond))
	if !gotSpike {
		t.Fatal("40KB P-frame vs 800B baseline must classify as spike")
	}
	if tr.mode != adaptiveNormal {
		t.Fatalf("mode = %v after spike, want normal", tr.mode)
	}
	if len(flush) == 0 || !flush[0].isIDR {
		t.Fatal("flush must start with the retained IDR")
	}
	if len(flush) != 507 { // IDR + 500 calm + 5 sparse + the spike frame itself (appended before takeGOP)
		t.Fatalf("flush = %d frames, want 507", len(flush))
	}
	if len(tr.gop) != 0 {
		t.Fatalf("gop ring must be detached after flush, has %d", len(tr.gop))
	}
}

func TestAdaptiveTracker_IDRResetsGOPRing(t *testing.T) {
	cfg := DefaultAdaptiveConfig()
	tr := newAdaptiveTracker(cfg, "cam", testAdaptiveLogger())
	t0 := time.Now()

	idr1 := bytes.Repeat([]byte{0x65}, 20000)
	tr.observe(idr1, true, t0)
	for i := range 10 {
		buf := make([]byte, 800)
		buf[0] = 0x41
		tr.observe(buf, false, t0.Add(time.Duration(i+1)*50*time.Millisecond))
	}
	if len(tr.gop) != 11 {
		t.Fatalf("gop = %d, want 11", len(tr.gop))
	}

	// New IDR: the partial chain is superseded and dropped.
	idr2 := bytes.Repeat([]byte{0x65}, 20000)
	tr.observe(idr2, true, t0.Add(600*time.Millisecond))
	if len(tr.gop) != 1 {
		t.Fatalf("gop after new IDR = %d, want 1", len(tr.gop))
	}
	if !tr.gop[0].isIDR {
		t.Fatal("ring must start with the new IDR")
	}
}

func TestAdaptiveTracker_BufferOverflowDegradesGracefully(t *testing.T) {
	cfg := AdaptiveConfig{CalmThreshold: time.Second, TimelapseInterval: 30 * time.Second, SpikeFactor: 3.0, MaxGOPBuffer: 4096}
	tr := newAdaptiveTracker(cfg, "cam", testAdaptiveLogger())
	t0 := time.Now()

	tr.observe(bytes.Repeat([]byte{0x65}, 3000), true, t0)
	// 2KB frames overflow the 4KB cap immediately.
	for i := range 5 {
		buf := make([]byte, 2000)
		buf[0] = 0x41
		tr.observe(buf, false, t0.Add(time.Duration(i+1)*50*time.Millisecond))
	}
	if !tr.gopBroken {
		t.Fatal("ring must be marked broken after overflow")
	}
	// Spike still resumes NORMAL mode, but with no flush (nil).
	spike := bytes.Repeat([]byte{0x41}, 60000)
	_, flush := tr.observe(spike, false, t0.Add(500*time.Millisecond))
	if tr.mode != adaptiveNormal {
		t.Fatalf("mode = %v, want normal (degraded resume)", tr.mode)
	}
	if flush != nil {
		t.Fatalf("broken ring must not flush, got %d frames", len(flush))
	}
}

func TestAdaptiveTracker_ShouldWriteSparseCadence(t *testing.T) {
	cfg := AdaptiveConfig{CalmThreshold: time.Second, TimelapseInterval: 30 * time.Second, SpikeFactor: 3.0, MaxGOPBuffer: 1 << 20}
	tr := newAdaptiveTracker(cfg, "cam", testAdaptiveLogger())
	now := time.Now()
	tr.lastSparseWrite = now

	if tr.shouldWriteSparse(true, now.Add(29*time.Second)) {
		t.Fatal("29s < 30s interval: keyframe must be skipped")
	}
	if !tr.shouldWriteSparse(true, now.Add(31*time.Second)) {
		t.Fatal("31s >= 30s interval: keyframe must be written")
	}
	if tr.shouldWriteSparse(false, now.Add(60*time.Second)) {
		t.Fatal("P-frames are never written in sparse mode")
	}
}

// TestAdaptiveTracker_SparseWriteStampsCadence pins the regression found on
// the Docker VM field test: when the writeFrames gate lets a sparse keyframe
// through it must stamp lastSparseWrite (mirrored here). Without the stamp
// every IDR in every GOP passes shouldWriteSparse and sparse mode degenerates
// to one keyframe per GOP.
func TestAdaptiveTracker_SparseWriteStampsCadence(t *testing.T) {
	cfg := AdaptiveConfig{CalmThreshold: time.Second, TimelapseInterval: 10 * time.Second, SpikeFactor: 3.0, MaxGOPBuffer: 1 << 20}
	tr := newAdaptiveTracker(cfg, "cam", testAdaptiveLogger())
	t0 := time.Now()
	tr.lastSparseWrite = t0

	// Keyframes at 2s GOP cadence (10 in a 20s window): with cadence stamping
	// exactly the ones at t0+10s and t0+20s may pass (one per 10s interval).
	// Without stamping all 10 would pass — that was the field-test bug.
	passed := 0
	for i := 1; i <= 10; i++ {
		at := t0.Add(time.Duration(i*2) * time.Second)
		if tr.shouldWriteSparse(true, at) {
			passed++
			tr.lastSparseWrite = at // the writeFrames gate does exactly this
		}
	}
	if passed != 2 {
		t.Fatalf("with cadence stamping, exactly 2 keyframes per 20s at 10s interval may pass, got %d", passed)
	}
}

func TestAdaptiveTracker_BaselineRingBounded(t *testing.T) {
	cfg := DefaultAdaptiveConfig()
	tr := newAdaptiveTracker(cfg, "cam", testAdaptiveLogger())
	t0 := time.Now()
	// Feed far more than adaptivePWindow frames — the ring must halve, not grow.
	for i := range adaptivePWindow + 500 {
		buf := make([]byte, 800+(i%5)*10)
		buf[0] = 0x41
		tr.observe(buf, false, t0.Add(time.Duration(i)*50*time.Millisecond))
	}
	if len(tr.pSizes) > adaptivePWindow {
		t.Fatalf("baseline ring grew to %d, cap %d", len(tr.pSizes), adaptivePWindow)
	}
}

// feedNoisyCalm simulates a static scene whose encoder still emits isolated
// oversized P-frames (issue #466): every gapSeconds a single spike frame is
// injected between small frames. Isolated spikes must NOT reset the calm
// accumulation, so TIMELAPSE is still entered once CalmThreshold elapses.
func feedNoisyCalm(t *testing.T, tr *adaptiveTracker, t0 time.Time, seconds int, gapSeconds time.Duration, spikeFactor float64) time.Time {
	t.Helper()
	step := 50 * time.Millisecond
	end := t0
	for elapsed := time.Duration(0); elapsed < time.Duration(seconds)*time.Second; elapsed += gapSeconds {
		// small frames up to the spike point
		for d := time.Duration(0); d < gapSeconds; d += step {
			buf := make([]byte, 800)
			buf[0] = 0x41
			tr.observe(buf, false, end.Add(d))
		}
		end = end.Add(gapSeconds)
		// one isolated spike well above the classify threshold
		big := make([]byte, int(800*(spikeFactor+2)))
		big[0] = 0x41
		tr.observe(big, false, end)
		end = end.Add(step)
	}
	return end
}

func TestAdaptiveTracker_IsolatedSpikesDoNotResetCalm(t *testing.T) {
	cfg := DefaultAdaptiveConfig() // SpikeFactor 5.0, CalmThreshold 60s
	tr := newAdaptiveTracker(cfg, "cam", testAdaptiveLogger())

	t0 := time.Now()
	// Baseline must exist before classify produces spikes: warm up calmly.
	end := feedCalm(t, tr, t0, 100) // 5s of small frames
	// 50s with an isolated spike every 10s (last at ~55s). With the OLD
	// single-spike rule the calm timer reset on every spike, pushing TIMELAPSE
	// past ~115s; now the isolated spikes are ignored and calm accumulates
	// across them. The window stays under the 60s threshold so entry happens
	// only in the trailing calm stretch below.
	end = feedNoisyCalm(t, tr, end, 50, 10*time.Second, cfg.SpikeFactor)
	// Pure calm crosses the 60s mark and enters TIMELAPSE; with no spike after
	// entry the mode holds (single-spike exit is exercised by its own test).
	feedCalm(t, tr, end, 400) // 20s
	if tr.mode != adaptiveTimelapse {
		t.Fatalf("mode = %v, want timelapse — isolated noise spikes must not block entry", tr.mode)
	}
}

func TestAdaptiveTracker_SpikeBurstResetsCalm(t *testing.T) {
	cfg := DefaultAdaptiveConfig()
	tr := newAdaptiveTracker(cfg, "cam", testAdaptiveLogger())

	t0 := time.Now()
	end := feedCalm(t, tr, t0, 100) // warm up baseline
	// Repeated bursts (2 spikes 100ms apart) every 10s for 80s — each burst
	// resets calmSince, so the 60s threshold is never reached.
	for elapsed := time.Duration(0); elapsed < 80*time.Second; elapsed += 10 * time.Second {
		at := end.Add(elapsed)
		for d := time.Duration(0); d < 10*time.Second; d += 50 * time.Millisecond {
			buf := make([]byte, 800)
			buf[0] = 0x41
			tr.observe(buf, false, at.Add(d))
		}
		for i := range 2 { // clustered spikes = motion burst
			big := make([]byte, int(800*(cfg.SpikeFactor+2)))
			big[0] = 0x41
			tr.observe(big, false, at.Add(10*time.Second+time.Duration(i)*100*time.Millisecond))
		}
	}
	if tr.mode != adaptiveNormal {
		t.Fatalf("mode = %v, want normal — motion bursts must keep resetting calm", tr.mode)
	}
}

func TestAdaptiveTracker_TimelapseExitRequiresFreshCalmWindow(t *testing.T) {
	cfg := AdaptiveConfig{CalmThreshold: 10 * time.Second, TimelapseInterval: 30 * time.Second, SpikeFactor: 3.0, MaxGOPBuffer: 16 << 20}
	tr := newAdaptiveTracker(cfg, "cam", testAdaptiveLogger())

	t0 := time.Now()
	idr := bytes.Repeat([]byte{0x65}, 30000)
	tr.observe(idr, true, t0)
	end := feedCalm(t, tr, t0.Add(50*time.Millisecond), 500) // 25s calm > 10s → TIMELAPSE
	if tr.mode != adaptiveTimelapse {
		t.Fatalf("mode = %v, want timelapse after sustained calm", tr.mode)
	}

	// Single isolated spike exits TIMELAPSE...
	big := make([]byte, 300000)
	big[0] = 0x41
	_, flush := tr.observe(big, false, end)
	if tr.mode != adaptiveNormal {
		t.Fatalf("mode = %v, want normal after spike", tr.mode)
	}
	if flush == nil {
		t.Fatal("expected GOP flush on timelapse exit")
	}
	// ...and the exit must reset the calm window: the very next calm frame
	// must NOT re-enter TIMELAPSE instantly (no oscillation).
	buf := make([]byte, 800)
	buf[0] = 0x41
	tr.observe(buf, false, end.Add(60*time.Millisecond))
	if tr.mode != adaptiveNormal {
		t.Fatalf("mode = %v, want normal — exit must require a fresh CalmThreshold before re-entry", tr.mode)
	}
}

func TestDefaultAdaptiveConfig_SpikeFactorCalibrated(t *testing.T) {
	cfg := DefaultAdaptiveConfig()
	if cfg.SpikeFactor != 5.0 {
		t.Fatalf("default SpikeFactor = %v, want 5.0 (issue #466 real-camera calibration)", cfg.SpikeFactor)
	}
}

func TestAdaptiveGate_SparseSkipAndFlushFacade(t *testing.T) {
	cfg := AdaptiveConfig{CalmThreshold: 10 * time.Second, TimelapseInterval: 30 * time.Second, SpikeFactor: 3.0, MaxGOPBuffer: 16 << 20}
	g := NewAdaptiveGate(cfg, "cam", testAdaptiveLogger())

	// Warm up: IDR + 25s calm (> 10s) enters timelapse.
	t0 := time.Now()
	idr := bytes.Repeat([]byte{0x65}, 30000)
	if _, skip, _ := g.Observe(idr, true, t0); skip {
		t.Fatal("IDR in NORMAL must not be skipped")
	}
	end := t0
	for i := range 500 { // 25s @ 20fps
		buf := make([]byte, 800)
		buf[0] = 0x41
		g.Observe(buf, false, end.Add(time.Duration(i)*50*time.Millisecond))
	}
	end = end.Add(25 * time.Second)
	if !g.Timelapse() {
		t.Fatal("want timelapse after sustained calm")
	}

	// Sparse mode: P frames skipped, sparse keyframes pass.
	p := make([]byte, 800)
	p[0] = 0x41
	if _, skip, _ := g.Observe(p, false, end); !skip {
		t.Fatal("P frame in timelapse must be skipped")
	}
	// Entry stamped the cadence clock, so the first sparse keyframe passes only
	// after a full TimelapseInterval (30s) from entry (~t0+10s).
	if _, skip, _ := g.Observe(idr, true, end.Add(31*time.Second)); skip {
		t.Fatal("sparse keyframe after a full interval must pass")
	}

	// Spike: no skip, GOP flush returned, back to NORMAL.
	big := make([]byte, 300000)
	big[0] = 0x41
	_, skip, flush := g.Observe(big, false, end.Add(2*time.Second))
	if skip {
		t.Fatal("spike frame must be written")
	}
	if len(flush) == 0 || !flush[0].IsIDR {
		t.Fatal("expected GOP flush starting with IDR")
	}
	if g.Timelapse() {
		t.Fatal("spike must exit timelapse")
	}
}

func TestResolveAdaptiveConfig_DefaultsAndOverrides(t *testing.T) {
	ac := ResolveAdaptiveConfig("", "", 0, 0)
	if ac != (AdaptiveConfig{CalmThreshold: 60 * time.Second, TimelapseInterval: 30 * time.Second, SpikeFactor: 5.0, MaxGOPBuffer: 32 << 20}) {
		t.Fatalf("defaults wrong: %+v", ac)
	}
	ac = ResolveAdaptiveConfig("2m", "10s", 7.5, 1<<20)
	if ac.CalmThreshold != 2*time.Minute || ac.TimelapseInterval != 10*time.Second || ac.SpikeFactor != 7.5 || ac.MaxGOPBuffer != 1<<20 {
		t.Fatalf("overrides wrong: %+v", ac)
	}
}

func TestAdaptiveGate_FlushSkipsWrittenFrames(t *testing.T) {
	cfg := AdaptiveConfig{CalmThreshold: 10 * time.Second, TimelapseInterval: 30 * time.Second, SpikeFactor: 3.0, MaxGOPBuffer: 16 << 20}
	g := NewAdaptiveGate(cfg, "cam", testAdaptiveLogger())
	t0 := time.Now()

	// IDR + calm → timelapse.
	idr := bytes.Repeat([]byte{0x65}, 30000)
	g.Observe(idr, true, t0)
	end := t0.Add(50 * time.Millisecond)
	for i := range 500 { // 25s calm
		buf := make([]byte, 800)
		buf[0] = 0x41
		g.Observe(buf, false, end.Add(time.Duration(i)*50*time.Millisecond))
	}
	end = end.Add(25 * time.Second)
	if !g.Timelapse() {
		t.Fatal("want timelapse")
	}

	// A sparse keyframe is written after the interval: the caller marks it.
	g.Observe(idr, true, end) // skipped (within interval)
	if _, skip, _ := g.Observe(idr, true, end.Add(31*time.Second)); skip {
		t.Fatal("sparse keyframe must pass")
	}
	g.MarkLastWritten() // caller wrote it successfully

	// Skipped P frames accumulate unwritten.
	p := make([]byte, 800)
	p[0] = 0x41
	for i := range 5 {
		g.Observe(p, false, end.Add((31+time.Duration(i))*time.Second))
	}

	// Spike exits timelapse: the flush must carry the sparse IDR as Written
	// (skip into the existing segment) and the skipped P frames as unwritten.
	big := make([]byte, 300000)
	big[0] = 0x41
	_, _, flush := g.Observe(big, false, end.Add(37*time.Second))
	if len(flush) < 6 {
		t.Fatalf("flush = %d frames, want >= 6", len(flush))
	}
	if !flush[0].IsIDR || !flush[0].Written {
		t.Fatal("flush anchor must be the written sparse IDR (skipped on disk re-write)")
	}
	for i, f := range flush[1:] {
		if f.Written {
			t.Fatalf("flush frame %d must be unwritten (it was skipped in sparse mode)", i+1)
		}
	}
}

func TestAdaptiveTracker_BurstGatedExit(t *testing.T) {
	cfg := AdaptiveConfig{CalmThreshold: 10 * time.Second, TimelapseInterval: 30 * time.Second, SpikeFactor: 3.0, MaxGOPBuffer: 16 << 20}
	tr := newAdaptiveTracker(cfg, "cam", testAdaptiveLogger())
	t0 := time.Now()
	idr := bytes.Repeat([]byte{0x65}, 30000)
	tr.observe(idr, true, t0)
	end := feedCalm(t, tr, t0.Add(50*time.Millisecond), 500) // 25s → TIMELAPSE
	if tr.mode != adaptiveTimelapse {
		t.Fatal("want timelapse")
	}

	// Thresholds for the 800-byte calm baseline: median=800, MAD=0 → floor=64.
	// spike thr = 800+3*64 = 992; major thr = 800+2*3*64 = 1184.
	isolated := make([]byte, 1100) // 992 < 1100 < 1184: spike but not major
	isolated[0] = 0x41

	// Isolated noise spikes (far apart) must NOT exit timelapse (#475).
	tr.observe(isolated, false, end)
	if tr.mode != adaptiveTimelapse {
		t.Fatal("isolated non-major spike must not exit timelapse")
	}
	tr.observe(isolated, false, end.Add(61*time.Second))
	if tr.mode != adaptiveTimelapse {
		t.Fatal("still timelapse after a second isolated spike")
	}

	// A spike burst (two within 2s) exits.
	tr.observe(isolated, false, end.Add(130*time.Second))
	if tr.mode != adaptiveTimelapse {
		t.Fatal("first spike of a pair must not exit alone")
	}
	tr.observe(isolated, false, end.Add(131*time.Second))
	if tr.mode == adaptiveTimelapse {
		t.Fatal("spike burst (2 within 2s) must exit timelapse")
	}

	// A MAJOR single spike (scene-cut scale) exits alone — the light-on /
	// person-appearing guard.
	feedCalm(t, tr, end.Add(131*time.Second), 800) // 40s > 10s calm → re-enter
	if tr.mode != adaptiveTimelapse {
		t.Fatal("want timelapse re-entry after calm")
	}
	major := make([]byte, 8000) // >> 1184
	major[0] = 0x41
	// Flush is nil here by design: the burst exit already detached the
	// IDR-anchored ring and no IDR has arrived since, so there is nothing
	// independently decodable to flush (takeGOP returns nil). Only the exit
	// itself is asserted.
	tr.observe(major, false, end.Add(175*time.Second))
	if tr.mode == adaptiveTimelapse {
		t.Fatal("major single spike must exit timelapse")
	}
}

func TestAdaptiveTracker_SpikeRetention(t *testing.T) {
	tr := &adaptiveTracker{}
	now := time.Now()
	tr.recordSpike(now)
	tr.recordSpike(now.Add(100 * time.Millisecond))
	if len(tr.recentSpikes) != 2 {
		t.Fatalf("recentSpikes = %d entries, want 2 (retention must survive a zero-drop append)", len(tr.recentSpikes))
	}
	if !tr.spikeBurst(now.Add(100 * time.Millisecond)) {
		t.Fatal("two spikes within the burst window must register as a burst")
	}
	// A spike after the window keeps only itself.
	tr.recordSpike(now.Add(10 * time.Second))
	if len(tr.recentSpikes) != 1 || !tr.recentSpikes[0].Equal(now.Add(10*time.Second)) {
		t.Fatalf("recentSpikes = %v, want only the newest", tr.recentSpikes)
	}
}
