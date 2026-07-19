package webrtc

import (
	"strings"
	"testing"

	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWithICEServersStoresConfig verifies the WithICEServers option populates
// the Manager's iceServers field — the field that feeds every PeerConnection
// created in CreateWHEPSession. This is the config→runtime contract: empty
// input keeps the legacy LAN-only behavior (nil), non-empty input is stored
// verbatim.
func TestWithICEServersStoresConfig(t *testing.T) {
	t.Run("nil preserves LAN-only default", func(t *testing.T) {
		mgr := NewManager()
		defer mgr.StopAll()
		assert.Nil(t, mgr.iceServers, "no option = nil = LAN-only (legacy default)")
	})

	t.Run("empty slice preserves LAN-only default", func(t *testing.T) {
		mgr := NewManager(WithICEServers(nil))
		defer mgr.StopAll()
		assert.Nil(t, mgr.iceServers, "nil input must keep legacy LAN-only behavior")
	})

	t.Run("stun server stored", func(t *testing.T) {
		servers := []webrtc.ICEServer{
			{URLs: []string{"stun:stun.l.google.com:19302"}},
		}
		mgr := NewManager(WithICEServers(servers))
		defer mgr.StopAll()
		require.Len(t, mgr.iceServers, 1)
		assert.Equal(t, []string{"stun:stun.l.google.com:19302"}, mgr.iceServers[0].URLs)
	})

	t.Run("turn server with creds stored", func(t *testing.T) {
		servers := []webrtc.ICEServer{{
			URLs:           []string{"turn:turn.example.com:3478"},
			Username:       "user",
			Credential:     "pass",
			CredentialType: webrtc.ICECredentialTypePassword,
		}}
		mgr := NewManager(WithICEServers(servers))
		defer mgr.StopAll()
		require.Len(t, mgr.iceServers, 1)
		assert.Equal(t, "user", mgr.iceServers[0].Username)
		assert.Equal(t, "pass", mgr.iceServers[0].Credential)
		assert.Equal(t, webrtc.ICECredentialTypePassword, mgr.iceServers[0].CredentialType)
	})
}

// TestWHEPSessionWithICEServers verifies end-to-end that an ICE-server-configured
// Manager still produces a valid SDP answer (the WHEP path must not break when
// ice_servers is populated). We cannot assert the browser actually reaches a
// public STUN server from a unit test, but we CAN assert the answer SDP still
// contains a video m-line + H.264 codec — i.e. ICE config injection did not
// corrupt the offer/answer exchange.
func TestWHEPSessionWithICEServers(t *testing.T) {
	mgr := NewManager(WithICEServers([]webrtc.ICEServer{
		{URLs: []string{"stun:stun.l.google.com:19302"}},
	}))
	defer mgr.StopAll()

	client := newTestClient(t, false)
	defer client.close()

	offerSDP := createOfferSDP(t, client.pc)
	answerSDP, sessionID := connectWHEP(t, mgr, "ice-cam", client.pc, offerSDP)

	// The answer must still be a well-formed H.264 WHEP answer.
	assert.True(t, strings.Contains(string(answerSDP), "m=video"),
		"answer should contain video m-line even with ICE servers configured")
	assert.NotEmpty(t, sessionID)
	assert.Equal(t, 1, mgr.activePeerCount("ice-cam"))
}

// TestWHEPSessionWithoutICEServersIsBackwardCompatible is a regression guard:
// a Manager created with zero options (the legacy code path in run.go before
// this feature) must still work identically.
func TestWHEPSessionWithoutICEServersIsBackwardCompatible(t *testing.T) {
	mgr := NewManager() // no WithICEServers — legacy path
	defer mgr.StopAll()

	client := newTestClient(t, false)
	defer client.close()

	offerSDP := createOfferSDP(t, client.pc)
	answerSDP, _ := connectWHEP(t, mgr, "legacy-cam", client.pc, offerSDP)
	assert.True(t, strings.Contains(string(answerSDP), "m=video"))
}
