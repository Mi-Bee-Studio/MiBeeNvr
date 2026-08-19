package config

// Ingest server configuration (FTP, WebDAV, SRT, RTMP).

type FTPConfig struct {
	Enabled          *bool  `yaml:"enabled"`            // default true
	Port             int    `yaml:"port"`               // default 2121
	PassivePortRange string `yaml:"passive_port_range"` // default "2122-2140"
}

type WebDAVConfig struct {
	Enabled    *bool  `yaml:"enabled"`     // default true
	PathPrefix string `yaml:"path_prefix"` // default "/dav"
	ReadWrite  bool   `yaml:"read_write"`  // default false
}

// SRTConfig configures the SRT listener for receiving MPEG-TS streams.
type SRTConfig struct {
	Enabled *bool       `yaml:"enabled"` // default false
	Port    int         `yaml:"port"`    // default 9000
	Streams []SRTStream `yaml:"streams"`
}

// SRTStream configures a single SRT stream mapping.
type SRTStream struct {
	CameraID   string `yaml:"camera_id" json:"camera_id"`
	Mode       string `yaml:"mode" json:"mode"`             // "listener" (receive pushes) or "caller" (pull from remote)
	Address    string `yaml:"address" json:"address"`       // For caller mode: remote SRT address (e.g. "192.168.1.100:9000")
	Passphrase string `yaml:"passphrase" json:"passphrase"` // AES encryption passphrase (optional)
	StreamID   string `yaml:"stream_id" json:"stream_id"`   // SRT stream ID for caller mode (optional)
}

// RTMPConfig configures the RTMP ingest server.
type RTMPConfig struct {
	Enabled    *bool             `yaml:"enabled"`     // default false
	Port       int               `yaml:"port"`        // default 1935
	StreamKeys map[string]string `yaml:"stream_keys"` // camera_id → stream_key
}

// WHIPConfig configures the WHIP (WebRTC-HTTP Ingest Protocol) push-in
// endpoint (#369): browsers (getUserMedia) and OBS 30+ push H.264+Opus over
// WebRTC. No port of its own — it rides the main HTTP listener. The stream
// key configured per camera (protocol "whip") is the credential.
type WHIPConfig struct {
	Enabled *bool `yaml:"enabled"` // default false
}
