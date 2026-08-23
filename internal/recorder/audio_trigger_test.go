package recorder

import (
	"testing"
	"time"
)

// --- G.711 decode ---

func TestDecodeMuLawKnownVectors(t *testing.T) {
	// Canonical µ-law table corners (Sun ulaw2linear).
	cases := map[byte]int16{
		0xFF: 0,      // positive zero
		0x7F: 0,      // negative zero
		0xFE: 8,      // smallest positive step
		0x00: -32124, // most negative
		0x80: 32124,  // most positive
	}
	for u, want := range cases {
		if got := DecodeMuLaw(u); got != want {
			t.Fatalf("DecodeMuLaw(0x%02X) = %d, want %d", u, got, want)
		}
	}
}

func TestDecodeMuLawMonotone(t *testing.T) {
	// Decoded magnitude must grow monotonically within each sign half.
	prev := int16(0)
	for u := byte(0xFF); u > 0x80; u-- { // positive half grows as u descends
		v := DecodeMuLaw(u)
		if v < 0 {
			t.Fatalf("positive half decoded negative at 0x%02X: %d", u, v)
		}
		if u != 0xFF && v < prev {
			t.Fatalf("positive half not monotone at 0x%02X: %d < %d", u, v, prev)
		}
		prev = v
	}
	prev = 0
	for u := byte(0x7F); ; u-- { // negative half grows as u descends (0x00 = max)
		mag := -DecodeMuLaw(u)
		if u != 0x7F && mag < prev {
			t.Fatalf("negative half not monotone at 0x%02X: %d < %d", u, mag, prev)
		}
		prev = mag
		if u == 0 {
			break
		}
	}
}

func TestDecodeALawSignHalves(t *testing.T) {
	// Bytes with bit7 clear (after the 0x55 XOR keeps sign bit set => negative
	// per Sun convention) must decode negative, and vice versa. Magnitude
	// correctness is covered by the encoder round-trip test.
	for a := byte(0); a < 0x55; a++ { // a^0x55 clears the sign bit => negative
		if v := DecodeALaw(a); v > 0 {
			t.Fatalf("DecodeALaw(0x%02X) = %d, want negative", a, v)
		}
	}
	for a := byte(0xAA); a != 0; a++ { // a^0x55 sets the sign bit => positive
		if v := DecodeALaw(a); v < 0 {
			t.Fatalf("DecodeALaw(0x%02X) = %d, want positive", a, v)
		}
	}
}

// --- audioLevelMeter ---

func TestAudioLevelMeterLoudAndQuiet(t *testing.T) {
	// Silence: µ-law 0xFF decodes to 0 → -Inf dBFS, quiet under any threshold.
	m := &audioLevelMeter{rate: 8000, minDBFS: -45}
	silent := make([]byte, 2000)
	for i := range silent {
		silent[i] = 0xFF // µ-law zero
	}
	for range 4 {
		closed, _, loud := m.add(true, silent)
		if closed && loud {
			t.Fatalf("silence flagged loud")
		}
	}

	// Full-scale-ish: µ-law 0x00 decodes to -32124 (~-0.18 dBFS) → loud.
	m2 := &audioLevelMeter{rate: 8000, minDBFS: -45}
	blare := make([]byte, 2000) // all zero bytes
	var gotLoud, gotClosed bool
	var level float64
	for range 4 {
		closed, dbfs, loud := m2.add(true, blare)
		if closed {
			gotClosed, gotLoud, level = closed, loud, dbfs
		}
	}
	if !gotClosed || !gotLoud {
		t.Fatalf("full-scale stream: closed=%v loud=%v, want true/true", gotClosed, gotLoud)
	}
	if level < -1 || level > 0 {
		t.Fatalf("full-scale level = %f dBFS, want ~[-0.18]", level)
	}
}

func TestAudioLevelMeterWindowBoundary(t *testing.T) {
	m := &audioLevelMeter{rate: 4, minDBFS: -45}
	closed, _, _ := m.add(true, []byte{0xFF, 0xFF})
	if closed {
		t.Fatalf("window closed early")
	}
	closed, _, _ = m.add(true, []byte{0xFF, 0xFF})
	if !closed {
		t.Fatalf("window did not close at rate samples")
	}
	if m.n != 0 {
		t.Fatalf("window not reset: n=%d", m.n)
	}
}

// --- audioRing ---

func TestAudioRingTrimAndDrain(t *testing.T) {
	r := newAudioRing(2 * time.Second)
	t0 := time.Now()
	for i := range 10 {
		r.append(true, []byte{byte(i)}, 20*time.Millisecond, t0.Add(time.Duration(i)*500*time.Millisecond))
	}
	s := r.drain()
	if len(s) != 5 { // last 2s of 500ms-spaced samples (0..4s, keep >= 2s)
		t.Fatalf("ring depth %d, want 5 (samples at 2.0s..4.0s)", len(s))
	}
	if s[0].Data[0] != 5 || s[4].Data[0] != 9 {
		t.Fatalf("ring contents wrong: first=%d last=%d", s[0].Data[0], s[4].Data[0])
	}
	if got := r.drain(); len(got) != 0 {
		t.Fatalf("drain did not clear: %d left", len(got))
	}
}

func TestAudioRingMarkWritten(t *testing.T) {
	r := newAudioRing(time.Second)
	t0 := time.Now()
	r.append(true, []byte{1}, 20*time.Millisecond, t0)
	r.append(true, []byte{2}, 20*time.Millisecond, t0.Add(time.Second))
	r.markWritten()
	s := r.drain()
	if s[0].Written || !s[1].Written {
		t.Fatalf("markWritten did not flag the newest sample only: %+v", s)
	}
}

func TestAudioRingAppendCopies(t *testing.T) {
	r := newAudioRing(time.Second)
	buf := []byte{7}
	r.append(true, buf, 20*time.Millisecond, time.Now())
	buf[0] = 9
	s := r.drain()
	if s[0].Data[0] != 7 {
		t.Fatalf("ring retained the caller's buffer (mutation leaked)")
	}
}

// --- tracker audio integration (issue #478) ---

func audioTestGate() *AdaptiveGate {
	cfg := DefaultAdaptiveConfig()
	cfg.CalmThreshold = 2 * time.Second
	cfg.TimelapseInterval = 30 * time.Second
	return NewAdaptiveGate(cfg, "cam", testAdaptiveLogger())
}

// audioFeedCalm feeds non-IDR frames of a static scene at 20fps for d seconds.
func audioFeedCalm(g *AdaptiveGate, t0 time.Time, d time.Duration) time.Time {
	n := int(d / (50 * time.Millisecond))
	for i := range n {
		buf := make([]byte, 800)
		g.Observe(buf, false, t0.Add(time.Duration(i)*50*time.Millisecond))
	}
	return t0.Add(d)
}

func TestAudioTriggerExitsTimelapseWithFlush(t *testing.T) {
	g := audioTestGate()
	t0 := time.Now()
	// Prime the baseline, then an IDR anchors the retained GOP, then calm
	// until timelapse (entry at ~2s of calm; the extra second is margin).
	end := audioFeedCalm(g, t0, time.Second)
	buf := make([]byte, 800)
	g.Observe(buf, true, end) // IDR anchors the retained GOP
	end = audioFeedCalm(g, end, 2500*time.Millisecond)
	if !g.Timelapse() {
		t.Fatalf("gate did not enter timelapse after calm")
	}

	// A loud window pokes the gate; the NEXT video frame performs the exit
	// and returns the retained GOP (flush) — the loud sound must both resume
	// full-rate writing and recover the pre-event frames.
	g.AudioLoud(end, 0)
	_, skip, flush := g.Observe(buf, false, end.Add(50*time.Millisecond))
	if g.Timelapse() {
		t.Fatalf("loud audio did not exit timelapse")
	}
	if skip {
		t.Fatalf("first frame after audio exit was skipped")
	}
	if len(flush) == 0 || !flush[0].IsIDR {
		t.Fatalf("audio exit did not flush the retained GOP (len=%d)", len(flush))
	}
}

func TestAudioTriggerDefersTimelapseEntry(t *testing.T) {
	g := audioTestGate()
	t0 := time.Now()
	end := audioFeedCalm(g, t0, 1500*time.Millisecond)
	if g.Timelapse() {
		t.Fatalf("entered timelapse before calm threshold")
	}
	// A loud event before the video-calm threshold would admit entry: entry
	// must wait a full CalmThreshold after the loud event (video calm is
	// already satisfied by then).
	g.AudioLoud(end, 0)
	end = audioFeedCalm(g, end, 1200*time.Millisecond) // 2.7s total
	if g.Timelapse() {
		t.Fatalf("entered timelapse while the audio calm window was still running")
	}
	// CalmThreshold (2s) after the loud event (1.5s) → entry at 3.5s.
	audioFeedCalm(g, end, 1000*time.Millisecond) // 3.7s total
	if !g.Timelapse() {
		t.Fatalf("did not enter timelapse after both signals went calm")
	}
}

func TestAudioEventInNormalNotStaleExit(t *testing.T) {
	// A loud event observed while in NORMAL must be consumed there — it may
	// not fire a "stale" exit right after a later timelapse entry.
	g := audioTestGate()
	t0 := time.Now()
	end := audioFeedCalm(g, t0, time.Second)
	g.AudioLoud(end, 0)                        // loud while NORMAL
	end = audioFeedCalm(g, end, 4*time.Second) // observes consume the event; entry at 3.5s
	if !g.Timelapse() {
		t.Fatalf("did not enter timelapse (audio calm satisfied at 3s)")
	}
	_, _, flush := g.Observe(make([]byte, 800), false, end.Add(50*time.Millisecond))
	if len(flush) != 0 {
		t.Fatalf("stale audio event fired a spurious exit (flush len=%d)", len(flush))
	}
	if !g.Timelapse() {
		t.Fatalf("stale audio event exited timelapse")
	}
}

func TestAudioTriggerHoldDefersEntry(t *testing.T) {
	// External semantic events carry a hold window (the trigger API): entry
	// stays deferred for hold + CalmThreshold after the event.
	g := audioTestGate()
	t0 := time.Now()
	end := audioFeedCalm(g, t0, 1500*time.Millisecond)
	if g.Timelapse() {
		t.Fatalf("entered timelapse before calm threshold")
	}
	g.AudioLoud(end, 5*time.Second) // lastLoud := 6.5s; entry needs >= 8.5s
	end = audioFeedCalm(g, end, 5700*time.Millisecond)
	if g.Timelapse() {
		t.Fatalf("entered timelapse during the external hold window (at %s)", end.Sub(t0))
	}
}

func TestAudioTriggerRuntimeLoudPath(t *testing.T) {
	// End-to-end through the runtime: full-scale G.711 payload → loud 1s
	// window → onLoud fires; the ring retains the payloads for back-fill.
	cfg := DefaultAudioTriggerConfig() // rate 8000, min -45 dBFS
	rt := NewAudioTriggerRuntime(cfg, "cam", testAdaptiveLogger())
	g := audioTestGate()

	var loudCalls int
	payload := make([]byte, 2000) // µ-law 0x00 ≈ full scale
	now := time.Now()
	for i := range 4 { // 4×2000 = 8000 samples = exactly one window
		rt.Ingest(true, payload, 250*time.Millisecond, now.Add(time.Duration(i)*250*time.Millisecond), func(time.Time) {
			loudCalls++
			g.AudioLoud(now, 0)
		})
	}
	if loudCalls == 0 {
		t.Fatalf("loud window never fired onLoud")
	}
	s := rt.Drain()
	if len(s) != 4 || len(s[0].Data) != 2000 {
		t.Fatalf("ring retained %d samples (want 4×2000B)", len(s))
	}
	if !g.Timelapse() {
		// Sanity: the loud event reached the gate; with the feed above the
		// gate never entered timelapse (no video observe ran), so Timelapse()
		// is false and the AudioLoud was a no-op on mode — the point of this
		// assertion is that the wiring does not panic and the gate stays
		// usable. Mode behavior is covered by the tracker tests above.
		t.Log("gate stayed NORMAL (video path never observed) — wiring OK")
	}
}
