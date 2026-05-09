package onvif

// DiscoveredDevice represents an ONVIF device found via WS-Discovery.
type DiscoveredDevice struct {
	UUID     string   `json:"uuid"`
	Name     string   `json:"name"`
	XAddrs   []string `json:"xaddrs"`
	Scopes   []string `json:"scopes"`
	Hardware string   `json:"hardware"`
	Endpoint string   `json:"endpoint"`
}

// DeviceProfile represents a media profile from an ONVIF device.
type DeviceProfile struct {
	Token    string `json:"token"`
	Name     string `json:"name"`
	Encoding string `json:"encoding"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
}

// DeviceInfo holds basic device information.
type DeviceInfo struct {
	Manufacturer string `json:"manufacturer"`
	Model        string `json:"model"`
	Firmware     string `json:"firmware"`
	SerialNumber string `json:"serial_number"`
	HardwareID   string `json:"hardware_id"`
}

// DeviceCapabilities describes what an ONVIF device supports.
type DeviceCapabilities struct {
	PTZ       bool `json:"ptz"`
	Streaming bool `json:"streaming"`
}

// PTZVector represents a PTZ position or velocity.
type PTZVector struct {
	Pan  float64 `json:"pan"`
	Tilt float64 `json:"tilt"`
	Zoom float64 `json:"zoom"`
}

// StreamInfo holds the RTSP stream URL and metadata.
type StreamInfo struct {
	URI          string `json:"uri"`
	Protocol     string `json:"protocol"`
	Encoding     string `json:"encoding"`
	ProfileToken string `json:"profile_token"`
}
