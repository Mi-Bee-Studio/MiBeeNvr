package peer

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// forgetSessions wipes the mock's resume window, mirroring a server restart:
// ladder resume attempts fail and the client must full-handshake again.
func forgetSessions(mock *mockSignalServer) {
	mock.mu.Lock()
	for k := range mock.sessionToPeer {
		delete(mock.sessionToPeer, k)
	}
	mock.mu.Unlock()
}

// TestSDK_SignalLadderTokenFuncRemints reproduces the 2026-10-14 production
// outage scenario: a device stays connected past its token TTL, then a server
// restart wipes the session — resume fails and the next full hello must
// authenticate. A static-JWT ladder replays the dead token forever; with
// TokenFunc every full handshake carries a fresh mint and the ladder
// recovers on its own.
func TestSDK_SignalLadderTokenFuncRemints(t *testing.T) {
	mock := newMockServer(t)

	var armed atomic.Bool
	var mints atomic.Int32
	mock.mu.Lock()
	mock.helloGate = func(jwt string) bool {
		// Pre-drop everything passes; post-drop only re-mints do.
		return !armed.Load() || jwt != "jwt-1"
	}
	mock.mu.Unlock()

	reconnected := make(chan struct{}, 1)
	device, err := Dial(context.Background(), SignalingConfig{
		ServerURL: mock.url,
		ProductID: "test-product",
		PeerID:    "device-1",
		Role:      "device",
		TokenFunc: func() (string, error) {
			n := mints.Add(1)
			return fmt.Sprintf("jwt-%d", n), nil
		},
		OnReconnect: func() {
			select {
			case reconnected <- struct{}{}:
			default:
			}
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = device.Close() })
	require.EqualValues(t, 1, mints.Load(), "initial connect must mint exactly once")

	armed.Store(true)
	forgetSessions(mock)
	mock.dropPeer("device-1")

	select {
	case <-reconnected:
	case <-time.After(15 * time.Second):
		t.Fatal("ladder did not recover via TokenFunc re-mint")
	}
	assert.GreaterOrEqual(t, mints.Load(), int32(2), "ladder must have re-minted")
}

// TestSDK_SignalLadderAbandonsOnAuthRejectionWithoutTokenFunc pins the
// escalation half: with a static JWT and an auth-class rejection the ladder
// must give up after ONE attempt (surfacing the failure to the session
// owner's watchdog) instead of retrying the dead token forever.
func TestSDK_SignalLadderAbandonsOnAuthRejectionWithoutTokenFunc(t *testing.T) {
	mock := newMockServer(t)

	var armed atomic.Bool
	mock.mu.Lock()
	mock.helloGate = func(jwt string) bool { return !armed.Load() }
	mock.mu.Unlock()

	reconnected := make(chan struct{}, 1)
	device, err := Dial(context.Background(), SignalingConfig{
		ServerURL: mock.url,
		JWT:       "expired-static-jwt",
		ProductID: "test-product",
		PeerID:    "device-1",
		Role:      "device",
		OnReconnect: func() {
			select {
			case reconnected <- struct{}{}:
			default:
			}
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = device.Close() })

	armed.Store(true)
	forgetSessions(mock)
	mock.dropPeer("device-1")

	// Attempt 1 lands ~1s after the drop and must be the last one; a
	// would-be attempt 2 (~3s) proves the ladder is still spinning.
	time.Sleep(5 * time.Second)
	select {
	case <-reconnected:
		t.Fatal("static JWT must not reconnect after auth-class rejection")
	default:
	}
	mock.mu.RLock()
	hellos := mock.hellos
	mock.mu.RUnlock()
	assert.Equal(t, 2, hellos, "exactly one ladder hello, then abandonment")
}
