package peer

// Empirical pin (2026-10-20 fleet leak hunt, ROOT CAUSE): when the REMOTE
// peer closes its PeerConnection GRACEFULLY (its SDK's session teardown —
// DTLS close_notify), pion cascades the SURVIVING side's PeerConnection to
// PeerConnectionStateClosed without any local Close() call. The SDK used
// to ignore that signal: the session lingered registered until the
// 15-minute idle sweep while its stats collector self-stopped against the
// closed PC (the fleet's "agent is closed" storm). watchPCClosed now turns
// this transition into an immediate session teardown — this test pins the
// cascade behavior that fix relies on.

import (
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/require"
)

func TestRemoteGracefulCloseCascadesToLocalPCClosed(t *testing.T) {
	pcOffer, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	pcAnswer, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)

	dc, err := pcOffer.CreateDataChannel("p2p-data", nil)
	require.NoError(t, err)
	opened := make(chan struct{})
	dc.OnOpen(func() { close(opened) })

	offer, err := pcOffer.CreateOffer(nil)
	require.NoError(t, err)
	require.NoError(t, pcOffer.SetLocalDescription(offer))
	<-webrtc.GatheringCompletePromise(pcOffer)
	require.NoError(t, pcAnswer.SetRemoteDescription(*pcOffer.LocalDescription()))

	answer, err := pcAnswer.CreateAnswer(nil)
	require.NoError(t, err)
	require.NoError(t, pcAnswer.SetLocalDescription(answer))
	<-webrtc.GatheringCompletePromise(pcAnswer)
	require.NoError(t, pcOffer.SetRemoteDescription(*pcAnswer.LocalDescription()))

	select {
	case <-opened:
	case <-time.After(5 * time.Second):
		t.Fatal("DataChannel never opened")
	}

	// The remote closes gracefully (its SDK's teardown — close_notify on
	// the wire). The survivor NEVER calls Close, yet its PC must report
	// Closed shortly after: the cascade the session teardown now rides.
	require.NoError(t, pcOffer.Close())

	deadline := time.Now().Add(8 * time.Second)
	for pcAnswer.ConnectionState() != webrtc.PeerConnectionStateClosed {
		if time.Now().After(deadline) {
			t.Fatalf("surviving PC state = %s, want Closed (remote graceful-close cascade)", pcAnswer.ConnectionState())
		}
		time.Sleep(100 * time.Millisecond)
	}
}
