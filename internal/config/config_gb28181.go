package config

import "time"

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

	// SubChannelProbe gates the sub-channel prober (#560): "auto" (default —
	// probe only devices whose DeviceInfo Manufacturer matches a known
	// offset-convention vendor), "on" (probe every device), "off" (never).
	// A probe is one extra INVITE per channel per process lifetime; failures
	// are silent (no camera error state — the camera keeps its no-sub-stream
	// degradation path).
	SubChannelProbe string `yaml:"sub_channel_probe,omitempty"` // default "auto"

	// SubChannelProbeOffset is the channel-code numeric offset the prober
	// applies to a main channel code to derive the sub-channel candidate
	// (Hikvision convention: +1). 0 disables probing regardless of
	// SubChannelProbe. Range 1–99.
	SubChannelProbeOffset int `yaml:"sub_channel_probe_offset,omitempty"` // default 1

	// TCPMode forces TCP media transport (passive).
	//
	// Deprecated: superseded by MediaTransport — no longer influences the
	// default (which is now "tcp-passive", #460) because an explicit
	// `tcp_mode: false` is indistinguishable from unset. Kept as a YAML-compat
	// no-op; set media_transport explicitly instead.
	TCPMode bool `yaml:"tcp_mode"`

	// TCPFraming selects the TCP-passive framing: "rfc4571" (2-byte length
	// prefix), "0x24" (RTSP-interleaved), or "auto" (detect from first bytes).
	TCPFraming string `yaml:"tcp_framing"` // default "auto"

	// MediaTransport selects the RTP media transport for INVITE sessions:
	// "tcp-passive" (default — NVR listens, device connects; UDP measured ~16%
	// frame loss on real GB cameras, #460), "udp", or "tcp-active" (NVR dials
	// the device's answer address). Signaling transport is independent
	// (SIPTransport).
	MediaTransport string `yaml:"media_transport"` // default "tcp-passive"

	// SIPTransport selects the SIP signaling listener: "udp" (default) or
	// "tcp". "tcp" adds a SIP-over-TCP listener alongside UDP — devices pick
	// whichever they speak.
	SIPTransport string `yaml:"sip_transport"` // default "udp"

	// SubscribeCatalog enables SUBSCRIBE Catalog: devices push channel-list
	// changes via NOTIFY instead of waiting for the periodic catalog poll
	// (GB/T 28181-2016 § 9.5.1). Default: on when gb28181.enabled (nil =
	// unset; explicit false opts out).
	SubscribeCatalog *bool `yaml:"subscribe_catalog,omitempty"`

	// SubscribeAlarm enables SUBSCRIBE Alarm: device alarm notifications
	// (motion, offline, ...) arrive as NOTIFY and surface on the event bus
	// (gb28181.alarm topic, SSE) plus the REST ring. Default: on when
	// gb28181.enabled.
	SubscribeAlarm *bool `yaml:"subscribe_alarm,omitempty"`

	// SubscribeMobilePosition enables SUBSCRIBE MobilePosition for moving
	// devices (vehicle-mounted cameras); reports land in the REST ring
	// (/api/gb28181/devices/{id}/positions). Default false — stationary
	// cameras never emit reports.
	SubscribeMobilePosition bool `yaml:"subscribe_mobile_position"` // default false

	// SubscribeExpires is the SUBSCRIBE lifetime; renewed at 80%.
	SubscribeExpires string `yaml:"subscribe_expires"` // default "3600s"

	// AlarmLinkage configures alarm-triggered streaming (#355): on an alarm
	// notification, INVITE the alarm channel when it is not already streaming,
	// hold the stream for the configured duration, then BYE — unless the
	// channel's camera wants recording (then the stream stays).
	AlarmLinkage *GB28181AlarmLinkageConfig `yaml:"alarm_linkage,omitempty"`
}

// GB28181AlarmLinkageConfig is the alarm→stream linkage block.
type GB28181AlarmLinkageConfig struct {
	Enabled bool `yaml:"enabled"` // default false

	// Duration of each alarm-triggered stream hold. Default "60s".
	Duration string `yaml:"duration"`
}

// AlarmLinkageDuration resolves the hold duration with its default.
func (c *GB28181AlarmLinkageConfig) AlarmLinkageDuration() time.Duration {
	if c == nil {
		return 0
	}
	if d, err := time.ParseDuration(c.Duration); err == nil && d > 0 {
		return d
	}
	return 60 * time.Second
}

// CatalogSubscriptionOn resolves the subscribe_catalog toggle: on when the
// server is enabled and the flag is unset or true.
func (c GB28181ServerConfig) CatalogSubscriptionOn() bool {
	if c.SubscribeCatalog == nil {
		return c.Enabled
	}
	return *c.SubscribeCatalog
}

// AlarmSubscriptionOn resolves the subscribe_alarm toggle (see CatalogSubscriptionOn).
func (c GB28181ServerConfig) AlarmSubscriptionOn() bool {
	if c.SubscribeAlarm == nil {
		return c.Enabled
	}
	return *c.SubscribeAlarm
}

// GB28181ChannelConfig holds per-camera GB28181 fields (used when the camera
// protocol is "gb28181"). DeviceID/ChannelID map a SIP device+channel to this
// camera; the NVR INVITEs the channel and ingests its RTP/PS stream (no URL).
type GB28181ChannelConfig struct {
	DeviceID     string `yaml:"device_id,omitempty" json:"device_id,omitempty"`   // GB 20-digit device code
	ChannelID    string `yaml:"channel_id,omitempty" json:"channel_id,omitempty"` // GB 20-digit channel code
	Manufacturer string `yaml:"manufacturer,omitempty" json:"manufacturer,omitempty"`
	// SubChannelID is the probed sub-stream channel code (#560): the vendor-
	// convention offset of ChannelID (Hikvision: channel number +1) that the
	// on-demand sub-stream puller INVITEs for quality=sub. Auto-populated by
	// the probe (fill-once — an existing value is never overwritten, clearing
	// it re-arms probing). Empty = no sub stream (negotiation falls back to
	// main).
	SubChannelID string `yaml:"sub_channel_id,omitempty" json:"sub_channel_id,omitempty"`
}
