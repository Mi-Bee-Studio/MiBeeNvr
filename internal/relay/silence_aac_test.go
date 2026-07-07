package relay

import (
	"context"
	"testing"
	"time"

	"github.com/bluenviron/mediacommon/v2/pkg/codecs/mpeg4audio"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Test 1: AudioSpecificConfig marshals correctly for 48kHz stereo AAC-LC.
// ---------------------------------------------------------------------------

func TestSilenceAAC_ConfigBytes(t *testing.T) {
	g := NewSilenceAACGenerator()
	cfg := g.Config()

	// 0x11 0x90 = AAC-LC 48kHz stereo AudioSpecificConfig per mediacommon tests.
	expected := []byte{0x11, 0x90}
	require.Equal(t, expected, cfg, "Config() should return marshaled AudioSpecificConfig for 48kHz stereo AAC-LC")

	// Round-trip: unmarshal back and verify fields.
	var dec mpeg4audio.AudioSpecificConfig
	err := dec.Unmarshal(cfg)
	require.NoError(t, err, "Config() bytes must be a valid AudioSpecificConfig")
	require.Equal(t, mpeg4audio.ObjectTypeAACLC, dec.Type)
	require.Equal(t, 48000, dec.SampleRate)
	require.Equal(t, uint8(2), dec.ChannelConfig)
}

// ---------------------------------------------------------------------------
// Test 2: Silent frame has expected length and structure.
// ---------------------------------------------------------------------------

func TestSilenceAAC_FrameBytes(t *testing.T) {
	g := NewSilenceAACGenerator()
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	ch := g.Start(ctx)
	frame, ok := <-ch
	require.True(t, ok, "should receive at least one frame before timeout")

	// Known-correct silent AAC-LC frame for 48kHz stereo: 6 bytes.
	require.Len(t, frame, 6, "silent AAC-LC frame should be 6 bytes")
	// Non-empty.
	require.NotZero(t, frame[0], "frame should not start with zero byte")
}

// ---------------------------------------------------------------------------
// Test 3: Emission rate is ~10fps.
// ---------------------------------------------------------------------------

func TestSilenceAAC_EmissionRate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()

	g := NewSilenceAACGenerator()
	ch := g.Start(ctx)

	var count int
	for range ch {
		count++
	}

	// In ~1.5s at 100ms/tick expect ~15 frames.  Allow ±40% for CI variance.
	require.InDelta(t, 15, count, 6, "expected ~15 frames in 1.5s at 10fps")
}

// ---------------------------------------------------------------------------
// Test 4: Generator auto-stops after 5s without SourceActive().
// ---------------------------------------------------------------------------

func TestSilenceAAC_AutoStop(t *testing.T) {
	ctx := context.Background()
	g := NewSilenceAACGenerator()
	ch := g.Start(ctx)

	start := time.Now()
	var count int
	for range ch {
		count++
	}
	elapsed := time.Since(start)

	// Should auto-stop within 6s (5s timeout + up to 1s ticker grace).
	require.Less(t, elapsed, 7*time.Second, "should stop within 7s")
	// Should have emitted at least some frames before stopping.
	require.GreaterOrEqual(t, count, 10, "should emit at least 10 frames before auto-stop")
}

// ---------------------------------------------------------------------------
// Test 5: SourceActive() resets the silence timer (keeps generator alive).
// ---------------------------------------------------------------------------

func TestSilenceAAC_SourceActiveReset(t *testing.T) {
	ctx := context.Background()

	// Short timeout so the test is fast.
	g := NewSilenceAACGeneratorWith(SilenceAACConfig{
		MaxContinuousSilence: 200 * time.Millisecond,
	})
	ch := g.Start(ctx)

	// Periodically call SourceActive to keep it alive.
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for range 40 {
			select {
			case <-ticker.C:
				g.SourceActive()
			case <-done:
				return
			}
		}
	}()

	// Read for 3s — generator should stay alive because SourceActive is called.
	var count int
	timeout := time.After(3 * time.Second)
loop:
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				break loop
			}
			count++
		case <-timeout:
			break loop
		}
	}
	close(done)
	g.Stop()

	// With 200ms timeout reset every 100ms, should survive 3s.
	require.GreaterOrEqual(t, count, 15,
		"should stay alive with periodic SourceActive calls")
}

// ---------------------------------------------------------------------------
// Test 6: Non-blocking send — slow consumer must not block the producer.
// ---------------------------------------------------------------------------

func TestSilenceAAC_NonBlockingSend(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()

	g := NewSilenceAACGenerator()
	ch := g.Start(ctx)

	// Don't read from channel for 200ms. Producer must not block on full channel.
	time.Sleep(200 * time.Millisecond)

	// Now drain whatever is in the channel.
	var count int
	for range ch {
		count++
	}

	// The test should NOT hang for 5s. If it completes in < 800ms the producer
	// was properly dropping frames. No assertion on count — just verify the
	// test completes without deadlock.
	t.Logf("read %d frames after 200ms consumer delay (non-blocking verified by exit)", count)
}

// ---------------------------------------------------------------------------
// Test 7: Stop() cancels generation immediately.
// ---------------------------------------------------------------------------

func TestSilenceAAC_Stop(t *testing.T) {
	g := NewSilenceAACGenerator()
	ctx := context.Background()
	ch := g.Start(ctx)

	// Read a couple frames to confirm it's running.
	select {
	case _, ok := <-ch:
		require.True(t, ok, "first frame should arrive")
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected first frame within 500ms")
	}

	// Stop immediately.
	g.Stop()

	// Channel should close quickly.
	select {
	case _, ok := <-ch:
		require.False(t, ok, "channel should be closed after Stop()")
	case <-time.After(500 * time.Millisecond):
		t.Fatal("channel should close within 500ms of Stop()")
	}
}

// ---------------------------------------------------------------------------
// Test 8: Canonical frame bytes match known-correct values.
// ---------------------------------------------------------------------------

func TestSilenceAAC_CanonicalFrame(t *testing.T) {
	// The canonical silent AAC-LC raw frame for 48kHz stereo, captured from
	// FFmpeg anullsrc -> AAC.  Used to verify the pre-computed constant hasn't
	// been accidentally changed.
	expected := []byte{0x21, 0x10, 0x04, 0x60, 0x8c, 0x1c}

	frame := silenceAACFrame48kStereo
	require.Equal(t, expected, frame, "canonical silent AAC-LC frame bytes must match")
}
