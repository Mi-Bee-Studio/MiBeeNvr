package config

// GB28181ServerConfig configures the GB/T 28181 platform server. When Enabled,
// compliant devices (Hikvision, Dahua, Uniview, ...) REGISTER over SIP and
// expose their channels as MiBee cameras; the NVR INVITEs a channel and ingests
// its RTP/PS stream. See internal/gb28181/ for the SIP + media implementation.
type GB28181ServerConfig struct {
	Enabled bool `yaml:"enabled"` // default false

	// SIPListen is the SIP UDP/TCP listen address, e.g. ":5060".
	SIPListen string `yaml:"sip_listen"` // default ":5060"

	// ServerID is the platform GB 20-digit serial (e.g. "34020000002000000001").
	ServerID string `yaml:"server_id"`

	// Realm is the SIP digest-auth realm presented in REGISTER challenges.
	Realm string `yaml:"realm"`

	// Password is the SIP digest-auth secret that registered devices must use.
	// SECRET — encrypted via encrypt-config like mqtt.password.
	Password string `yaml:"password"`

	// PortRange is the RTP media port pool, "start-end". Default "30000-30050".
	PortRange string `yaml:"port_range"`

	// AllowedDeviceIDs restricts which devices may register. Empty = allow any.
	AllowedDeviceIDs []string `yaml:"allowed_device_ids,omitempty"`

	// HeartbeatInterval is how often devices are expected to send Keepalive.
	HeartbeatInterval string `yaml:"heartbeat_interval"` // default "60s"

	// CatalogInterval is how often the platform refreshes the device catalog.
	CatalogInterval string `yaml:"catalog_interval"` // default "30m"

	// TCPMode forces TCP media transport (passive). Default false (UDP).
	//
	// Deprecated: superseded by MediaTransport — kept as a YAML-compat
	// alias (true → "tcp-passive").
	TCPMode bool `yaml:"tcp_mode"`

	// TCPFraming selects the TCP-passive framing: "rfc4571" (2-byte length
	// prefix), "0x24" (RTSP-interleaved), or "auto" (detect from first bytes).
	TCPFraming string `yaml:"tcp_framing"` // default "auto"

	// MediaTransport selects the RTP media transport for INVITE sessions:
	// "udp" (default), "tcp-passive" (NVR listens, device connects — the
	// Hikvision/Dahua default), or "tcp-active" (NVR dials the device's
	// answer address). Signaling transport is independent (SIPTransport).
	MediaTransport string `yaml:"media_transport"` // default "udp"

	// SIPTransport selects the SIP signaling listener: "udp" (default) or
	// "tcp". "tcp" adds a SIP-over-TCP listener alongside UDP — devices pick
	// whichever they speak.
	SIPTransport string `yaml:"sip_transport"` // default "udp"
}

// GB28181ChannelConfig holds per-camera GB28181 fields (used when the camera
// protocol is "gb28181"). DeviceID/ChannelID map a SIP device+channel to this
// camera; the NVR INVITEs the channel and ingests its RTP/PS stream (no URL).
type GB28181ChannelConfig struct {
	DeviceID     string `yaml:"device_id,omitempty" json:"device_id,omitempty"`   // GB 20-digit device code
	ChannelID    string `yaml:"channel_id,omitempty" json:"channel_id,omitempty"` // GB 20-digit channel code
	Manufacturer string `yaml:"manufacturer,omitempty" json:"manufacturer,omitempty"`
}
