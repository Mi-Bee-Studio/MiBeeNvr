package peer

import (
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWrapDataChannel_BuffersEarlyMessages verifies that DataChannel messages
// arriving before the application registers OnMessage are buffered and
// replayed in order. Without buffering, tunnel frames sent by the remote peer
// immediately after channel open are silently dropped when tunnel.Serve()
// has not yet registered its handler — the remote never sees its OPEN frame.
func TestWrapDataChannel_BuffersEarlyMessages(t *testing.T) {
	pcOffer, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = pcOffer.Close() })

	pcAnswer, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = pcAnswer.Close() })

	// Mirror production wiring (p2p.go): wrap the channel immediately when
	// OnDataChannel fires, so the pion-level handler exists before any data
	// can arrive. The application-level handler is only registered later.
	wrappedCh := make(chan DataChannel, 1)
	pcAnswer.OnDataChannel(func(dc *webrtc.DataChannel) {
		wrappedCh <- wrapDataChannel(dc)
	})

	dc, err := pcOffer.CreateDataChannel("t", nil)
	require.NoError(t, err)
	dcOpened := make(chan struct{})
	dc.OnOpen(func() { close(dcOpened) })

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

	<-dcOpened
	wrapped := <-wrappedCh

	// Send before the wrapped channel has a handler, then give the messages
	// time to arrive so the buffering path (not just live delivery) is
	// exercised.
	require.NoError(t, dc.Send([]byte("early-1")))
	require.NoError(t, dc.Send([]byte("early-2")))
	time.Sleep(200 * time.Millisecond)

	got := make(chan string, 4)
	wrapped.OnMessage(func(data []byte) { got <- string(data) })

	require.NoError(t, dc.Send([]byte("late-3")))

	deadline := time.After(5 * time.Second)
	var received []string
	for len(received) < 3 {
		select {
		case m := <-got:
			received = append(received, m)
		case <-deadline:
			t.Fatalf("timed out with %d/3 messages: %v", len(received), received)
		}
	}
	assert.Equal(t, []string{"early-1", "early-2", "late-3"}, received,
		"buffered messages must be replayed in arrival order before live ones")
}
