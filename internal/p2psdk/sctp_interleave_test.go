package peer

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/require"
)

// TestSCTPInterleaveCtrlNotBlockedByBulk (#104) pins RFC 8260 I-DATA
// interleaving: while a single large message is being fragmented and sent
// on one data channel, a small message on a second channel must get through
// without waiting for that message to finish. Without interleaved DATA the
// whole fragment run of a message occupies consecutive TSNs, so the ctrl
// message waits behind one complete bulk message — the assertion is
// order-based (how many bulk messages completed before the first ctrl ping
// landed), not timing-based, so it stays stable on fast loopback.
func TestSCTPInterleaveCtrlNotBlockedByBulk(t *testing.T) {
	// Raise the negotiated max message size so bulk messages are far above
	// the 64 KiB default and force real multi-fragment sends. The receive
	// buffer must exceed one full bulk message, otherwise the receiver's
	// reassembly queue exhausts the advertised window mid-message and the
	// transfer deadlocks (classic SCTP large-message stall).
	se := webrtc.SettingEngine{}
	se.SetSCTPMaxMessageSize(16 * 1024 * 1024)
	se.SetSCTPMaxReceiveBufferSize(16 * 1024 * 1024)
	api := webrtc.NewAPI(webrtc.WithSettingEngine(se))

	pcOffer, err := api.NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = pcOffer.Close() })

	pcAnswer, err := api.NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = pcAnswer.Close() })

	const bulkMessages = 8 // x 8 MiB
	bulkPayload := make([]byte, 8*1024*1024)

	bulkOpened := make(chan struct{})
	bulk, err := pcOffer.CreateDataChannel("bulk", nil)
	require.NoError(t, err)
	bulk.OnOpen(func() { close(bulkOpened) })

	ctrlOpened := make(chan struct{})
	ctrl, err := pcOffer.CreateDataChannel("ctrl", nil)
	require.NoError(t, err)
	ctrl.OnOpen(func() { close(ctrlOpened) })

	// Receiver: snapshot the number of completed bulk messages at the
	// moment the first ctrl ping lands, plus the final count.
	var bulkReceived atomic.Int64
	ctrlObserved := make(chan int64, 1)
	pcAnswer.OnDataChannel(func(dc *webrtc.DataChannel) {
		switch dc.Label() {
		case "ctrl":
			dc.OnMessage(func(webrtc.DataChannelMessage) {
				select {
				case ctrlObserved <- bulkReceived.Load():
				default:
				}
			})
		case "bulk":
			dc.OnMessage(func(webrtc.DataChannelMessage) { bulkReceived.Add(1) })
		}
	})

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

	<-bulkOpened
	<-ctrlOpened

	// Queue the bulk backlog first, then the pings — worst case for FIFO
	// ordering, best case for interleaving.
	for range bulkMessages {
		require.NoError(t, bulk.Send(bulkPayload))
	}
	for range 4 {
		require.NoError(t, ctrl.Send([]byte("ping")))
	}

	var bulkDoneAtFirstCtrl int64
	select {
	case bulkDoneAtFirstCtrl = <-ctrlObserved:
	case <-time.After(30 * time.Second):
		t.Fatal("ctrl ping never arrived")
	}
	require.EqualValues(t, 0, bulkDoneAtFirstCtrl,
		"first ctrl ping waited for %d complete bulk message(s); I-DATA interleaving not active", bulkDoneAtFirstCtrl)

	// The bulk backlog must still drain completely.
	deadline := time.Now().Add(60 * time.Second)
	for bulkReceived.Load() < bulkMessages {
		if time.Now().After(deadline) {
			t.Fatalf("receiver got %d/%d bulk messages", bulkReceived.Load(), bulkMessages)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
