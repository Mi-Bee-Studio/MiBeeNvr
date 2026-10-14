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

// TestSession_OnReject_FiredOnConnectReject verifies that when the server
// returns a connect_reject (e.g. target not found or unauthorized), the
// caller's OnReject callback fires with the reject code and message. This is
// how a client learns its Connect attempt was refused.
func TestSession_OnReject_FiredOnConnectReject(t *testing.T) {
	mock := newMockServer(t)
	mock.rejectNextConnect(RejectCodeUnauthorized, "no such device")

	var gotCode RejectCode
	var gotMsg string
	var gotReject atomic.Int32
	var mu sync.Mutex

	s, err := Dial(context.Background(), SignalingConfig{
		ServerURL: mock.url,
		JWT:       "test-jwt",
		ProductID: "test-product",
		PeerID:    "client-1",
		Role:      RoleClient,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	s.OnReject = func(code RejectCode, msg string) {
		mu.Lock()
		gotCode = code
		gotMsg = msg
		mu.Unlock()
		gotReject.Add(1)
	}

	err = s.Connect(context.Background(), "nonexistent-device")
	require.NoError(t, err, "Connect sends the request and returns nil; rejection is async")

	require.Eventually(t, func() bool {
		return gotReject.Load() == 1
	}, 3*time.Second, 20*time.Millisecond, "OnReject should fire once after connect_reject")

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, RejectCodeUnauthorized, gotCode)
	assert.Equal(t, "no such device", gotMsg)
}

// TestSession_OnReject_EntitlementCode verifies the entitlement reject code
// is surfaced distinctly from unauthorized.
func TestSession_OnReject_EntitlementCode(t *testing.T) {
	mock := newMockServer(t)
	mock.rejectNextConnect(RejectCodeEntitlement, "plan limit reached")

	var gotCode RejectCode
	var fired atomic.Int32

	s, err := Dial(context.Background(), SignalingConfig{
		ServerURL: mock.url,
		JWT:       "test-jwt",
		ProductID: "test-product",
		PeerID:    "client-2",
		Role:      RoleClient,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	s.OnReject = func(code RejectCode, _ string) {
		gotCode = code
		fired.Add(1)
	}

	_ = s.Connect(context.Background(), "some-device")

	require.Eventually(t, func() bool { return fired.Load() == 1 },
		3*time.Second, 20*time.Millisecond)
	assert.Equal(t, RejectCodeEntitlement, gotCode)
}
