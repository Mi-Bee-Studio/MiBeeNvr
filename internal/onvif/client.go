package onvif

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
)

var logger = slog.Default().With("component", "onvif-client")

// Client wraps ONVIF device operations.
type Client struct {
	endpoint string
	username string
	password string
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

	// TODO: Implement with onvif-go library
	// onvifClient, err := onvif.NewClient(c.endpoint, onvif.WithAuth(c.username, c.password))
	// if err != nil { return err }
	// if err := onvifClient.Initialize(ctx); err != nil { return err }

	c.ready = true
	logger.Info("connected to ONVIF device", "endpoint", c.endpoint)
	return nil
}

// GetDeviceInformation retrieves device info (manufacturer, model, firmware).
func (c *Client) GetDeviceInformation(ctx context.Context) (*DeviceInfo, error) {
	if !c.ready {
		return nil, fmt.Errorf("onvif client not connected, call Connect() first")
	}
	// TODO: Implement with onvif-go
	return &DeviceInfo{}, nil
}

// GetProfiles retrieves media profiles from the device.
func (c *Client) GetProfiles(ctx context.Context) ([]DeviceProfile, error) {
	if !c.ready {
		return nil, fmt.Errorf("onvif client not connected, call Connect() first")
	}
	// TODO: Implement with onvif-go
	return nil, nil
}

// GetStreamURI retrieves the RTSP stream URI for a profile.
func (c *Client) GetStreamURI(ctx context.Context, profileToken string) (*StreamInfo, error) {
	if !c.ready {
		return nil, fmt.Errorf("onvif client not connected, call Connect() first")
	}
	// TODO: Implement with onvif-go
	return nil, nil
}

// GetCapabilities retrieves device capabilities (PTZ, streaming, etc.).
func (c *Client) GetCapabilities(ctx context.Context) (*DeviceCapabilities, error) {
	if !c.ready {
		return nil, fmt.Errorf("onvif client not connected, call Connect() first")
	}
	// TODO: Implement with onvif-go
	return &DeviceCapabilities{}, nil
}
