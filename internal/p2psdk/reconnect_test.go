package peer

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSDK_SignalReconnectPreservesPeers verifies that when the signaling
// WebSocket connection drops and reconnects:
//
//  1. Existing p2pSession entries in Session.peers are preserved
//     (they live on the Session struct, not on the signalingClient,
//     so reconnecting the signal connection is safe by design).
//
//  2. New connect requests can still be sent and delivered after reconnect.
//
//  3. The p2pSession map is not destroyed by the reconnect cycle.
//
//nolint:funlen // test function
func TestSDK_SignalReconnectPreservesPeers(t *testing.T) {
	mock := newMockServer(t)

	// ── Connect device session ──────────────────────────────────────────
	device, err := Dial(context.Background(), SignalingConfig{
		ServerURL: mock.url,
		JWT:       "test-device-jwt",
		ProductID: "test-product",
		PeerID:    "device-1",
		Role:      "device",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = device.Close() })

	// ── Manually add a p2pSession to peers map as if one existed ────────
	fakeP2P := &p2pSession{
		sessionID:    "test-session-id",
		remotePeerID: "client-1",
	}
	device.mu.Lock()
	device.peers["test-session-id"] = fakeP2P
	peersBefore := len(device.peers)
	device.mu.Unlock()
	require.Equal(t, 1, peersBefore, "should have one fake p2pSession")

	// ── Capture the pre-reconnect signaling conn for comparison ─────────
	device.signal.connMu.Lock()
	oldConn := device.signal.conn
	device.signal.connMu.Unlock()
	require.NotNil(t, oldConn)

	// ── Simulate signal connection drop ──────────────────────────────
	// Close the server-side WebSocket so the client's recvLoop detects
	// the error and triggers reconnectWithBackoff.
	mock.mu.RLock()
	serverConn, ok := mock.peers["device-1"]
	mock.mu.RUnlock()
	require.True(t, ok, "device should be registered in mock server")
	require.NotNil(t, serverConn)
	_ = serverConn.Close()

	// ── Wait for reconnect (poll until conn is re-established) ──────────
	reconnected := false
	for range 100 {
		device.signal.connMu.Lock()
		conn := device.signal.conn
		device.signal.connMu.Unlock()
		if conn != nil && conn != oldConn {
			reconnected = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	require.True(t, reconnected, "signal client did not reconnect within 10s")
	t.Log("signal reconnection successful")

	// ── Verify peers map is preserved after reconnect ──────────────────
	device.mu.RLock()
	if assert.Len(t, device.peers, 1,
		"peers map should preserve existing p2pSessions after signal reconnect") {
		assert.Equal(t, "test-session-id", device.peers["test-session-id"].sessionID,
			"sessionID should be preserved")
		assert.Equal(t, "client-1", device.peers["test-session-id"].remotePeerID,
			"remotePeerID should be preserved")
	}
	device.mu.RUnlock()
	t.Log("peers map verification: PASS")

	// ── Verify new connect requests work after reconnect ────────────────
	// Connect a client peer and initiate a connect request to device-1.
	client, err := Dial(context.Background(), SignalingConfig{
		ServerURL: mock.url,
		JWT:       "test-client-jwt",
		ProductID: "test-product",
		PeerID:    "client-2",
		Role:      "client",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	// The device's OnDataChannel will fire if the full P2P flow completes.
	// We also watch for the device peers map to confirm the connect_request
	// was received and a p2pSession was created on the device side.

	// Send connect request.
	err = client.Connect(context.Background(), "device-1")
	require.NoError(t, err)
	t.Log("connect_request sent after reconnect")

	// Wait for the device to process the request and add a new p2pSession.
	// The existing fake session should persist alongside the new one.
	var peersAfterConnect int
	gotRequest := false
	for range 100 {
		device.mu.RLock()
		peersAfterConnect = len(device.peers)
		device.mu.RUnlock()

		// We expect 2 entries: the fake one + the new connect_request one.
		if peersAfterConnect == 2 {
			gotRequest = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	assert.True(t, gotRequest,
		"device should have received connect_request (expected 2 peers, got %d)", peersAfterConnect)
	t.Log("connect_request delivery after reconnect: PASS")

	// ── Verify the original fake session is still intact alongside new one ─
	device.mu.RLock()
	assert.Equal(t, "test-session-id", device.peers["test-session-id"].sessionID,
		"original fake session should remain intact")
	assert.Equal(t, "client-1", device.peers["test-session-id"].remotePeerID,
		"original fake session remotePeerID should remain intact")
	device.mu.RUnlock()
	t.Log("all acceptance criteria met: peers preserved, reconnect works, new requests deliverable")
}
