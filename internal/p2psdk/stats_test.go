package peer

import (
	"log/slog"
	"runtime"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewStatsCollector_CreateAndStop verifies that a StatsCollector can be
// created and stopped without panicking.
func TestNewStatsCollector_CreateAndStop(t *testing.T) {
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = pc.Close() })

	sc := NewStatsCollector(pc, slog.Default())
	require.NotNil(t, sc)

	sc.Start()
	// Give the goroutine a moment to start.
	time.Sleep(50 * time.Millisecond)
	sc.Stop()
}

// TestStatsCollector_StopWithoutStart verifies that Stop() is safe to call
// even when Start() was never called.
func TestStatsCollector_StopWithoutStart(t *testing.T) {
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = pc.Close() })

	sc := NewStatsCollector(pc, slog.Default())
	// Calling Stop without Start should not panic or block.
	sc.Stop()
}

// TestStatsCollector_DoubleStop verifies that calling Stop() twice is safe.
func TestStatsCollector_DoubleStop(t *testing.T) {
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = pc.Close() })

	sc := NewStatsCollector(pc, slog.Default())
	sc.Start()
	time.Sleep(50 * time.Millisecond)
	sc.Stop()
	// Second call must not panic or block.
	sc.Stop()
}

// TestStatsCollector_NoGoroutineLeak verifies that the background goroutine
// exits cleanly and does not leak.
func TestStatsCollector_NoGoroutineLeak(t *testing.T) {
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = pc.Close() })

	before := runtime.NumGoroutine()

	sc := NewStatsCollector(pc, slog.Default())
	sc.Start()
	time.Sleep(50 * time.Millisecond)
	sc.Stop()

	// Give the goroutine time to exit after stopCh closes.
	time.Sleep(100 * time.Millisecond)
	after := runtime.NumGoroutine()

	assert.LessOrEqual(t, after, before+1,
		"StatsCollector goroutine should not increase goroutine count beyond 1"+
			" (golang.org might spawn cleanup goroutines)")
}

// TestStatsCollector_Collect verifies that collect() returns a snapshot
// without panicking on an idle PeerConnection.
func TestStatsCollector_Collect(t *testing.T) {
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = pc.Close() })

	sc := NewStatsCollector(pc, slog.Default())
	snap := sc.collect()

	// On an idle PC, all values should be zero.
	assert.Zero(t, snap.ICERTTSeconds)
	assert.Zero(t, snap.ICEAvailableBitrateBits)
	assert.Equal(t, uint64(0), snap.DataChannelBytesSent)
	assert.Equal(t, uint64(0), snap.DataChannelBytesReceived)
	assert.Equal(t, uint64(0), snap.DataChannelMessagesSent)
	assert.Equal(t, uint64(0), snap.DataChannelMessagesReceived)
}

// TestStatsCollector_OnStatsCallback verifies the OnStats callback is
// invoked with a snapshot.
func TestStatsCollector_OnStatsCallback(t *testing.T) {
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = pc.Close() })

	called := make(chan StatsSnapshot, 1)

	sc := NewStatsCollector(pc, slog.Default())
	sc.interval = 100 * time.Millisecond // short interval for test
	sc.OnStats = func(snap StatsSnapshot) {
		select {
		case called <- snap:
		default:
		}
	}

	sc.Start()
	defer sc.Stop()

	select {
	case <-called:
		// Callback invoked — test passes.
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for OnStats callback")
	}
}

func TestSnapshotFromReport_ObservabilityFields(t *testing.T) {
	local := webrtc.ICECandidateStats{
		ID: "lc", Type: webrtc.StatsTypeLocalCandidate,
		CandidateType: webrtc.ICECandidateTypeHost, IP: "192.168.1.2", Port: 40000,
		Protocol: "udp", Priority: 100,
	}
	remote := webrtc.ICECandidateStats{
		ID: "rc", Type: webrtc.StatsTypeRemoteCandidate,
		CandidateType: webrtc.ICECandidateTypeSrflx, IP: "8.8.8.8", Port: 50000,
		Protocol: "udp", Priority: 50,
	}
	pair := webrtc.ICECandidatePairStats{
		ID: "pair", Nominated: true, LocalCandidateID: "lc", RemoteCandidateID: "rc",
		CurrentRoundTripTime: 0.02, TotalRoundTripTime: 1.5,
		AvailableOutgoingBitrate: 1_000_000, AvailableIncomingBitrate: 2_000_000,
		RetransmissionsSent: 3, RetransmissionsReceived: 1,
	}
	sctp := webrtc.SCTPTransportStats{
		SmoothedRoundTripTime: 0.03, CongestionWindow: 1024, ReceiverWindow: 2048, MTU: 1200,
	}
	snap := snapshotFromReport(webrtc.StatsReport{
		"lc": local, "rc": remote, "pair": pair, "sctp": sctp,
	})
	if snap.SCTPCongestionWindow != 1024 || snap.SCTPReceiverWindow != 2048 || snap.SCTPMTU != 1200 {
		t.Fatalf("SCTP stats missing: %+v", snap)
	}
	if snap.SCTPSmoothedRTTSeconds != 0.03 || snap.ICERetransmissionsSent != 3 || snap.ICETotalRoundTripTime != 1.5 {
		t.Fatalf("pair/sctp scalars missing: %+v", snap)
	}
	if len(snap.Candidates) != 2 {
		t.Fatalf("want 2 candidates, got %d", len(snap.Candidates))
	}
	var remoteOK bool
	for _, c := range snap.Candidates {
		if c.IsRemote && c.CandidateType == "srflx" && c.IP == "8.8.8.8" && c.Port == 50000 {
			remoteOK = true
		}
	}
	if !remoteOK {
		t.Fatalf("remote candidate not captured: %+v", snap.Candidates)
	}
}
