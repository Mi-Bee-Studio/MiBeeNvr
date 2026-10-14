package peer

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSession_OnDisconnect_FiredOnServerClose verifies that OnDisconnect is
// invoked when the signaling WebSocket link drops, with a non-nil error
// describing the cause. This is the contract callers rely on to detect link
// loss without polling.
func TestSession_OnDisconnect_FiredOnServerClose(t *testing.T) {
	mock := newMockServer(t)
	var disconnectCount atomic.Int32
	var mu sync.Mutex
	var disconnectErr error

	s, err := Dial(context.Background(), SignalingConfig{
		ServerURL: mock.url,
		JWT:       "test-jwt",
		ProductID: "test-product",
		PeerID:    "device-1",
		Role:      RoleDevice,
		OnDisconnect: func(err error) {
			disconnectCount.Add(1)
			mu.Lock()
			disconnectErr = err
			mu.Unlock()
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	// Force the server side to drop our connection.
	mock.dropPeer("device-1")

	require.Eventually(t, func() bool {
		return disconnectCount.Load() >= 1
	}, 5*time.Second, 50*time.Millisecond, "OnDisconnect should fire after server drops the link")

	mu.Lock()
	defer mu.Unlock()
	assert.NotNil(t, disconnectErr, "OnDisconnect error should be non-nil")
}

// TestSession_OnReconnect_FiredAfterRecovery verifies that OnReconnect fires
// once the signaling client successfully re-establishes the WebSocket link
// after a drop. Together with OnDisconnect this gives callers a full
// link-health signal.
func TestSession_OnReconnect_FiredAfterRecovery(t *testing.T) {
	mock := newMockServer(t)
	var reconnectCount atomic.Int32

	s, err := Dial(context.Background(), SignalingConfig{
		ServerURL: mock.url,
		JWT:       "test-jwt",
		ProductID: "test-product",
		PeerID:    "device-2",
		Role:      RoleDevice,
		OnReconnect: func() {
			reconnectCount.Add(1)
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	// Drop the link; the client should reconnect automatically.
	mock.dropPeer("device-2")

	require.Eventually(t, func() bool {
		return reconnectCount.Load() >= 1
	}, 10*time.Second, 100*time.Millisecond,
		"OnReconnect should fire after the client re-establishes the link")
}
