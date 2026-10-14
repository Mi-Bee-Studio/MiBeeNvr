package peer

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOnConnectRequest_GateRejectsBeforeWebRTC verifies the device-side
// admission gate: a device whose OnConnectRequest returns false answers the
// inbound connect_request with connect_reject (code device_busy) instead of
// starting the WebRTC offerer dance, and the requester's OnReject fires with
// that code.
func TestOnConnectRequest_GateRejectsBeforeWebRTC(t *testing.T) {
	mock := newMockServer(t)

	var gateCalls atomic.Int32
	dev, err := Dial(context.Background(), SignalingConfig{
		ServerURL: mock.url,
		JWT:       "test-jwt",
		ProductID: "test-product",
		PeerID:    "device-1",
		Role:      RoleDevice,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = dev.Close() })
	dev.SetOnDataChannel(func(DataChannel) {}) // keep assign-under-lock style consistent
	dev.OnConnectRequest = func(IncomingConnect) bool {
		gateCalls.Add(1)
		return false
	}

	var gotCode RejectCode
	var gotMsg string
	var rejected atomic.Int32
	var mu sync.Mutex
	cli, err := Dial(context.Background(), SignalingConfig{
		ServerURL: mock.url,
		JWT:       "test-jwt",
		ProductID: "test-product",
		PeerID:    "client-1",
		Role:      RoleClient,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = cli.Close() })
	cli.OnReject = func(code RejectCode, msg string) {
		mu.Lock()
		gotCode, gotMsg = code, msg
		mu.Unlock()
		rejected.Add(1)
	}

	require.NoError(t, cli.Connect(context.Background(), "device-1"))

	require.Eventually(t, func() bool { return rejected.Load() == 1 },
		5*time.Second, 20*time.Millisecond, "client OnReject should fire once")
	require.Eventually(t, func() bool { return gateCalls.Load() == 1 },
		5*time.Second, 20*time.Millisecond, "device gate should have run once")

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, RejectCodeDeviceBusy, gotCode)
	assert.NotEmpty(t, gotMsg, "reject carries a human-readable message")
	// No WebRTC machinery started: the device never created a p2p session.
	assert.Equal(t, 0, dev.ActiveSessions(), "rejected request must not create a P2P session")
}

// TestOnConnectRequest_NilGateAccepts pins the backward-compatibility
// contract: without a gate, inbound connect_requests are accepted exactly as
// before (the device session appears in the tracking map).
func TestOnConnectRequest_NilGateAccepts(t *testing.T) {
	mock := newMockServer(t)

	dev, err := Dial(context.Background(), SignalingConfig{
		ServerURL: mock.url,
		JWT:       "test-jwt",
		ProductID: "test-product",
		PeerID:    "device-1",
		Role:      RoleDevice,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = dev.Close() })
	assert.Nil(t, dev.connectRequestCallback(), "no gate assigned by default")

	cli, err := Dial(context.Background(), SignalingConfig{
		ServerURL: mock.url,
		JWT:       "test-jwt",
		ProductID: "test-product",
		PeerID:    "client-1",
		Role:      RoleClient,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = cli.Close() })

	require.NoError(t, cli.Connect(context.Background(), "device-1"))

	// The mock forwards the request; the device creates the offerer session.
	require.Eventually(t, func() bool { return dev.ActiveSessions() == 1 },
		5*time.Second, 20*time.Millisecond, "nil gate must keep accepting inbound requests")
}

// TestCloseInfo_UnwrapsWrappedCloseError verifies CloseInfo sees through the
// %w wrapping Dial/OnDisconnect apply, and reports ok=false for plain errors.
func TestCloseInfo_UnwrapsWrappedCloseError(t *testing.T) {
	inner := &websocket.CloseError{
		Code: websocket.ClosePolicyViolation,
		Text: "license_expired",
	}
	wrapped := errors.Join(errors.New("read welcome: "), inner)

	code, reason, ok := CloseInfo(wrapped)
	require.True(t, ok)
	assert.Equal(t, 1008, code) // ClosePolicyViolation == 1008
	assert.Equal(t, "license_expired", reason)

	code, reason, ok = CloseInfo(errors.New("dial tcp: connection refused"))
	assert.False(t, ok)
	assert.Zero(t, code)
	assert.Empty(t, reason)

	code, _, ok = CloseInfo(nil)
	assert.False(t, ok)
	assert.Zero(t, code)
}
