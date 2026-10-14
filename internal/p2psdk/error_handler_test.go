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

// TestSession_OnError_FiredOnServerErrorMessage verifies that when the server
// sends a protocol-level error message, the caller's OnError callback fires
// with the error code and message. Previously server errors were only logged,
// leaving callers unable to react to protocol-level rejections.
func TestSession_OnError_FiredOnServerErrorMessage(t *testing.T) {
	mock := newMockServer(t)

	var gotCode, gotMsg string
	var fired atomic.Int32
	var mu sync.Mutex

	s, err := Dial(context.Background(), SignalingConfig{
		ServerURL: mock.url,
		JWT:       "test-jwt",
		ProductID: "test-product",
		PeerID:    "device-err",
		Role:      RoleDevice,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	s.OnError = func(code, msg string) {
		mu.Lock()
		gotCode = code
		gotMsg = msg
		mu.Unlock()
		fired.Add(1)
	}

	// Have the mock push a server error to this peer.
	mock.sendError("device-err", "rate_limited", "too many requests")

	require.Eventually(t, func() bool { return fired.Load() == 1 },
		3*time.Second, 20*time.Millisecond, "OnError should fire on server error message")

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, "rate_limited", gotCode)
	assert.Equal(t, "too many requests", gotMsg)
}
