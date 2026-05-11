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
}
