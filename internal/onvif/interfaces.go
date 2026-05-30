package onvif

import (
	"context"
	"time"
)

// Discoverer discovers ONVIF devices on the network.
type Discoverer interface {
	Discover(ctx context.Context, timeout time.Duration) ([]DiscoveredDevice, error)
	ProbeDevice(ctx context.Context, host string, port int, timeout time.Duration) (*DiscoveredDevice, error)
}

// DeviceClient connects to and queries an ONVIF device.
type DeviceClient interface {
	Connect(ctx context.Context) error
	GetDeviceInformation(ctx context.Context) (*DeviceInfo, error)
	GetProfiles(ctx context.Context) ([]DeviceProfile, error)
	GetStreamURI(ctx context.Context, profileToken string) (*StreamInfo, error)
	GetCapabilities(ctx context.Context) (*DeviceCapabilities, error)
}

// PTZController controls PTZ movement on an ONVIF device.
type PTZController interface {
	ContinuousMove(ctx context.Context, velocity PTZVector) error
	AbsoluteMove(ctx context.Context, position PTZVector) error
	RelativeMove(ctx context.Context, displacement PTZVector) error
	Stop(ctx context.Context, stopPanTilt, stopZoom bool) error
	GetStatus(ctx context.Context) (position PTZVector, moving bool, err error)
	GetPresets(ctx context.Context) ([]PTZPreset, error)
	SetPreset(ctx context.Context, name string) (string, error)
	GoToPreset(ctx context.Context, token string) error
	RemovePreset(ctx context.Context, token string) error
}

// ImagingController controls imaging parameters on an ONVIF device.
type ImagingController interface {
	GetImagingSettings(ctx context.Context) (*ImagingSettings, error)
	SetImagingSettings(ctx context.Context, settings ImagingSettings) error
	GetImagingOptions(ctx context.Context) (*ImagingOptions, error)
}

// PresetManager manages PTZ presets on an ONVIF device.
type PresetManager interface {
	GetPresets(ctx context.Context) ([]PTZPreset, error)
	SetPreset(ctx context.Context, name string) (string, error)
	GoToPreset(ctx context.Context, token string) error
	RemovePreset(ctx context.Context, token string) error
}

// EventSubscriber subscribes to events from an ONVIF device.
type EventSubscriber interface {
	Subscribe(ctx context.Context, cameraID string) error
	Unsubscribe(ctx context.Context, cameraID string) error
	GetEventMessages(ctx context.Context) ([]ONVIFEvent, error)
}

// DeviceManager manages device-level operations on an ONVIF device.
type DeviceManager interface {
	SystemReboot(ctx context.Context) error
	GetNetworkInterfaces(ctx context.Context) ([]NetworkInterface, error)
	SetNetworkInterfaces(ctx context.Context, interfaces []NetworkInterface) error
	GetUsers(ctx context.Context) ([]ONVIFUser, error)
	CreateUsers(ctx context.Context, users []ONVIFUser) error
	DeleteUsers(ctx context.Context, usernames []string) error
	SetUser(ctx context.Context, username, password string) error
}

// SnapshotProvider provides snapshot URI from an ONVIF device.
type SnapshotProvider interface {
	GetSnapshotUri(ctx context.Context) (string, error)
}
