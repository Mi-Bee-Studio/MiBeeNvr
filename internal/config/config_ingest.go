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
	CameraID   string `yaml:"camera_id"`
	Mode       string `yaml:"mode"`       // "listener" (receive pushes) or "caller" (pull from remote)
	Address    string `yaml:"address"`    // For caller mode: remote SRT address (e.g. "192.168.1.100:9000")
	Passphrase string `yaml:"passphrase"` // AES encryption passphrase (optional)
	StreamID   string `yaml:"stream_id"`  // SRT stream ID for caller mode (optional)
}

// RTMPConfig configures the RTMP ingest server.
type RTMPConfig struct {
	Enabled    *bool             `yaml:"enabled"`     // default false
	Port       int               `yaml:"port"`        // default 1935
	StreamKeys map[string]string `yaml:"stream_keys"` // camera_id → stream_key
}
