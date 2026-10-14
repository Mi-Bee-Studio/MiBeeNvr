package peer

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSDK_KeepaliveSendsPings verifies the signaling client emits app-level
// pings periodically (the server echoes pong, keeping both idle timers fed).
func TestSDK_KeepaliveSendsPings(t *testing.T) {
	orig := currentKeepaliveInterval()
	setKeepaliveInterval(80 * time.Millisecond)
	t.Cleanup(func() { setKeepaliveInterval(orig) })

	mock := newMockServer(t)
	sess, err := Dial(context.Background(), SignalingConfig{
		ServerURL: mock.url,
		JWT:       "test-jwt",
		ProductID: "test-product",
		PeerID:    "ka-1",
		Role:      "device",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sess.Close() })

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if mock.pingCount("ka-1") >= 2 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("expected >=2 keepalive pings within 3s, got %d", mock.pingCount("ka-1"))
}

// TestSDK_ReconnectFallsBackToHelloWhenResumeRejected verifies that when the
// server no longer knows the session (resume window expired / single-use
// entry consumed), the client redials and completes a full hello handshake
// instead of retrying the doomed resume forever.
func TestSDK_ReconnectFallsBackToHelloWhenResumeRejected(t *testing.T) {
	mock := newMockServer(t)
	sess, err := Dial(context.Background(), SignalingConfig{
		ServerURL: mock.url,
		JWT:       "test-jwt",
		ProductID: "test-product",
		PeerID:    "fb-1",
		Role:      "device",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sess.Close() })

	sess.signal.connMu.Lock()
	oldConn := sess.signal.conn
	oldSession := sess.signal.lastSessionID
	sess.signal.connMu.Unlock()
	require.NotNil(t, oldConn)
	require.NotEmpty(t, oldSession)

	// Make resume impossible: the mock forgets the session, so it closes the
	// connection on the resume attempt.
	mock.forgetSession(oldSession)
	mock.dropPeer("fb-1")

	reconnected := false
	for range 150 { // up to 15s — attempt 1 fires after 1s, resume read timeout 2s
		sess.signal.connMu.Lock()
		conn := sess.signal.conn
		sess.signal.connMu.Unlock()
		if conn != nil && conn != oldConn {
			reconnected = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	require.True(t, reconnected, "client should reconnect via full hello after rejected resume")

	sess.signal.connMu.Lock()
	newSession := sess.signal.lastSessionID
	sess.signal.connMu.Unlock()
	assert.NotEqual(t, oldSession, newSession, "fallback handshake should mint a fresh session id")
}
