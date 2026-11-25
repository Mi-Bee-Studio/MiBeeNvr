package peer

// Caps advertising : the server treats a peer with no caps as
// capability-unknown and anchors cross-node relay sessions on that side.
// These tests pin the wire contract so the device stops being invisible to
// the capability-gated assigner.

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHelloAdvertisesRelayRepoint(t *testing.T) {
	// The token is additive: a server that predates capability advertisement ignores the field.
	h := Hello{ProductID: "p", PeerID: "d", Role: "device", JWT: "j"}
	h.Caps = ClientCapabilities()
	raw, err := json.Marshal(h)
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))
	caps, ok := decoded["caps"].([]any)
	require.True(t, ok, "hello must carry a caps array, got %s", raw)
	require.Len(t, caps, 1)
	require.Equal(t, "relay_repoint", caps[0])
}

func TestSyncStateEchoesRelayRepoint(t *testing.T) {
	s := SyncState{SessionID: "s", LastSeenSeq: 7}
	s.Caps = ClientCapabilities()
	raw, err := json.Marshal(s)
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))
	caps, ok := decoded["caps"].([]any)
	require.True(t, ok, "resume must echo caps so the capability plane survives a resume")
	require.Len(t, caps, 1)
	require.Equal(t, "relay_repoint", caps[0])
}

func TestClientCapabilitiesIsStableSingleton(t *testing.T) {
	// A returned slice that callers can mutate would let one session's
	// accidental append silently change every later session's advertisement.
	a := ClientCapabilities()
	a[0] = "corrupted"
	require.Equal(t, []string{"relay_repoint"}, ClientCapabilities(),
		"callers must not be able to mutate the shared capability list")
}
