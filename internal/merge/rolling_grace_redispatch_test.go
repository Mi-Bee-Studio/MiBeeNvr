package merge

// Open-bucket freshness: on transcode-enabled cameras the #810 grace
// rail defers every young segment (< TranscodeGrace old) from the live fold
// and DROPS it back to the 10-minute backfill sweep. During the deferral
// window the open bucket's DB row never advances — the soak saw end_trails
// of 1000-2800s on a camera whose journal folded every 30s — so the
// recordings list, the timeline, and anything health-checking "latest
// recording end" all go stale. The fix: young segments stay queued in the
// event loop and re-dispatch once their grace window expires.

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	"github.com/stretchr/testify/require"
)

// TestRollingMerge_GraceRailRedispatchesYoungSegments: a fresh segment on a
// transcode-enabled camera must NOT wait for the backfill sweep — once its
// grace window expires it is folded into the bucket (source file deleted,
// bucket row exists).
func TestRollingMerge_GraceRailRedispatchesYoungSegments(t *testing.T) {
	env := newMergeTestEnv(t)
	defer env.close(t)

	bus := event.NewEventBus(16)
	cfg := config.MergeConfig{
		RollingEnabled:  boolPtr(true),
		RollingDebounce: "50ms",
		RollingWindow:   "1h",
		TranscodeGrace:  "300ms",
	}
	mt := metrics.NewMetrics()
	r := NewRollingMergeCoordinator(
		env.db, env.store,
		func() config.MergeConfig { return cfg },
		func(string) *config.MergeConfig { return nil },
		nil,
		func() []config.CameraConfig { return nil },
		mt,
		bus,
	)
	r.SetCameraTranscodeEnabled(func(string) bool { return true })

	cameraID := "cam-grace-young"
	// Fresh segment: endedAt ~ now, inside the 300ms grace window at publish.
	started := time.Now().UTC().Add(-30 * time.Second)
	p := createAndInsertSegment(t, env, "rec-young", cameraID, started)

	r.pendingTranscodePaths = func(context.Context, string) (map[string]bool, error) {
		return map[string]bool{}, nil
	}

	require.NoError(t, r.Start(context.Background()))
	defer r.Stop()

	publishSegmentCompletedAt(t, bus, cameraID, "rec-young", p, "h264", started, time.Now().UTC())

	// Pre-fix behavior: the dispatch defers the young segment and drops it —
	// nothing folds until the backfill sweep (10m default). Post-fix the grace
	// timer re-dispatches and the segment folds: source gone, bucket exists.
	require.Eventually(t, func() bool {
		if _, ok := r.buckets.Load(cameraID); !ok {
			return false
		}
		_, err := os.Stat(p)
		return os.IsNotExist(err)
	}, 15*time.Second, 100*time.Millisecond,
		"young segment must fold into the bucket once its grace window expires (no backfill wait)")
}

// TestRollingMerge_GraceRailStillDefersWithinWindow: within the grace window
// the young segment must NOT be folded (the #810 TOCTOU protection keeps
// working) — source file still present, no bucket yet.
func TestRollingMerge_GraceRailStillDefersWithinWindow(t *testing.T) {
	env := newMergeTestEnv(t)
	defer env.close(t)

	bus := event.NewEventBus(16)
	cfg := config.MergeConfig{
		RollingEnabled:  boolPtr(true),
		RollingDebounce: "50ms",
		RollingWindow:   "1h",
		TranscodeGrace:  "10s", // far beyond this test's patience window
	}
	mt := metrics.NewMetrics()
	r := NewRollingMergeCoordinator(
		env.db, env.store,
		func() config.MergeConfig { return cfg },
		func(string) *config.MergeConfig { return nil },
		nil,
		func() []config.CameraConfig { return nil },
		mt,
		bus,
	)
	r.SetCameraTranscodeEnabled(func(string) bool { return true })

	cameraID := "cam-grace-hold"
	// Fresh segment inside a 10s grace window.
	started := time.Now().UTC().Add(-30 * time.Second)
	p := createAndInsertSegment(t, env, "rec-hold", cameraID, started)

	r.pendingTranscodePaths = func(context.Context, string) (map[string]bool, error) {
		return map[string]bool{}, nil
	}

	require.True(t, r.CameraTranscodeEnabled(cameraID), "test premise: camera must resolve as transcode-enabled")
	require.Greater(t, r.resolveRollingConfig(cameraID).TranscodeGrace, time.Duration(0), "test premise: grace rail must be on")

	require.NoError(t, r.Start(context.Background()))
	defer r.Stop()

	publishSegmentCompletedAt(t, bus, cameraID, "rec-hold", p, "h264", started, time.Now().UTC())

	// Well within the 10s grace: no fold may happen.
	time.Sleep(1 * time.Second)
	require.FileExists(t, p, "young segment must stay deferred inside its grace window (#810)")
	_, ok := r.buckets.Load(cameraID)
	require.False(t, ok, "no bucket may exist while the only segment is deferred")
}
