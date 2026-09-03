package pixgate

// Goroutine-leak guards (#691): every sampleFrames/sampleFramesHub call
// spawns an exec.CommandContext child whose watchCtx goroutine only retires
// via cmd.Wait() — os-level Process.Wait() does NOT close it, and neither
// does cancelling the parent ctx afterwards. watchCtx only spawns for a
// CANCELLABLE context (Start skips it when ctx.Done()==nil), so these tests
// must use an uncancelled WithCancel ctx — exactly the production shape
// (the manager's long-lived run context). On M5 the periodic sampler leaked
// ~600 watchCtx goroutines per hour until the process restarted.

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/streamhub"
)

// leakTolerance allows unrelated runtime goroutines (timers, GC helpers)
// without masking a per-call leak: 25 invocations leaking 1 goroutine each
// must fail the guard.
const (
	leakIterations = 25
	leakTolerance  = 5
)

func goroutinesSettleToBaseline(t *testing.T, baseline int) {
	t.Helper()
	require.Eventually(t, func() bool {
		return runtime.NumGoroutine() <= baseline+leakTolerance
	}, 15*time.Second, 100*time.Millisecond,
		"goroutines did not return to baseline: exec.Cmd watchers are leaking (got %d, baseline %d)",
		runtime.NumGoroutine(), baseline)
}

func TestSampleFramesHub_NoCmdWatcherLeak(t *testing.T) {
	t.Helper()
	script, stdinDump, argsDump := writeFakeEOFGatedFFmpeg(t)
	cfg := Config{
		FFmpegPath:        script,
		Env:               []string{"STDIN_DUMP=" + stdinDump, "ARGS_DUMP=" + argsDump},
		FrameStallTimeout: 4 * time.Second,
		HubBatchWindow:    500 * time.Millisecond,
	}

	// Production shape: a cancellable-but-long-lived context. A Background
	// ctx would spawn no watcher at all and the test would pass vacuously.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	baseline := runtime.NumGoroutine()
	for i := range leakIterations {
		hub := newHubWithKeyframe(t)
		n, err := sampleFramesHub(ctx, cfg, &stubHubSource{hub: hub}, 1,
			func([]byte) bool { return true }, make([]byte, GridW*GridH))
		require.NoError(t, err, "iteration %d", i)
		require.Positive(t, n, "iteration %d should decode the flushed frames", i)
	}
	goroutinesSettleToBaseline(t, baseline)
}

func TestSampleFrames_NoCmdWatcherLeak(t *testing.T) {
	t.Helper()
	empty, err := os.Create(filepath.Join(t.TempDir(), "frames.bin"))
	require.NoError(t, err)
	require.NoError(t, empty.Close())

	cfg := Config{
		FFmpegPath: "cat",
		FFmpegArgs: func(string, float64) []string { return []string{empty.Name()} },
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	baseline := runtime.NumGoroutine()
	for range leakIterations {
		// cat dumps an empty file → immediate EOF → the error return is the
		// expected path here; the point is the Cmd lifecycle, not the frames.
		_, _ = sampleFrames(ctx, cfg, Target{}, 1,
			func([]byte) bool { return true }, make([]byte, GridW*GridH))
	}
	goroutinesSettleToBaseline(t, baseline)
}

func newHubWithKeyframe(t *testing.T) *streamhub.StreamHub {
	t.Helper()
	hub := streamhub.New()
	hub.Broadcast(1, h264KeyAU(0xCC), true) // cached-IDR replay feeds the probe
	return hub
}
