package peer

// #117 asymmetric leg loss: a FramePeerGone carrying the session's OWN
// relay key is the server's self-notification that the sibling (data/ctrl)
// registration departed. The session must respond by rebuilding the relay
// client — not by tearing down victim sessions (its own key binds none).

import (
	"bytes"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/p2psdk/relay"
)

func TestHandleRelayPeerGone_OwnKeyTriggersRelayRebuild(t *testing.T) {
	buf := &bytes.Buffer{}
	s := &Session{
		logger:  slog.New(slog.NewTextHandler(buf, nil)),
		peers:   make(map[string]*p2pSession),
		welcome: &Welcome{}, // empty RelayURL → the async rebuild is a no-op
		closeCh: make(chan struct{}),
		done:    make(chan struct{}),
	}
	rc := relay.NewClient()
	require.NotEqual(t, [32]byte{}, rc.PubKey(), "identity keys are minted eagerly")
	s.relayClient = rc

	var stranger [32]byte
	stranger[0] = 0xEE
	nobodyTornDown := true
	s.OnSessionClose = func(_, _ string) { nobodyTornDown = false }

	// The server's self-notification: own key + any reason byte.
	s.handleRelayPeerGone(rc.PubKey(), relay.PeerGoneConnLost)

	// The WARN fires synchronously; the rebuild goroutine defers (empty
	// welcome makes it a no-op) — give it a beat to settle.
	time.Sleep(50 * time.Millisecond)
	assert.Contains(t, buf.String(), "own sibling leg departed", "self-notify must be logged as a rebuild trigger")
	assert.True(t, nobodyTornDown, "own key binds no victim sessions — nothing to tear down")

	// Sanity: a stranger's key still takes the victim path (no rebuild WARN).
	buf.Reset()
	s.handleRelayPeerGone(stranger, relay.PeerGoneConnLost)
	time.Sleep(20 * time.Millisecond)
	assert.NotContains(t, buf.String(), "own sibling leg departed")
}
