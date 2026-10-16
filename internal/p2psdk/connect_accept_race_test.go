package peer

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestConnectAcceptOfferBackToBack (#89): connect_accept and sdp_offer are
// relayed back-to-back by the server; when handleConnectAccept deferred
// startAsAnswerer to a goroutine, the read loop could process the offer
// before the PeerConnection existed — handleRemoteOffer hit a nil pc and
// dropped the offer, killing the handshake on both sides. The synchronous
// fix makes the ordering structurally safe: the accept handler cannot
// return before the pc exists, and the read loop is serial.
//
// Determinism note: on a loaded CI box the pre-fix race reproduces with
// GOMAXPROCS=1 (issue reports 2/3 failures with -race -count=3); this test
// is the canary for that window in the normal flow.
func TestConnectAcceptOfferBackToBack(t *testing.T) {
	mock := newMockServer(t)
	// Deliberately NO connectAcceptDelay: accept and offer must land
	// adjacently at the answerer, which is exactly the racy window.

	device, err := Dial(context.Background(), SignalingConfig{
		ServerURL: mock.url,
		JWT:       "test-device-jwt",
		ProductID: "test-product",
		PeerID:    "device-1",
		Role:      "device",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = device.Close() })

	deviceDCOpen := make(chan struct{}, 1)
	device.OnDataChannel = func(DataChannel) { deviceDCOpen <- struct{}{} }

	client, err := Dial(context.Background(), SignalingConfig{
		ServerURL: mock.url,
		JWT:       "test-client-jwt",
		ProductID: "test-product",
		PeerID:    "client-1",
		Role:      "client",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	require.NoError(t, client.Connect(context.Background(), "device-1"))

	// Pre-fix, a dropped offer means neither side ever opens a channel —
	// the 10s guard then fails. Generous enough to stay green on slow
	// runners, tight enough to catch the permanent hang.
	select {
	case <-deviceDCOpen:
	case <-time.After(10 * time.Second):
		t.Fatal("answerer never opened a DataChannel — sdp_offer was dropped " +
			"in the connect_accept back-to-back window (#89)")
	}
}

// TestSetForceRelay_ConcurrentSnapshotReads (#89): SetForceRelay writes cfg
// after Dial while session-construction points read it via sessionConfig();
// under -race, concurrent unsynchronized access must not be flagged and the
// written value must become visible to subsequent snapshots.
func TestSetForceRelay_ConcurrentSnapshotReads(t *testing.T) {
	mock := newMockServer(t)
	sess, err := Dial(context.Background(), SignalingConfig{
		ServerURL: mock.url,
		JWT:       "test-device-jwt",
		ProductID: "test-product",
		PeerID:    "device-1",
		Role:      "device",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sess.Close() })

	require.False(t, sess.sessionConfig().ForceRelay)

	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() { // reader: the idle-sweep / handler read pattern
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = sess.sessionConfig().ForceRelay
			}
		}
	}()
	for i := 0; i < 50; i++ {
		sess.SetForceRelay(i%2 == 0)
	}
	close(stop)
	wg.Wait()

	sess.SetForceRelay(true)
	require.True(t, sess.sessionConfig().ForceRelay)
}

// TestDetectNATClass_DualStackReportedIndependently (#89): the dual-stack
// interface fact is reported even when the v4 classification cannot run
// (fewer than two servers), and globalIPv6Probe is injectable for tests.
func TestDetectNATClass_DualStackReportedIndependently(t *testing.T) {
	orig := globalIPv6Probe
	t.Cleanup(func() { globalIPv6Probe = orig })

	globalIPv6Probe = func() bool { return true }
	res := DetectNATClass(context.Background(), []string{"only-one:3478"}, time.Second)
	require.True(t, res.DualStack, "dual-stack must be reported with <2 servers")
	require.Equal(t, NATUnknown, res.Class)

	globalIPv6Probe = func() bool { return false }
	res = DetectNATClass(context.Background(), []string{"only-one:3478"}, time.Second)
	require.False(t, res.DualStack)
}
