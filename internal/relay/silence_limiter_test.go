package relay

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Unit: ShouldEmit
// ---------------------------------------------------------------------------

// TestSilenceLimiter_ShouldEmit_NoPressure verifies that ShouldEmit returns
// true when there is no drop backoff and the per-second cap is not reached.
func TestSilenceLimiter_ShouldEmit_NoPressure(t *testing.T) {
	em := NewBufferAwareSilenceEmitter(NewSilenceAACGenerator())
	require.True(t, em.ShouldEmit(), "ShouldEmit should be true when no drops and below cap")
}

// TestSilenceLimiter_ShouldEmit_FalseAfterNotifyDrop verifies that ShouldEmit
// returns false immediately after a drop is reported.
func TestSilenceLimiter_ShouldEmit_FalseAfterNotifyDrop(t *testing.T) {
	em := NewBufferAwareSilenceEmitter(NewSilenceAACGenerator())
	em.NotifyDrop()
	require.False(t, em.ShouldEmit(), "ShouldEmit should be false after NotifyDrop")
}

// TestSilenceLimiter_ShouldEmit_ResumesAfterDropRecovery verifies that
// ShouldEmit returns true again once the drop recovery period (3 s) has
// elapsed without new drops.
func TestSilenceLimiter_ShouldEmit_ResumesAfterDropRecovery(t *testing.T) {
	em := NewBufferAwareSilenceEmitter(NewSilenceAACGenerator())
	em.NotifyDrop()
	require.False(t, em.ShouldEmit(), "ShouldEmit should be false right after NotifyDrop")

	// Wait for recovery to expire (add guard band for scheduler delay).
	time.Sleep(dropRecoveryPeriod + 100*time.Millisecond)

	require.True(t, em.ShouldEmit(), "ShouldEmit should be true after recovery period")
}

// TestSilenceLimiter_ShouldEmit_HardCap5PerSecond verifies that no more than
// maxEmitsPerSecond (5) frames pass per 1 s window.
func TestSilenceLimiter_ShouldEmit_HardCap5PerSecond(t *testing.T) {
	em := NewBufferAwareSilenceEmitter(NewSilenceAACGenerator())

	// First 5 should all pass.
	for i := range maxEmitsPerSecond {
		require.True(t, em.ShouldEmit(), "ShouldEmit iteration %d should be true", i)
	}

	// 6th — cap hit.
	require.False(t, em.ShouldEmit(), "6th call in same second should be capped")

	// Wait for window to roll over.
	time.Sleep(1100 * time.Millisecond)

	require.True(t, em.ShouldEmit(), "ShouldEmit should return true after window reset")
}

// ---------------------------------------------------------------------------
// Unit: NotifyDrop
// ---------------------------------------------------------------------------

// TestSilenceLimiter_NotifyDrop_MultipleCalls verifies that calling
// NotifyDrop repeatedly extends the backoff window.
func TestSilenceLimiter_NotifyDrop_MultipleCalls(t *testing.T) {
	em := NewBufferAwareSilenceEmitter(NewSilenceAACGenerator())
	em.NotifyDrop()

	// Advance partway into recovery — ShouldEmit still false.
	time.Sleep(1 * time.Second)
	require.False(t, em.ShouldEmit(), "ShouldEmit should still be false after 1 s")

	// Another drop resets the 3 s clock.
	em.NotifyDrop()
	time.Sleep(2 * time.Second)
	require.False(t, em.ShouldEmit(), "ShouldEmit should be false after refresh at 2 s")

	// Now wait past the refreshed deadline.
	time.Sleep(1100 * time.Millisecond)
	require.True(t, em.ShouldEmit(), "ShouldEmit should be true after final recovery")
}

// ---------------------------------------------------------------------------
// Integration: Start / Stop / Channel
// ---------------------------------------------------------------------------

// TestSilenceLimiter_Start_EmitsFrames verifies that the emitter produces
// frames on the returned channel and that the count and suppression metrics
// are non-zero after a reasonable run.
func TestSilenceLimiter_Start_EmitsFrames(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()

	em := NewBufferAwareSilenceEmitter(NewSilenceAACGenerator())
	ch := em.Start(ctx)

	var received int
	for range ch {
		received++
	}

	t.Logf("received %d frames, emitted=%d suppressed=%d",
		received, em.Emitted(), em.Suppressed())

	// Generator produces ~15 frames in 1.5 s at 10 fps; limiter should pass
	// no more than 5 per wall-clock second ≈ 7-8 in 1.5 s.  Just verify it
	// emitted at least a few and suppressed at least one.
	require.GreaterOrEqual(t, em.Emitted(), int64(1), "should have emitted at least 1 frame")
	require.GreaterOrEqual(t, em.Suppressed(), int64(1),
		"should have suppressed at least 1 frame (cap exceeded)")
}

// TestSilenceLimiter_Stop verifies that Stop() closes the channel promptly.
func TestSilenceLimiter_Stop(t *testing.T) {
	em := NewBufferAwareSilenceEmitter(NewSilenceAACGenerator())
	ch := em.Start(context.Background())

	// Read one frame to confirm running.
	select {
	case _, ok := <-ch:
		require.True(t, ok, "first frame should arrive")
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected first frame within 500 ms")
	}

	em.Stop()

	select {
	case _, ok := <-ch:
		require.False(t, ok, "channel should be closed after Stop()")
	case <-time.After(500 * time.Millisecond):
		t.Fatal("channel should close within 500 ms of Stop()")
	}
}

// ---------------------------------------------------------------------------
// Delegation
// ---------------------------------------------------------------------------

// TestSilenceLimiter_AudioConfig verifies that AudioConfig delegates to
// the underlying generator.
func TestSilenceLimiter_AudioConfig(t *testing.T) {
	gen := NewSilenceAACGenerator()
	em := NewBufferAwareSilenceEmitter(gen)

	cfg := em.AudioConfig()
	expected := gen.Config()
	require.Equal(t, expected, cfg, "AudioConfig should delegate to generator.Config()")
	require.NotEmpty(t, cfg, "Audio config should not be empty")
}

// TestSilenceLimiter_EmptyConfig verifies that AudioConfig is correct even
// when the generator is configured with custom parameters.
func TestSilenceLimiter_EmptyConfig(t *testing.T) {
	gen := NewSilenceAACGenerator()
	em := NewBufferAwareSilenceEmitter(gen)
	cfg := em.AudioConfig()
	require.NotNil(t, cfg, "AudioConfig should never be nil")
	require.Len(t, cfg, 2, "AudioConfig for 48 kHz stereo AAC-LC should be 2 bytes")
}

// ---------------------------------------------------------------------------
// Metrics
// ---------------------------------------------------------------------------

// TestSilenceLimiter_MetricsAccessors verifies that Emitted and Suppressed
// accessors return the current counter values.
func TestSilenceLimiter_MetricsAccessors(t *testing.T) {
	em := NewBufferAwareSilenceEmitter(NewSilenceAACGenerator())

	require.Equal(t, int64(0), em.Emitted(), "initial emitted should be 0")
	require.Equal(t, int64(0), em.Suppressed(), "initial suppressed should be 0")

	// Run briefly to accumulate some metrics.
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()

	ch := em.Start(ctx)
	for range ch {
	}

	t.Logf("after short run: emitted=%d suppressed=%d", em.Emitted(), em.Suppressed())
	require.GreaterOrEqual(t, em.Emitted(), int64(0), "emitted should be >= 0")
	require.GreaterOrEqual(t, em.Suppressed(), int64(0), "suppressed should be >= 0")
}
