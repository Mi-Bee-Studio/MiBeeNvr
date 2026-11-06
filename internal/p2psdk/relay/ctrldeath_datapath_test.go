package relay

// #117 endgame: does a dead ctrl connection (#88②) impair the data plane?
// The 2026-11-03 incident had a device whose ctrl leg departed at the exact
// breakpoint while its data leg stayed registered and ponging — if data
// still flows with a dead ctrl leg (as designed), ctrl death alone cannot
// explain the black hole and the stale-key theory stands.

import (
	"context"
	"encoding/hex"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCtrlLegDeathDoesNotImpairDataPath: with dual connections established,
// the server drops client B's ctrl connection; A↔B tunnel data must keep
// flowing in both directions on the data legs.
func TestCtrlLegDeathDoesNotImpairDataPath(t *testing.T) {
	mock := newMockRelayServer(t)
	mock.caps = 0x04 // FrameCapDualConn bit (#88②)
	pubKeyHex := hex.EncodeToString(mock.pubKey[:])

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	a := NewClient()
	require.NoError(t, a.Connect(ctx, mock.url, pubKeyHex))
	defer func() { _ = a.Close() }()
	b := NewClient()
	require.NoError(t, b.Connect(ctx, mock.url, pubKeyHex))
	defer func() { _ = b.Close() }()

	// Both dial their ctrl links after connect (caps advertised).
	require.Eventually(t, func() bool {
		mock.ctrlMu.Lock()
		defer mock.ctrlMu.Unlock()
		return mock.ctrlConns[a.PubKey()] != nil && mock.ctrlConns[b.PubKey()] != nil
	}, 5*time.Second, 20*time.Millisecond, "both ctrl links must establish")

	a.SetRemotePeer(b.PubKey())
	b.SetRemotePeer(a.PubKey())

	receivedB := make(chan []byte, 4)
	b.OnMessage(func(data []byte) { receivedB <- data })
	receivedA := make(chan []byte, 4)
	a.OnMessage(func(data []byte) { receivedA <- data })

	// Baseline: data flows with both legs alive.
	require.NoError(t, a.Send([]byte("baseline")))
	select {
	case msg := <-receivedB:
		assert.Equal(t, "baseline", string(msg))
	case <-time.After(5 * time.Second):
		t.Fatal("baseline: B never received")
	}

	// Server drops B's ctrl connection (asymmetric leg loss).
	mock.ctrlMu.Lock()
	ctrlB := mock.ctrlConns[b.PubKey()]
	mock.ctrlMu.Unlock()
	require.NotNil(t, ctrlB)
	_ = ctrlB.Close()

	// B's SDK degrades to single-connection; give it a beat.
	time.Sleep(100 * time.Millisecond)

	// Data plane must be unaffected in BOTH directions.
	require.NoError(t, a.Send([]byte("after-ctrl-death-a-to-b")))
	select {
	case msg := <-receivedB:
		assert.Equal(t, "after-ctrl-death-a-to-b", string(msg))
	case <-time.After(5 * time.Second):
		t.Fatal("after ctrl death: B never received — data plane broken by ctrl loss")
	}
	require.NoError(t, b.Send([]byte("after-ctrl-death-b-to-a")))
	select {
	case msg := <-receivedA:
		assert.Equal(t, "after-ctrl-death-b-to-a", string(msg))
	case <-time.After(5 * time.Second):
		t.Fatal("after ctrl death: A never received — device-side egress broken by ctrl loss")
	}
}
