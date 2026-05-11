package onvif

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	onvifgo "github.com/0x524a/onvif-go"
)

var logger = slog.Default().With("component", "onvif-client")

// Client wraps an onvif-go Client for ONVIF device operations.
type Client struct {
	endpoint string
	username string
	password string
	client   *onvifgo.Client
	mu       sync.Mutex
	ready    bool
}

// NewClient creates a new ONVIF client for a specific device.
// Call Connect() before using device operations.
func NewClient(endpoint, username, password string) *Client {
	return &Client{
		endpoint: endpoint,
		username: username,
		password: password,
	}
}

// Connect initializes the ONVIF connection and discovers service endpoints.
func (c *Client) Connect(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	onvifClient, err := onvifgo.NewClient(c.endpoint, onvifgo.WithCredentials(c.username, c.password))
	if err != nil {
		return fmt.Errorf("create ONVIF client: %w", err)
	}

	if err := onvifClient.Initialize(ctx); err != nil {
		return fmt.Errorf("initialize ONVIF client: %w", err)
	}

	c.client = onvifClient
	c.ready = true
	logger.Info("connected to ONVIF device", "endpoint", c.endpoint)
	return nil
}

// GetDeviceInformation retrieves device info (manufacturer, model, firmware).
func (c *Client) GetDeviceInformation(ctx context.Context) (*DeviceInfo, error) {
	if !c.ready {
		return nil, fmt.Errorf("onvif client not connected, call Connect() first")
	}

	info, err := c.client.GetDeviceInformation(ctx)
	if err != nil {
		return nil, fmt.Errorf("get device information: %w", err)
	}

	return mapDeviceInfo(info), nil
}

// GetProfiles retrieves media profiles from the device.
func (c *Client) GetProfiles(ctx context.Context) ([]DeviceProfile, error) {
	if !c.ready {
		return nil, fmt.Errorf("onvif client not connected, call Connect() first")
	}

	profiles, err := c.client.GetProfiles(ctx)
	if err != nil {
		return nil, fmt.Errorf("get profiles: %w", err)
	}

	result := make([]DeviceProfile, 0, len(profiles))
	for _, p := range profiles {
		result = append(result, mapProfile(p))
	}
	return result, nil
}

// GetStreamURI retrieves the RTSP stream URI for a profile.
func (c *Client) GetStreamURI(ctx context.Context, profileToken string) (*StreamInfo, error) {
	if !c.ready {
		return nil, fmt.Errorf("onvif client not connected, call Connect() first")
	}

	uri, err := c.client.GetStreamURI(ctx, profileToken)
	if err != nil {
		return nil, fmt.Errorf("get stream URI: %w", err)
	}

	return mapStreamURI(uri, profileToken), nil
}

// GetCapabilities retrieves device capabilities (PTZ, streaming, etc.).
func (c *Client) GetCapabilities(ctx context.Context) (*DeviceCapabilities, error) {
	if !c.ready {
		return nil, fmt.Errorf("onvif client not connected, call Connect() first")
	}

	caps, err := c.client.GetCapabilities(ctx)
	if err != nil {
		return nil, fmt.Errorf("get capabilities: %w", err)
	}

	return mapCapabilities(caps), nil
}

// mapDeviceInfo converts onvif-go DeviceInformation to project DeviceInfo.
func mapDeviceInfo(info *onvifgo.DeviceInformation) *DeviceInfo {
	return &DeviceInfo{
		Manufacturer: info.Manufacturer,
		Model:        info.Model,
		Firmware:     info.FirmwareVersion,
		SerialNumber: info.SerialNumber,
		HardwareID:   info.HardwareID,
	}
}

// mapCapabilities converts onvif-go Capabilities to project DeviceCapabilities.
func mapCapabilities(caps *onvifgo.Capabilities) *DeviceCapabilities {
	return &DeviceCapabilities{
		PTZ:       caps.PTZ != nil,
		Streaming: caps.Media != nil,
	}
}

// mapProfile converts onvif-go Profile to project DeviceProfile.
func mapProfile(p *onvifgo.Profile) DeviceProfile {
	profile := DeviceProfile{
		Token: p.Token,
		Name:  p.Name,
	}
	if p.VideoEncoderConfiguration != nil {
		profile.Encoding = p.VideoEncoderConfiguration.Encoding
		if p.VideoEncoderConfiguration.Resolution != nil {
			profile.Width = p.VideoEncoderConfiguration.Resolution.Width
			profile.Height = p.VideoEncoderConfiguration.Resolution.Height
		}
	}
	return profile
}

// mapStreamURI converts onvif-go MediaURI to project StreamInfo.
func mapStreamURI(uri *onvifgo.MediaURI, profileToken string) *StreamInfo {
	return &StreamInfo{
		URI:          uri.URI,
		Protocol:     "RTSP",
		Encoding:     "",
		ProfileToken: profileToken,
	}
}

// NewPTZController creates a PTZController backed by this client's onvif-go connection.
// Requires Connect() to have been called first. Returns nil if not connected.
func (c *Client) NewPTZController(profileToken string) PTZController {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client == nil {
		return nil
	}
	return NewPTZController(c.client, profileToken)
}
