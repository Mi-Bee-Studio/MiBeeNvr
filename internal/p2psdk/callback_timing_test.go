package peer

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestOnDataChannel_AssignedAfterConnectStillFires guards the callback
// read-through semantics: OnDataChannel assigned AFTER Connect() returns but
// BEFORE the DataChannel opens must still fire. It used to be silently lost
// because Session.Connect snapshotted the (then nil) callback into the p2p
// session — the mibee-peer forward command was bitten by exactly this window.
//
// Determinism: the mock holds connect_accept delivery for connectAcceptDelay,
// so the DataChannel cannot open before that elapses; the test assigns the
// callback well inside the window.
func TestOnDataChannel_AssignedAfterConnectStillFires(t *testing.T) {
	mock := newMockServer(t)
	mock.connectAcceptDelay = 750 * time.Millisecond

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

	clientDCOpen := make(chan struct{}, 1)

	// Connect first, then assign the callback inside the accept-delay window.
	// SetOnDataChannel is the race-free way to assign once a session is in
	// flight; direct field assignment here would race with event dispatch.
	require.NoError(t, client.Connect(context.Background(), "device-1"))
	time.Sleep(250 * time.Millisecond) // < connectAcceptDelay
	client.SetOnDataChannel(func(DataChannel) { clientDCOpen <- struct{}{} })

	select {
	case <-deviceDCOpen:
	case <-time.After(120 * time.Second):
		t.Fatal("timeout waiting for device DataChannel")
	}
	select {
	case <-clientDCOpen:
	case <-time.After(120 * time.Second):
		t.Fatal("client OnDataChannel never fired: callback assigned after Connect was lost (snapshot regression)")
	}
}
