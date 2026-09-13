package pixgate

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// TestManager_MetricsRecorded (#699): the sampler's Prometheus telemetry is
// the ONLY observation surface that survives journald vacuuming — the field
// incident lost 10 days of journal to disk-pressure rotation, leaving
// "sampler dead" and "scene quiet" indistinguishable after the fact. Every
// sample must bump the per-camera counter, refresh the last-sample
// timestamp (the sampler heartbeat) and the last FG area gauge; every
// confirmed activity must count a trigger.
func TestManager_MetricsRecorded(t *testing.T) {
	dir := t.TempDir()
	// Same sequence as TestManager_FiresTriggerOnConfirmedActivity: prime,
	// two person-blob frames (arms the gate at Persist=2), then quiet.
	var stream []byte
	flat := frame(100)
	person := frame(100)
	drawBlock(person, 40, 50, 30, 20, 220)
	for range 3 {
		stream = append(stream, flat...)
	}
	for range 2 {
		stream = append(stream, person...)
	}
	for range 3 {
		stream = append(stream, flat...)
	}
	frameFile := filepath.Join(dir, "frames.bin")
	if err := os.WriteFile(frameFile, stream, 0o644); err != nil {
		t.Fatal(err)
	}

	mt := metrics.NewMetrics()
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
		Metrics: mt,
		Cameras: map[string]CameraConfig{
			"cam-m": {SampleFPS: 10, MinAreaPct: 1.5, Persist: 2, Hold: time.Second},
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
		t.Fatal("pixel trigger never fired — fixture regression")
	}

	// Samples counted with the sampler's source label. The read races the
	// still-running sampler — the trigger fires on the 2nd person sample, so
	// at least prime-excluded (2 quiet + 2 person) samples have landed.
	if got := testutil.ToFloat64(mt.PixgateSamplesTotal.WithLabelValues("cam-m", "rtsp")); got < 3 {
		t.Fatalf("samples_total = %v, want ≥3 (counter must track samples)", got)
	}
	// Confirmed activity counted.
	if got := testutil.ToFloat64(mt.PixgateTriggersTotal.WithLabelValues("cam-m")); got < 1 {
		t.Fatalf("triggers_total = %v, want ≥1", got)
	}
	// Last-sample heartbeat: a fresh unix timestamp (within the test run).
	ts := testutil.ToFloat64(mt.PixgateLastSampleTimestamp.WithLabelValues("cam-m"))
	if ts < float64(time.Now().Add(-time.Minute).Unix()) {
		t.Fatalf("last_sample_timestamp = %v, want a fresh heartbeat", ts)
	}
	// Last FG area mirrors the engine's verdict.
	area := testutil.ToFloat64(mt.PixgateLastAreaPct.WithLabelValues("cam-m"))
	if area < 0 {
		t.Fatalf("last_area_pct = %v, want ≥0", area)
	}
}
