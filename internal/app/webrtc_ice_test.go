package app

import (
	"testing"

	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
)

// TestWebRTCICEServersConversion verifies the config-layer → pion-layer
// conversion in run.go:webrtcICEServers. This is the boundary where
// streaming.webrtc.ice_servers (user-facing YAML) becomes the runtime ICE
// config that pion hands to each PeerConnection.
func TestWebRTCICEServersConversion(t *testing.T) {
	t.Run("empty input returns nil (LAN-only legacy default)", func(t *testing.T) {
		// Critical contract: nil return lets WithICEServers fall through to the
		// legacy LAN-only path — no behavior change for users who don't set
		// ice_servers.
		assert.Nil(t, webrtcICEServers(nil))
		assert.Nil(t, webrtcICEServers([]config.ICEServerConfig{}))
	})

	t.Run("stun-only server (no creds)", func(t *testing.T) {
		in := []config.ICEServerConfig{
			{URLs: []string{"stun:stun.l.google.com:19302"}},
		}
		out := webrtcICEServers(in)
		require.Len(t, out, 1)
		assert.Equal(t, []string{"stun:stun.l.google.com:19302"}, out[0].URLs)
		assert.Empty(t, out[0].Username, "STUN must not set credentials")
		assert.Equal(t, webrtc.ICECredentialType(0), out[0].CredentialType,
			"STUN must leave CredentialType zero (unspecified)")
	})

	t.Run("turn server gets password credential type", func(t *testing.T) {
		in := []config.ICEServerConfig{{
			URLs:       []string{"turn:turn.example.com:3478"},
			Username:   "u",
			Credential: "p",
		}}
		out := webrtcICEServers(in)
		require.Len(t, out, 1)
		assert.Equal(t, "u", out[0].Username)
		assert.Equal(t, "p", out[0].Credential)
		assert.Equal(t, webrtc.ICECredentialTypePassword, out[0].CredentialType,
			"TURN with username+password must set CredentialType=Password")
	})

	t.Run("mixed stun+turn list preserved in order", func(t *testing.T) {
		in := []config.ICEServerConfig{
			{URLs: []string{"stun:stun.l.google.com:19302"}},
			{URLs: []string{"turn:turn.example.com:3478"}, Username: "u", Credential: "p"},
			{URLs: []string{"turns:turn.example.com:5349"}, Username: "u2", Credential: "p2"},
		}
		out := webrtcICEServers(in)
		require.Len(t, out, 3)
		assert.Empty(t, out[0].Username)
		assert.Equal(t, "u", out[1].Username)
		assert.Equal(t, "u2", out[2].Username)
	})

	t.Run("turn with empty username but non-empty credential ignored", func(t *testing.T) {
		// Guards the `if s.Username != ""` branch — a half-filled TURN entry
		// (credential without username) is treated as STUN-like (no creds),
		// because pion would otherwise reject a TURN request with empty user.
		in := []config.ICEServerConfig{
			{URLs: []string{"turn:turn.example.com:3478"}, Credential: "p"}, // no Username
		}
		out := webrtcICEServers(in)
		require.Len(t, out, 1)
		assert.Empty(t, out[0].Username)
		assert.Equal(t, webrtc.ICECredentialType(0), out[0].CredentialType)
	})
}
