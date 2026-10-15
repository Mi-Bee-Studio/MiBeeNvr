package config

// DoorbellConfig configures the optional SIP doorbell / intercom ingress
// (#971): standard SIP UA door stations (Doorbird / Akuvox / Hikvision
// intercoms) register (or reach us through a PBX) and INVITE on press; the
// NVR turns that into ring events, an answerable two-way G.711 talk bridge
// and an optional snapshot of the linked camera. Deliberately decoupled
// from gb28181 — no shared numbering, no shared config. Default off.
type DoorbellConfig struct {
	Enabled bool `yaml:"enabled"`

	// Listen is the SIP listener, ":5070" by default (ApplyDefaults fills).
	// Behind "lan_only" (default) the server refuses SIP from non-private
	// source addresses, so a port forward alone does not expose it.
	Listen string `yaml:"listen"`

	// Realm for digest challenges ("mibee-nvr").
	Realm string `yaml:"realm"`

	// Transport: "udp" (default), "tcp" or "tls" (needs tls_cert/tls_key).
	Transport string `yaml:"transport"`
	TLSCert   string `yaml:"tls_cert"`
	TLSKey    string `yaml:"tls_key"`

	// LANOnly (default true) rejects SIP whose source IP is not private —
	// the hard "never exposed to WAN by default" guarantee from #971.
	LANOnly *bool `yaml:"lan_only"`

	// RingTimeout bounds how long a call rings before auto-terminating
	// ("60s" default, ApplyDefaults fills).
	RingTimeout string `yaml:"ring_timeout"`

	// Users are the door stations allowed to register (mode A) or whose
	// From user is accepted from a trusted PBX (mode B). Secrets are
	// encrypted at rest (ENC: mechanism, same as mqtt.password).
	Users []DoorbellUserConfig `yaml:"users"`

	// Outbound enables mode B: the NVR registers as a UA to an external SIP
	// server (Asterisk / FreeSWITCH); doorbell INVITEs then arrive via the
	// PBX. nil = mode A only.
	Outbound *DoorbellOutboundConfig `yaml:"outbound"`
}

// DoorbellUserConfig is one door station account.
type DoorbellUserConfig struct {
	Username string `yaml:"username"`
	// Secret is the SIP digest password — SECRET, encrypted at rest via the
	// ENC: mechanism (see internal/config/crypto.go).
	Secret string `yaml:"secret"`
	// Name is a display name for the web popup ("Front door").
	Name string `yaml:"name"`
	// CameraID optionally links a camera that gets snapshotted on ring.
	CameraID string `yaml:"camera_id"`
}

// DoorbellOutboundConfig is mode B: register the NVR to a PBX.
type DoorbellOutboundConfig struct {
	// Server is the PBX address, "host:port" or "sip:host:port".
	Server string `yaml:"server"`
	// Username is the SIP user the PBX assigned to the NVR.
	Username string `yaml:"username"`
	// Secret is the digest password — SECRET, encrypted at rest.
	Secret  string `yaml:"secret"`
	Expires string `yaml:"expires"` // registration refresh, "300s" default
}

// LANOnlyEnabled resolves the tri-state (nil = true).
func (c DoorbellConfig) LANOnlyEnabled() bool {
	return c.LANOnly == nil || *c.LANOnly
}
