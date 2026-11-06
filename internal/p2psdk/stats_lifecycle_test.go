package peer

import (
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/require"
)

// TestStartStatsCollectorLocked_StopsPrevious verifies the leak fix for the
// NVR-reported log noise: rebuilding a session must stop the previous stats
// poller instead of orphaning it on a closed PeerConnection ("the agent is
// closed" spam on every GetStats tick).
func TestStartStatsCollectorLocked_StopsPrevious(t *testing.T) {
	p := &p2pSession{}

	pcA, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = pcA.Close() }) //nolint:errcheck // test cleanup
	pcB, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = pcB.Close() }) //nolint:errcheck // test cleanup

	first := NewStatsCollector(pcA, nil)
	first.Start()
	p.mu.Lock()
	p.statsCollector = first
	p.startStatsCollectorLocked(pcB)
	p.mu.Unlock()

	require.NotSame(t, first, p.statsCollector,
		"rebinding must install a fresh collector")
	select {
	case <-first.done:
		// Previous poller drained — no orphan on the old PC.
	case <-time.After(2 * time.Second):
		t.Fatal("previous stats collector was not stopped on rebind")
	}

	p.mu.Lock()
	p.statsCollector.Stop()
	p.mu.Unlock()
}

// TestStartStatsCollectorLocked_NilOverPrevious verifies the nil-session boot
// path: no prior collector must not panic.
func TestStartStatsCollectorLocked_NilOverPrevious(t *testing.T) {
	p := &p2pSession{}
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = pc.Close() }) //nolint:errcheck // test cleanup

	require.Nil(t, p.statsCollector)
	p.mu.Lock()
	p.startStatsCollectorLocked(pc)
	p.mu.Unlock()
	require.NotNil(t, p.statsCollector)

	p.mu.Lock()
	p.statsCollector.Stop()
	p.mu.Unlock()
}

// TestStatsCollectorSelfStopsOnClosedPC verifies the 2026-10-19 self-heal:
// a collector whose session was never close()d (leaked by whatever
// lifecycle path) stops itself once the PeerConnection reports Closed,
// instead of polling GetStats forever (fleet evidence: ~36k
// "the agent is closed" journal lines/day from ~2 orphaned collectors).
func TestStatsCollectorSelfStopsOnClosedPC(t *testing.T) {
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	require.NoError(t, pc.Close())

	sc := NewStatsCollector(pc, nil)
	sc.interval = 50 * time.Millisecond
	sc.Start()

	select {
	case <-sc.done:
		// Self-stopped on the first tick against the closed PC.
	case <-time.After(2 * time.Second):
		t.Fatal("collector kept polling a closed PeerConnection")
	}

	// A late Stop() (the leaked session's eventual close) stays safe.
	sc.Stop()
}
