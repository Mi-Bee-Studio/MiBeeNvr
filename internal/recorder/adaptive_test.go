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
