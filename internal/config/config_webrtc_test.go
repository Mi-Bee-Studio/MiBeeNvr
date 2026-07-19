package config

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestValidateWebRTCICEServers_Valid covers the happy paths for the new
// streaming.webrtc.ice_servers section: empty (LAN-only), STUN-only, TURN,
// and TURNS (TLS) are all accepted.
func TestValidateWebRTCICEServers_Valid(t *testing.T) {
	valid := []struct {
		name    string
		servers []ICEServerConfig
	}{
		{"empty (LAN-only)", nil},
		{"stun only", []ICEServerConfig{{URLs: []string{"stun:stun.l.google.com:19302"}}}},
		{"turn with creds", []ICEServerConfig{{
			URLs:       []string{"turn:turn.example.com:3478?transport=udp"},
			Username:   "user",
			Credential: "pass",
		}}},
		{"turns (TLS)", []ICEServerConfig{{URLs: []string{"turns:turn.example.com:5349"}}}},
		{"multiple", []ICEServerConfig{
			{URLs: []string{"stun:stun.l.google.com:19302"}},
			{URLs: []string{"turn:turn.example.com:3478"}, Username: "u", Credential: "p"},
		}},
	}
	for _, tc := range valid {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{Streaming: StreamingConfig{WebRTC: WebRTCConfig{ICEServers: tc.servers}}}
			cfg.ApplyDefaults()
			assert.NoError(t, Validate(cfg))
		})
	}
}

// TestValidateWebRTCICEServers_Invalid covers rejection cases.
func TestValidateWebRTCICEServers_Invalid(t *testing.T) {
	invalid := []struct {
		name    string
		servers []ICEServerConfig
		want    string
	}{
		{"empty URLs", []ICEServerConfig{{URLs: nil}}, "urls is required"},
		{"wrong scheme", []ICEServerConfig{{URLs: []string{"http://example.com"}}}, "must start with stun:/turn:/turns:"},
		{"empty string URL", []ICEServerConfig{{URLs: []string{""}}}, "must start with stun:/turn:/turns:"},
		{"stun with bad scheme mix", []ICEServerConfig{{
			URLs: []string{"stun:stun.l.google.com:19302", "ftp://bad"},
		}}, "must start with stun:/turn:/turns:"},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{Streaming: StreamingConfig{WebRTC: WebRTCConfig{ICEServers: tc.servers}}}
			cfg.ApplyDefaults()
			err := Validate(cfg)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// TestWebRTCICEServersDefaultsUnchanged verifies that leaving ice_servers empty
// (the documented LAN-only default) does not change legacy WebRTC config
// behavior — Enabled stays true, MaxViewers stays 2, IdleTimeout stays "60s".
func TestWebRTCICEServersDefaultsUnchanged(t *testing.T) {
	cfg := &Config{}
	cfg.ApplyDefaults()
	require.NotNil(t, cfg.Streaming.WebRTC.Enabled)
	assert.True(t, *cfg.Streaming.WebRTC.Enabled, "WebRTC should default to enabled")
	assert.Equal(t, 2, cfg.Streaming.WebRTC.MaxViewers)
	assert.Equal(t, "60s", cfg.Streaming.WebRTC.IdleTimeout)
	assert.Nil(t, cfg.Streaming.WebRTC.ICEServers, "ICE servers must default to nil (LAN-only)")
}

// TestWebRTCICEServersRoundTrip ensures ice_servers survives a YAML save+load
// cycle, so users can configure TURN credentials persistently.
func TestWebRTCICEServersRoundTrip(t *testing.T) {
	src := &Config{
		Storage: StorageConfig{RootDir: "/tmp/nvr"},
		Auth:    AuthConfig{Username: "u", PasswordHash: "$2a$10$x"},
		Streaming: StreamingConfig{
			DefaultProtocol: "hls",
			WebRTC: WebRTCConfig{
				ICEServers: []ICEServerConfig{
					{URLs: []string{"stun:stun.l.google.com:19302"}},
					{URLs: []string{"turn:turn.example.com:3478"}, Username: "u", Credential: "p"},
				},
			},
		},
	}
	src.ApplyDefaults()

	path := t.TempDir() + "/cfg.yaml"
	require.NoError(t, Save(path, src))
	loaded, err := Load(path)
	require.NoError(t, err)
	require.Len(t, loaded.Streaming.WebRTC.ICEServers, 2)
	assert.Equal(t, []string{"stun:stun.l.google.com:19302"}, loaded.Streaming.WebRTC.ICEServers[0].URLs)
	assert.Equal(t, "u", loaded.Streaming.WebRTC.ICEServers[1].Username)
	assert.Equal(t, "p", loaded.Streaming.WebRTC.ICEServers[1].Credential)

	// Sanity check Validate passes on the loaded config.
	assert.NoError(t, Validate(loaded))
}

// TestWebRTCICEServersOmittedFromYAMLWhenEmpty guards against accidentally
// serializing an empty ice_servers: [] key (keeps the example diff minimal).
func TestWebRTCICEServersOmittedFromYAMLWhenEmpty(t *testing.T) {
	cfg := &Config{Storage: StorageConfig{RootDir: "/tmp/nvr"}}
	cfg.ApplyDefaults()
	path := t.TempDir() + "/cfg.yaml"
	require.NoError(t, Save(path, cfg))

	b, err := os.ReadFile(path)
	require.NoError(t, err)
	raw := string(b)
	assert.False(t, strings.Contains(raw, "ice_servers"),
		"empty ice_servers must be omitted from YAML (omitempty), got:\n%s", raw)
}
