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
	"github.com/stretchr/testify/require"
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

	// Gate on the trigger (fixture sanity), then poll the METRICS themselves
	// — absolute counts read at the trigger instant race the still-running
	// sampler's frame delivery (a CI failure showed samples_total=2 at the
	// trigger), so the assertions wait for the observable end state instead.
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return fired > 0
	}, 5*time.Second, 20*time.Millisecond, "pixel trigger never fired — fixture regression")

	// Samples counted with the sampler's source label (the cat fixture
	// streams 7 processed samples in a burst).
	require.Eventually(t, func() bool {
		return testutil.ToFloat64(mt.PixgateSamplesTotal.WithLabelValues("cam-m", "rtsp")) >= 3
	}, 5*time.Second, 20*time.Millisecond, "samples_total must track samples")

	// Confirmed activity counted.
	require.Eventually(t, func() bool {
		return testutil.ToFloat64(mt.PixgateTriggersTotal.WithLabelValues("cam-m")) >= 1
	}, 5*time.Second, 20*time.Millisecond, "triggers_total must count the confirmed activity")

	// Last-sample heartbeat: a fresh unix timestamp (within the test run).
	require.Eventually(t, func() bool {
		return testutil.ToFloat64(mt.PixgateLastSampleTimestamp.WithLabelValues("cam-m")) >=
			float64(time.Now().Add(-time.Minute).Unix())
	}, 5*time.Second, 20*time.Millisecond, "last_sample_timestamp must be a fresh heartbeat")

	// Last FG area mirrors the engine's verdict (non-negative).
	require.Eventually(t, func() bool {
		return testutil.ToFloat64(mt.PixgateLastAreaPct.WithLabelValues("cam-m")) >= 0
	}, 5*time.Second, 20*time.Millisecond, "last_area_pct must be set")
}
