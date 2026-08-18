package config

// GB28181CascadeConfig configures the GB/T 28181 cascade client: the NVR as
// a LOWER-LEVEL platform registering to an upper-level platform, aggregating
// its cameras as a catalog, and forwarding media on the upper platform's
// INVITEs (internal/gb28181/cascade). Independent of the platform-role
// server config (GB28181ServerConfig); both can run simultaneously.
type GB28181CascadeConfig struct {
	Enabled bool `yaml:"enabled"` // default false

	// ServerDomain is the upper platform's 20-digit GB ID.
	ServerDomain string `yaml:"server_domain"`

	// ServerAddr is the upper platform's SIP address, "host:port".
	ServerAddr string `yaml:"server_addr"`

	// LocalDeviceID is THIS NVR's 20-digit GB device ID as seen by the
	// upper platform.
	LocalDeviceID string `yaml:"local_device_id"`

	// Realm is the digest realm the upper platform challenges with.
	Realm string `yaml:"realm"`

	// Password is the SIP digest secret. SECRET — encrypted via
	// encrypt-config like gb28181.password.
	Password string `yaml:"password"`

	// SIPListen is the cascade client's own SIP UDP port (the platform-role
	// server owns 5060; the cascade dialogs use this one). Default ":5061".
	SIPListen string `yaml:"sip_listen"`

	// HeartbeatInterval is the Keepalive MESSAGE cadence. Default "60s".
	HeartbeatInterval string `yaml:"heartbeat_interval"`

	// RegisterExpires is the REGISTER lifetime in seconds (re-registered at
	// 80%). Default 3600.
	RegisterExpires int `yaml:"register_expires"`

	// Upstreams declares ADDITIONAL upper platforms for multi-upstream
	// cascade (#370): each entry runs its own REGISTER/keepalive session over
	// the shared SIP listener, with independent online state. Fields left
	// empty fall back to the single-form values above. The single form itself
	// (ServerAddr non-empty) remains the first upstream.
	Upstreams []GB28181CascadeUpstream `yaml:"upstreams,omitempty"`
}

// GB28181CascadeUpstream is one upper-platform entry of a multi-upstream
// cascade (#370).
type GB28181CascadeUpstream struct {
	// ServerDomain is the upper platform's 20-digit GB ID.
	ServerDomain string `yaml:"server_domain"`

	// ServerAddr is the upper platform's SIP address, "host:port".
	ServerAddr string `yaml:"server_addr"`

	// LocalDeviceID is this NVR's device ID AT THAT platform — uppers may
	// assign different IDs. Empty = the single-form LocalDeviceID.
	LocalDeviceID string `yaml:"local_device_id,omitempty"`

	// Realm is the digest realm. Empty = the single-form Realm.
	Realm string `yaml:"realm,omitempty"`

	// Password is the digest secret. Empty = the single-form Password.
	Password string `yaml:"password,omitempty"`

	// HeartbeatInterval overrides the keepalive cadence for this upstream.
	HeartbeatInterval string `yaml:"heartbeat_interval,omitempty"`

	// RegisterExpires overrides the REGISTER lifetime for this upstream.
	RegisterExpires int `yaml:"register_expires,omitempty"`
}
