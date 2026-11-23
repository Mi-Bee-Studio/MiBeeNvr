package tunnel_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/p2psdk/tunnel"
)

// TestBitrateRoundTrip pins the #134① control-plane pipe: the viewer's
// SetTargetBitrate crosses the tunnel pair and lands on the device-side
// OnTargetBitrate handler verbatim (latest-wins semantics — no ack frame).
func TestBitrateRoundTrip(t *testing.T) {
	devTun, clientTun := setupTunnelPair(t)

	got := make(chan tunnel.TunnelBitrate, 4)
	devTun.OnTargetBitrate(func(b tunnel.TunnelBitrate) { got <- b })

	require.NoError(t, clientTun.SetTargetBitrate(7, 2_500_000))
	select {
	case b := <-got:
		assert.Equal(t, uint32(7), b.StreamID)
		assert.Equal(t, uint32(2_500_000), b.TargetBps)
	case <-time.After(3 * time.Second):
		t.Fatal("bitrate target must cross the tunnel pair")
	}

	// Second target (session-level, stream 0) replaces the first — the
	// device keeps receiving a continuous stream of updates.
	require.NoError(t, clientTun.SetTargetBitrate(0, 800_000))
	select {
	case b := <-got:
		assert.Equal(t, uint32(0), b.StreamID)
		assert.Equal(t, uint32(800_000), b.TargetBps)
	case <-time.After(3 * time.Second):
		t.Fatal("second bitrate target must arrive")
	}
}

// TestBitrateCeilingRejected pins the sender-side sanity ceiling: a runaway
// estimator above MaxTargetBitrateBps errors locally instead of shipping a
// corrupt target to the agent (the agent still clamps locally).
func TestBitrateCeilingRejected(t *testing.T) {
	_, clientTun := setupTunnelPair(t)

	err := clientTun.SetTargetBitrate(0, tunnel.MaxTargetBitrateBps+1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds ceiling")
}

// TestBitrateWithoutHandlerDrops pins the no-handler tolerance: frames
// arriving before OnTargetBitrate registration (or after nil unregister)
// are dropped without disturbing the tunnel loop.
func TestBitrateWithoutHandlerDrops(t *testing.T) {
	devTun, clientTun := setupTunnelPair(t)

	// No handler registered on devTun: the frame must vanish quietly.
	require.NoError(t, clientTun.SetTargetBitrate(0, 1_000_000))
	time.Sleep(200 * time.Millisecond)
	devTun.OnTargetBitrate(nil)
	require.NoError(t, clientTun.SetTargetBitrate(0, 1_000_000))
	time.Sleep(200 * time.Millisecond)
}
