package config

// Streaming protocol configuration (HLS, WebRTC, FLV).

type HLSConfig struct {
	WriteBufferSize  int    `yaml:"write_buffer_size"`   // async frame buffer per stream (default 100)
	SegmentMaxSizeMB int    `yaml:"segment_max_size_mb"` // HLS segment max size in MB (default 10)
	SegmentCount     int    `yaml:"segment_count"`       // HLS segment count per stream (default 7, range [3,10])
	MaxStreams       int    `yaml:"max_streams"`         // default 4 (RPi constraint)
	LowLatency       bool   `yaml:"low_latency"`         // enable Low-Latency HLS (gohlslib MuxerVariantLowLatency)
	PartMinDuration  string `yaml:"part_min_duration"`   // LL-HLS partial segment duration (default "200ms", range [100ms-1s])
}

// StreamingConfig configures streaming protocol options (WebRTC, FLV, etc.)
//
// NOTE: a global default_protocol field was removed — the frontend Player
// Orchestrator now auto-selects the best protocol per camera (probes
// /api/cameras/{id}/protocols, folds in codec + browser capability, demotes
// on health failure). A stale default_protocol key in existing YAML is
// silently ignored (unknown fields are not strict-decoded). Per-camera
// overrides remain available via the Protocol Switcher on each camera's
// LiveView page.
type StreamingConfig struct {
	WebRTC WebRTCConfig `yaml:"webrtc"`
	FLV    FLVConfig    `yaml:"flv"`
}

// WebRTCConfig configures WebRTC WHEP streaming
type WebRTCConfig struct {
	Enabled     *bool             `yaml:"enabled"`               // default true
	MaxViewers  int               `yaml:"max_viewers"`           // default 2, range [1,10]
	IdleTimeout string            `yaml:"idle_timeout"`          // default "60s"
	ICEServers  []ICEServerConfig `yaml:"ice_servers,omitempty"` // STUN/TURN for cross-network access; empty = LAN-only (default)
}

// ICEServerConfig describes a single STUN/TURN server used for cross-network
// (WAN/4G/remote WiFi) WebRTC access. Leave streaming.webrtc.ice_servers empty
// for LAN-only deployments — this preserves the legacy behavior.
type ICEServerConfig struct {
	URLs       []string `yaml:"urls"`                 // required, e.g. ["stun:stun.l.google.com:19302"] or ["turn:turn.example.com:3478?transport=udp"]
	Username   string   `yaml:"username,omitempty"`   // TURN only
	Credential string   `yaml:"credential,omitempty"` // TURN only
}

// FLVConfig configures HTTP-FLV streaming
type FLVConfig struct {
	Enabled      *bool  `yaml:"enabled"`        // default true
	MaxViewers   int    `yaml:"max_viewers"`    // default 10, range [1,50]
	IdleTimeout  string `yaml:"idle_timeout"`   // default "60s"
	GOPCacheSize int    `yaml:"gop_cache_size"` // default 1
}
