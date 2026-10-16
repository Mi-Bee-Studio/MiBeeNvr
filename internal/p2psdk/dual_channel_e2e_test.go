package peer

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func timeoutCh(d time.Duration) <-chan time.Time { return time.After(d) }

func timeSecond() time.Duration { return time.Second }
func timeMilli() time.Duration  { return time.Millisecond }

// TestDualChannelEndToEnd (#88①): with both sides on the new SDK, the
// connect_request declares ctrl_channel, the device-side offerer opens
// p2p-ctrl alongside p2p-data, and BOTH sides' application callbacks fire
// exactly once — for the data channel — with the ctrl channel paired via
// DataChannel.Ctrl (never delivered to the application directly).
func TestDualChannelEndToEnd(t *testing.T) {
	mock := newMockServer(t)

	device, err := Dial(context.Background(), SignalingConfig{
		ServerURL: mock.url,
		JWT:       "test-device-jwt",
		ProductID: "test-product",
		PeerID:    "device-1",
		Role:      "device",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = device.Close() })

	deviceDC := make(chan DataChannel, 4)
	device.OnDataChannel = func(dc DataChannel) { deviceDC <- dc }

	client, err := Dial(context.Background(), SignalingConfig{
		ServerURL: mock.url,
		JWT:       "test-client-jwt",
		ProductID: "test-product",
		PeerID:    "client-1",
		Role:      "client",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	clientDC := make(chan DataChannel, 4)
	client.SetOnDataChannel(func(dc DataChannel) { clientDC <- dc })

	require.NoError(t, client.Connect(context.Background(), "device-1"))

	// Device side: exactly one callback, paired ctrl.
	select {
	case dc := <-deviceDC:
		require.Equal(t, DataChannelLabel, dc.Label)
		require.NotNil(t, dc.Ctrl, "device side must see the paired ctrl channel")
		require.Equal(t, DataChannelLabelCtrl, dc.Ctrl.Label)
	case <-timeoutCh(15 * timeSecond()):
		t.Fatal("device never received the data channel")
	}
	select {
	case dc := <-deviceDC:
		t.Fatalf("unexpected second application callback, label=%s", dc.Label)
	case <-timeoutCh(300 * timeMilli()):
	}

	// Client side: exactly one callback, paired ctrl.
	select {
	case dc := <-clientDC:
		require.Equal(t, DataChannelLabel, dc.Label)
		require.NotNil(t, dc.Ctrl, "client side must see the paired ctrl channel")
	case <-timeoutCh(15 * timeSecond()):
		t.Fatal("client never received the data channel")
	}
	select {
	case dc := <-clientDC:
		t.Fatalf("unexpected second application callback, label=%s", dc.Label)
	case <-timeoutCh(300 * timeMilli()):
	}
}
