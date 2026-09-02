package pixgate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakePublisher records published events.
type fakePublisher struct {
	mu     sync.Mutex
	events []Event
}

func (p *fakePublisher) Publish(_ context.Context, _ string, data interface{}) {
	if e, ok := data.(Event); ok {
		p.mu.Lock()
		p.events = append(p.events, e)
		p.mu.Unlock()
	}
}

// TestManager_FiresTriggerOnConfirmedActivity runs the manager against a
// fake "ffmpeg" (cat streaming prebuilt gray frames from a file) and asserts
// the full loop: resolve → sample → CV → trigger + event.
func TestManager_FiresTriggerOnConfirmedActivity(t *testing.T) {
	dir := t.TempDir()
	// Sequence file: prime frame (flat 100), two person frames (blob at x=40),
	// then quiet frames. The engine primes on sample 1, arms on the second
	// person sample (Persist 2).
	var stream []byte
	flat := frame(100)
	person := frame(100)
	drawBlock(person, 40, 50, 30, 20, 220)
	stream = append(stream, flat...)
	stream = append(stream, person...)
	stream = append(stream, person...)
	stream = append(stream, flat...)
	stream = append(stream, flat...)
	stream = append(stream, flat...)
	frameFile := filepath.Join(dir, "frames.bin")
	if err := os.WriteFile(frameFile, stream, 0o644); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	fired := 0
	pub := &fakePublisher{}
	m := NewManager(Config{
		FFmpegPath: "cat",
		FFmpegArgs: func(string, float64) []string { return []string{frameFile} },
		Resolver: func(context.Context, string) (Target, bool, error) {
			return Target{URL: "rtsp://example/stream"}, true, nil
		},
		Trigger: func(string, time.Duration) error {
			mu.Lock()
			fired++
			mu.Unlock()
			return nil
		},
		Bus: pub,
		Cameras: map[string]CameraConfig{
			"cam-1": {SampleFPS: 10, MinAreaPct: 1.5, Persist: 2, Hold: time.Second},
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	if err := m.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer m.Stop()
	defer cancel()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		f := fired
		mu.Unlock()
		if f > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if fired == 0 {
		t.Fatal("pixel trigger never fired for the person-blob sequence")
	}
	pub.mu.Lock()
	defer pub.mu.Unlock()
	if len(pub.events) == 0 {
		t.Fatal("pixgate.activity event never published")
	}
	if !pub.events[len(pub.events)-1].Active && fired == 0 {
		t.Fatal("expected an active event")
	}
}

// TestManager_NoTargetDisablesCamera: a camera without a resolvable RTSP
// sub-stream logs and exits its goroutine without firing anything.
func TestManager_NoTargetDisablesCamera(t *testing.T) {
	m := NewManager(Config{
		FFmpegPath: "cat",
		Resolver: func(context.Context, string) (Target, bool, error) {
			return Target{}, false, nil
		},
		Trigger: func(string, time.Duration) error { return nil },
		Cameras: map[string]CameraConfig{"cam-1": {}},
	})
	ctx, cancel := context.WithCancel(context.Background())
	if err := m.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	m.Stop()
	cancel()
}

// TestManager_NoFFmpegStaysOff: the optional dependency is missing → Start
// succeeds as a no-op (WARN path).
func TestManager_NoFFmpegStaysOff(t *testing.T) {
	m := NewManager(Config{Resolver: func(context.Context, string) (Target, bool, error) {
		return Target{URL: "rtsp://x"}, true, nil
	}, Cameras: map[string]CameraConfig{"cam-1": {}}})
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("start without ffmpeg must be a no-op, got %v", err)
	}
	m.Stop()
}

// TestManager_SegmentStatsMath drives the FG ring directly and checks the
// window math the activity-score path consumes: duty cycle, mean area,
// coverage, invalid-sample exclusion, out-of-window exclusion.
func TestManager_SegmentStatsMath(t *testing.T) {
	m := NewManager(Config{})
	m.ensureRing("cam-s", CameraConfig{SampleFPS: 2, MinAreaPct: 2})

	base := time.Now().Add(-time.Hour)
	// One sample long before the window — must be excluded.
	m.recordFG("cam-s", base.Add(-time.Minute), 9, true)
	// In-window: six valid + one invalid, 0.5s apart (fps 2 → full coverage).
	areas := []float64{0.1, 3, 0.2, 5, 0.1, 2}
	for i, a := range areas {
		m.recordFG("cam-s", base.Add(time.Duration(i)*500*time.Millisecond), a, true)
	}
	m.recordFG("cam-s", base.Add(3*time.Second), 9, false)

	st, ok := m.SegmentStats("cam-s", base, base.Add(4*time.Second))
	if !ok {
		t.Fatal("expected stats for camera with samples")
	}
	if st.Samples != 7 || st.Valid != 6 {
		t.Fatalf("samples/valid = %d/%d, want 7/6", st.Samples, st.Valid)
	}
	if st.DutyCycle < 0.49 || st.DutyCycle > 0.51 {
		t.Fatalf("duty = %v, want 0.5 (3 of 6 at/above trig 2%%)", st.DutyCycle)
	}
	wantMean := (0.1 + 3 + 0.2 + 5 + 0.1 + 2) / 6
	if st.MeanAreaPct < wantMean-0.01 || st.MeanAreaPct > wantMean+0.01 {
		t.Fatalf("mean = %v, want ~%v", st.MeanAreaPct, wantMean)
	}
	if st.Coverage > 1 {
		t.Fatalf("coverage must cap at 1, got %v", st.Coverage)
	}
	if st.TrigAreaPct != 2 || st.FPS != 2 {
		t.Fatalf("trig/fps = %v/%v, want 2/2", st.TrigAreaPct, st.FPS)
	}

	if _, ok := m.SegmentStats("cam-unknown", base, base.Add(time.Second)); ok {
		t.Fatal("unknown camera must report ok=false")
	}
	// Window with zero samples inside → ok=false.
	if _, ok := m.SegmentStats("cam-s", base.Add(10*time.Minute), base.Add(11*time.Minute)); ok {
		t.Fatal("empty window must report ok=false")
	}
}

// TestManager_SuppressBlindsGate: with a suppression window active, the
// person-blob stream that normally fires (see TestManager_FiresTrigger…)
// stays silent and every sample lands invalid.
func TestManager_SuppressBlindsGate(t *testing.T) {
	dir := t.TempDir()
	var stream []byte
	flat := frame(100)
	person := frame(100)
	drawBlock(person, 40, 50, 30, 20, 220)
	stream = append(stream, flat...)
	for range 6 {
		stream = append(stream, person...)
	}
	frameFile := filepath.Join(dir, "frames.bin")
	if err := os.WriteFile(frameFile, stream, 0o644); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	fired := 0
	m := NewManager(Config{
		FFmpegPath: "cat",
		FFmpegArgs: func(string, float64) []string { return []string{frameFile} },
		Resolver: func(context.Context, string) (Target, bool, error) {
			return Target{URL: "rtsp://example/stream"}, true, nil
		},
		Trigger: func(string, time.Duration) error {
			mu.Lock()
			fired++
			mu.Unlock()
			return nil
		},
		Bus: &fakePublisher{},
		Cameras: map[string]CameraConfig{
			"cam-1": {SampleFPS: 10, MinAreaPct: 1.5, Persist: 2, Hold: time.Second},
		},
	})
	m.Suppress("cam-1", 30*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	if err := m.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer m.Stop()
	defer cancel()

	time.Sleep(700 * time.Millisecond)
	mu.Lock()
	f := fired
	mu.Unlock()
	if f != 0 {
		t.Fatalf("suppressed gate must not fire, got %d triggers", f)
	}
	st, ok := m.SegmentStats("cam-1", time.Now().Add(-time.Minute), time.Now())
	if !ok || st.Samples == 0 {
		t.Fatal("suppressed samples must still be recorded (invalid)")
	}
	if st.Valid != 0 {
		t.Fatalf("suppressed samples must be invalid, got %d valid of %d", st.Valid, st.Samples)
	}
}

// TestHelperStallProcess is not a real test: it is spawned as the fake ffmpeg
// by TestSampleFramesStallKillsWedgedSource. It emits exactly one frame, then
// blocks forever — the shape of an ffmpeg wedged in a network read.
func TestHelperStallProcess(t *testing.T) {
	if os.Getenv("GO_PIXGATE_HELPER") != "stall" {
		return
	}
	if _, err := os.Stdout.Write(make([]byte, GridW*GridH)); err != nil {
		os.Exit(1)
	}
	// Sleep-loop rather than select{}: without a pending timer the runtime's
	// deadlock detector kills the process (panic → EOF on the pipe), which is
	// the exited-source shape, not the wedged-source shape under test.
	for {
		time.Sleep(time.Hour)
	}
}

// TestSampleFramesStallKillsWedgedSource (2026-09-02 incident, 05:01 network
// blip): a sampler source that emits one frame and then goes silent WITHOUT
// exiting must be aborted after FrameStallTimeout instead of blocking
// io.ReadFull forever (field shape: pixgate silent from 02:01 EOF-respawn
// until the 05:09 service restart). Before the fix this test hangs.
func TestSampleFramesStallKillsWedgedSource(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GO_PIXGATE_HELPER", "stall")
	cfg := Config{
		FFmpegPath: exe,
		FFmpegArgs: func(string, float64) []string {
			return []string{"-test.run=TestHelperStallProcess"}
		},
		FrameStallTimeout: 300 * time.Millisecond,
	}
	fr := make([]byte, GridW*GridH)
	n, serr := sampleFrames(context.Background(), cfg, Target{URL: "rtsp://example/stream"}, 10,
		func([]byte) bool { return true }, fr)
	if n != 1 {
		t.Fatalf("frames = %d, want 1 (the first frame must arrive before the stall)", n)
	}
	if serr == nil || !strings.Contains(serr.Error(), "stall") {
		t.Fatalf("want a stall error, got %v", serr)
	}
}

// TestFFmpegArgsNoUnsupportedOptions: the sampler args must only use options
// portable across the fleet's ffmpeg builds. -rw_timeout looked like a second
// line of defense against wedged RTSP reads, but M5's ffmpeg 4.4 rejects it
// outright ("Option rw_timeout not found" → Error opening input → sampler
// EOF-loop with zero frames, deployed 2026-09-02 11:56 and caught live) — the
// stall watchdog in sampleFrames is the portable defense.
func TestFFmpegArgsNoUnsupportedOptions(t *testing.T) {
	args := ffmpegArgs(Target{URL: "rtsp://example/stream"}, 1)
	for _, banned := range []string{"-rw_timeout", "-stimeout", "-timeout"} {
		for _, a := range args {
			if a == banned {
				t.Fatalf("ffmpegArgs must not use %s (rejected by ffmpeg 4.4 on M5): %v", banned, args)
			}
		}
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"-rtsp_transport tcp", "-i rtsp://example/stream", "-f rawvideo", "-pix_fmt gray"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args missing %q: %v", want, args)
		}
	}
}
