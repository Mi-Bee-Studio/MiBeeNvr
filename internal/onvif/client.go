package onvif

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
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

func (c *Client) GetStreamURI(ctx context.Context, profileToken string) (*StreamInfo, error) {
	if !c.ready {
		return nil, fmt.Errorf("onvif client not connected, call Connect() first")
	}

	uri, err := c.client.GetStreamURI(ctx, profileToken)
	if err != nil {
		return nil, fmt.Errorf("get stream URI: %w", err)
	}

	// onvif-go may return empty URI due to XML namespace parsing issues
	// with some devices. Fallback to raw SOAP request if URI is empty.
	if strings.TrimSpace(uri.URI) == "" {
		logger.Warn("onvif-go returned empty URI, trying raw SOAP fallback", "profile_token", profileToken)
		rawURI, rawErr := c.getRawStreamURI(ctx, profileToken)
		if rawErr != nil {
			logger.Warn("raw SOAP fallback failed", "error", rawErr)
		} else if strings.TrimSpace(rawURI) != "" {
			uri.URI = rawURI
		}
	}

	logger.Info("GetStreamURI response", "profile_token", profileToken, "uri", uri.URI)

	return mapStreamURI(uri, profileToken), nil
}

// getRawStreamURI sends a raw SOAP GetStreamUri request and parses the response.
// This works around XML namespace parsing issues in onvif-go with some devices.
func (c *Client) getRawStreamURI(ctx context.Context, profileToken string) (string, error) {
	soapBody := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"
 xmlns:trt="http://www.onvif.org/ver10/media/wsdl"
 xmlns:tt="http://www.onvif.org/ver10/schema">
  <s:Body>
    <trt:GetStreamUri>
      <trt:StreamSetup>
        <tt:Stream>RTP-Unicast</tt:Stream>
        <tt:Transport>
          <tt:Protocol>RTSP</tt:Protocol>
        </tt:Transport>
      </trt:StreamSetup>
      <trt:ProfileToken>%s</trt:ProfileToken>
    </trt:GetStreamUri>
  </s:Body>
</s:Envelope>`, profileToken)

	req, err := http.NewRequestWithContext(ctx, "POST", c.endpoint, strings.NewReader(soapBody))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/soap+xml")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	// Parse URI from XML response using regex-like approach
	// Look for <tt:Uri> or <Uri> tag content
	var envelope struct {
		XMLName xml.Name `xml:"Envelope"`
		Body    struct {
			XMLName xml.Name `xml:"Body"`
			GetStreamURIResponse struct {
				XMLName  xml.Name `xml:"GetStreamUriResponse"`
				MediaURI struct {
					URI string `xml:"Uri"`
				} `xml:"MediaUri"`
			} `xml:"GetStreamUriResponse"`
		} `xml:"Body"`
	}

	if err := xml.Unmarshal(body, &envelope); err != nil {
		return "", fmt.Errorf("parse response: %w", err)
	}

	return envelope.Body.GetStreamURIResponse.MediaURI.URI, nil
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
