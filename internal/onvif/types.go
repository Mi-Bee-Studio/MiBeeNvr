package onvif


import "time"

// DiscoveredDevice represents an ONVIF device found via WS-Discovery.
type DiscoveredDevice struct {
	UUID     string   `json:"uuid"`
	Name     string   `json:"name"`
	XAddrs   []string `json:"xaddrs"`
	Scopes   []string `json:"scopes"`
	Hardware string   `json:"hardware"`
	Endpoint string   `json:"endpoint"`
}

// DiscoveryError represents a categorized error from ONVIF device discovery.
type DiscoveryError struct {
	Category string `json:"category"` // NETWORK, TIMEOUT, NO_DEVICES, PARSE_ERROR
	Message  string `json:"message"`
}

// DiscoveryResult holds the outcome of an ONVIF discovery operation.
// Devices is always non-nil (empty slice when no devices found).
// Error is nil on success, non-nil when a categorized error occurred.
type DiscoveryResult struct {
	Devices []DiscoveredDevice `json:"devices"`
	Error   *DiscoveryError     `json:"error"`
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

// ImagingSettings represents camera imaging parameters.
type ImagingSettings struct {
	Brightness  float64           `json:"brightness"`
	Contrast    float64           `json:"contrast"`
	Saturation  float64           `json:"saturation"`
	Sharpness   float64           `json:"sharpness"`
	Exposure    ExposureSettings  `json:"exposure"`
	WhiteBalance WhiteBalanceSettings `json:"white_balance"`
}

// ExposureSettings represents exposure configuration.
type ExposureSettings struct {
	Mode         string  `json:"mode"`          // "auto" or "manual"
	ExposureTime float64 `json:"exposure_time"`
	Gain         float64 `json:"gain"`
}

// WhiteBalanceSettings represents white balance configuration.
type WhiteBalanceSettings struct {
	Mode              string  `json:"mode"`               // "auto" or "manual"
	ColorTemperature float64 `json:"color_temperature"`
}

// ImagingOptions represents supported ranges for imaging parameters.
type ImagingOptions struct {
	Brightness  *Range `json:"brightness,omitempty"`
	Contrast    *Range `json:"contrast,omitempty"`
	Saturation  *Range `json:"saturation,omitempty"`
	Sharpness   *Range `json:"sharpness,omitempty"`
	Exposure    *ExposureOptions  `json:"exposure,omitempty"`
	WhiteBalance *WhiteBalanceOptions `json:"white_balance,omitempty"`
}

// Range represents a min/max range for a numeric parameter.
type Range struct {
	Min float64 `json:"min"`
	Max float64 `json:"max"`
}

// ExposureOptions represents supported exposure ranges.
type ExposureOptions struct {
	Mode         []string `json:"modes"`
	ExposureTime *Range   `json:"exposure_time,omitempty"`
	Gain         *Range   `json:"gain,omitempty"`
}

// WhiteBalanceOptions represents supported white balance ranges.
type WhiteBalanceOptions struct {
	Mode              []string `json:"modes"`
	ColorTemperature *Range    `json:"color_temperature,omitempty"`
}

// PTZPreset represents a saved PTZ position.
type PTZPreset struct {
	Token    string   `json:"token"`
	Name     string   `json:"name"`
	Position PTZVector `json:"position"`
}

// ONVIFEvent represents an event from an ONVIF device.
type ONVIFEvent struct {
	Topic     string         `json:"topic"`
	Timestamp time.Time      `json:"timestamp"`
	Data      map[string]any `json:"data"`
	CameraID  string         `json:"camera_id"`
}

// NetworkInterface represents network configuration for an ONVIF device.
type NetworkInterface struct {
	Name      string            `json:"name"`
	Enabled   bool              `json:"enabled"`
	IPv4      NetworkIPv4       `json:"ipv4"`
	IPv6      NetworkIPv6       `json:"ipv6,omitempty"`
	DNS       []string          `json:"dns,omitempty"`
	NTP       NetworkNTP        `json:"ntp,omitempty"`
}

// NetworkIPv4 represents IPv4 network configuration.
type NetworkIPv4 struct {
	Enabled  bool   `json:"enabled"`
	DHCP     bool   `json:"dhcp"`
	Address  string `json:"address,omitempty"`
	Netmask  string `json:"netmask,omitempty"`
	Gateway  string `json:"gateway,omitempty"`
}

// NetworkIPv6 represents IPv6 network configuration.
type NetworkIPv6 struct {
	Enabled  bool   `json:"enabled"`
	DHCP     bool   `json:"dhcp"`
	Address  string `json:"address,omitempty"`
	Prefix   int    `json:"prefix,omitempty"`
	Gateway  string `json:"gateway,omitempty"`
}

// NetworkNTP represents NTP server configuration.
type NetworkNTP struct {
	Manual []string `json:"manual,omitempty"`
	DHCP   bool     `json:"dhcp"`
}

// ONVIFUser represents an ONVIF device user account.
type ONVIFUser struct {
	Username string `json:"username"`
	Password string `json:"password,omitempty"`
	Level    string `json:"level"` // "Administrator", "Operator", "User", "Anonymous"
}

// DeviceCapabilitiesDetailed extends DeviceCapabilities with per-service capability details.
type DeviceCapabilitiesDetailed struct {
	PTZ       bool `json:"ptz"`
	Imaging   bool `json:"imaging"`
	Events    bool `json:"events"`
	Snapshot  bool `json:"snapshot"`
	Streaming bool `json:"streaming"`
	Device    bool `json:"device"` // Device management (reboot, network, users)
}
