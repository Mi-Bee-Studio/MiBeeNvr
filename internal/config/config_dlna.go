package config

// DLNAConfig configures the optional UPnP AV / DLNA media server (#970):
// TVs and media players on the LAN discover the NVR via SSDP and browse
// cameras → days → recordings. Default off; the HTTP/SSDP surface binds only
// to the configured (or auto-detected LAN) interfaces and never crosses
// relay/p2p tunnels.
type DLNAConfig struct {
	Enabled bool `yaml:"enabled"`

	// Port is the per-interface HTTP port for the device description, SOAP
	// control and item URLs. 0 → 49152 (ApplyDefaults fills it).
	Port int `yaml:"port"`

	// Interfaces restricts SSDP announcements + HTTP listeners to the named
	// interfaces. Empty = every non-loopback IPv4 interface that is up and
	// multicast-capable.
	Interfaces []string `yaml:"interfaces"`

	// FriendlyName is the server name shown on renderers. Empty → the
	// server.device_name, falling back to "MiBee NVR".
	FriendlyName string `yaml:"friendly_name"`
}
