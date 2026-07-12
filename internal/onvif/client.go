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
	"time"

	onvifgo "github.com/0x524a/onvif-go"
)

var logger = slog.Default().With("component", "onvif-client")

var httpClient = &http.Client{Timeout: 15 * time.Second}

// Client wraps an onvif-go Client for ONVIF device operations.
type Client struct {
	endpoint string
	username string
	password string
	client   *onvifgo.Client
	mu       sync.Mutex
	ready    bool

	cachedCapabilities *DeviceCapabilitiesDetailed
	capsMu             sync.Mutex
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
// Idempotent: calling Connect again on an already-connected client is a no-op,
// so the recorder, snapshot auto-populator and PTZ controller can safely share
// one client without each triggering a fresh GetCapabilities handshake
// (critical for minimal ONVIF devices like the ESP32 MiBeeCam, which block
// under concurrent HTTP load).
func (c *Client) Connect(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.ready {
		return nil
	}

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

// IsReady reports whether Connect has completed successfully. Lets callers
// holding a different lock (e.g. the camera manager's onvifMu) check readiness
// and decide whether to Connect without racing the recorder's own Connect.
func (c *Client) IsReady() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ready
}

// GetDeviceInformation retrieves device info (manufacturer, model, firmware).
func (c *Client) GetDeviceInformation(ctx context.Context) (*DeviceInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
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
	c.mu.Lock()
	defer c.mu.Unlock()
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
	c.mu.Lock()
	defer c.mu.Unlock()
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
		rawURI, rawErr := c.getRawStreamURI(ctx, profileToken, "RTSP")
		if rawErr != nil {
			logger.Warn("raw SOAP fallback failed", "error", rawErr)
		} else if strings.TrimSpace(rawURI) != "" {
			uri.URI = rawURI
		}
	}

	logger.Info("GetStreamURI response", "profile_token", profileToken, "uri", uri.URI)

	return mapStreamURI(uri, profileToken), nil
}

// GetStreamURIWithProtocol requests a stream URI with a specific transport protocol.
// Valid protocols: "RTSP" (default), "HTTP" (RTSP-over-HTTP tunneling), "UDP".
// This uses raw SOAP since onvif-go doesn't support protocol selection.
func (c *Client) GetStreamURIWithProtocol(ctx context.Context, profileToken, protocol string) (*StreamInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.ready {
		return nil, fmt.Errorf("onvif client not connected, call Connect() first")
	}
	rawURI, err := c.getRawStreamURI(ctx, profileToken, protocol)
	if err != nil {
		return nil, fmt.Errorf("get stream URI with protocol %q: %w", protocol, err)
	}
	if strings.TrimSpace(rawURI) == "" {
		return nil, fmt.Errorf("device returned empty URI for protocol %q", protocol)
	}
	logger.Info("GetStreamURIWithProtocol response", "profile_token", profileToken, "protocol", protocol, "uri", rawURI)
	return &StreamInfo{URI: rawURI, ProfileToken: profileToken}, nil
}

// getRawStreamURI sends a raw SOAP GetStreamUri request and parses the response.
// This works around XML namespace parsing issues in onvif-go with some devices.
func (c *Client) getRawStreamURI(ctx context.Context, profileToken, protocol string) (string, error) {
	soapBody := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"
 xmlns:trt="http://www.onvif.org/ver10/media/wsdl"
 xmlns:tt="http://www.onvif.org/ver10/schema">
  <s:Body>
    <trt:GetStreamUri>
      <trt:StreamSetup>
        <tt:Stream>RTP-Unicast</tt:Stream>
        <tt:Transport>
          <tt:Protocol>%s</tt:Protocol>
        </tt:Transport>
      </trt:StreamSetup>
      <trt:ProfileToken>%s</trt:ProfileToken>
    </trt:GetStreamUri>
  </s:Body>
</s:Envelope>`, protocol, profileToken)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, strings.NewReader(soapBody))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/soap+xml")

	resp, err := httpClient.Do(req)
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
			XMLName              xml.Name `xml:"Body"`
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
// Returns cached capabilities if available. On SOAP failure, returns minimal
// capabilities (all flags false) instead of error, and caches the minimal result
// to avoid repeated failing calls to limited devices.
func (c *Client) GetCapabilities(ctx context.Context) (*DeviceCapabilitiesDetailed, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Check cache first
	c.capsMu.Lock()
	if c.cachedCapabilities != nil {
		caps := c.cachedCapabilities
		c.capsMu.Unlock()
		return caps, nil
	}
	c.capsMu.Unlock()

	if !c.ready {
		return nil, fmt.Errorf("onvif client not connected, call Connect() first")
	}

	caps, err := c.client.GetCapabilities(ctx)
	if err != nil {
		logger.Debug("failed to get capabilities from device, returning minimal capabilities", "error", err)
		minimal := &DeviceCapabilitiesDetailed{}
		c.capsMu.Lock()
		c.cachedCapabilities = minimal
		c.capsMu.Unlock()
		return minimal, nil
	}

	result := mapCapabilities(caps)
	c.capsMu.Lock()
	c.cachedCapabilities = result
	c.capsMu.Unlock()

	return result, nil
}

// InvalidateCapabilitiesCache clears the cached capabilities.
// Call this when the device capabilities may have changed (e.g., after firmware update).
func (c *Client) InvalidateCapabilitiesCache() {
	c.capsMu.Lock()
	c.cachedCapabilities = nil
	c.capsMu.Unlock()
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

func mapCapabilities(caps *onvifgo.Capabilities) *DeviceCapabilitiesDetailed {
	return &DeviceCapabilitiesDetailed{
		PTZ:       caps.PTZ != nil,
		Imaging:   caps.Imaging != nil,
		Events:    caps.Events != nil,
		Snapshot:  caps.Media != nil,
		Streaming: caps.Media != nil,
		Device:    caps.Device != nil,
	}
}

// mapProfile converts onvif-go Profile to project DeviceProfile.
func mapProfile(p *onvifgo.Profile) DeviceProfile {
	profile := DeviceProfile{
		Token: p.Token,
		Name:  p.Name,
	}
	// VideoSourceConfiguration.SourceToken is required by the imaging service
	// (GetImagingSettings/SetImagingSettings/GetOptions take a VideoSourceToken,
	// NOT a profile token). Many HiSilicon-OEM cameras reject the profile token
	// here with HTTP 400, so we must surface the real video source token.
	if p.VideoSourceConfiguration != nil {
		profile.VideoSourceToken = p.VideoSourceConfiguration.SourceToken
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

// PTZEndpoint returns the PTZ service endpoint URL derived from the device endpoint.
// Format: http://host:port/onvif/ptz_service
func (c *Client) PTZEndpoint() string {
	return strings.Replace(c.endpoint, "device_service", "ptz_service", 1)
}

// DeviceEndpoint returns the device service endpoint URL (same as c.endpoint).
func (c *Client) DeviceEndpoint() string {
	return c.endpoint
}

// DoRawSOAPNoAuth sends a raw SOAP request without any authentication header.
// Used as fallback for cameras with buggy per-service WS-Security validation.
func (c *Client) DoRawSOAPNoAuth(ctx context.Context, endpoint, soapBody string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(soapBody))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/soap+xml; charset=utf-8")
	// No auth header — camera firmware rejects WS-Security on some services

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("SOAP request failed with status %d: %s", resp.StatusCode, truncateStr(string(body), 500))
	}
	return body, nil
}

// DoRawSOAPBasicAuth sends a raw SOAP request with HTTP Basic Auth.
// Used as fallback for cameras that accept BasicAuth but reject WS-Security.
func (c *Client) DoRawSOAPBasicAuth(ctx context.Context, endpoint, soapBody string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(soapBody))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/soap+xml; charset=utf-8")
	if c.username != "" {
		req.SetBasicAuth(c.username, c.password)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("SOAP request failed with status %d: %s", resp.StatusCode, truncateStr(string(body), 500))
	}
	return body, nil
}

// buildWSSecurityHeader creates a WS-Security header with UsernameToken using PasswordText.
func (c *Client) buildWSSecurityHeader() string {
	return `<s:Header><wsse:Security xmlns:wsse="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-secext-1.0.xsd" xmlns:wsu="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-utility-1.0.xsd"><wsse:UsernameToken><wsse:Username>` + c.username + `</wsse:Username><wsse:Password Type="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-username-token-profile-1.0#PasswordText">` + c.password + `</wsse:Password></wsse:UsernameToken></wsse:Security></s:Header>`
}

// DoRawSOAPWithPasswordText sends a raw SOAP request with WS-Security UsernameToken (PasswordText).
// The soapBody parameter should be a full SOAP envelope (with <s:Envelope> and <s:Body>).
// This method injects the WS-Security header into the envelope before the <s:Body>.
// Used as fallback for cameras that reject PasswordDigest but accept PasswordText.
func (c *Client) DoRawSOAPWithPasswordText(ctx context.Context, endpoint, soapBody string) ([]byte, error) {
	// Inject WS-Security header before <s:Body>
	bodyWithAuth := strings.Replace(soapBody, "<s:Body>", c.buildWSSecurityHeader()+"<s:Body>", 1)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(bodyWithAuth))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/soap+xml; charset=utf-8")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("SOAP request failed with status %d: %s", resp.StatusCode, truncateStr(string(body), 500))
	}
	return body, nil
}

// isAuthError checks if an error indicates WS-Security auth rejection by the camera.
// This is used to trigger fallback to an alternate auth strategy (Basic → NoAuth, etc.).
func isAuthError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "NotAuthorized") ||
		strings.Contains(s, "status 401") ||
		strings.Contains(s, "status 403") ||
		strings.Contains(s, "status 400") ||
		strings.Contains(s, "status 500") ||
		strings.Contains(s, "status 502")
}

// NewPTZController creates a PTZController backed by this client's onvif-go connection.
// Requires Connect() to have been called first. Returns nil if not connected.
func (c *Client) NewPTZController(profileToken string) PTZController {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client == nil {
		return nil
	}
	return NewPTZController(c.client, profileToken, c.PTZEndpoint(), c.username, c.password, c.DoRawSOAPWithPasswordText)
}

// NewDeviceManager creates a DeviceManager backed by this client's onvif-go connection.
// Requires Connect() to have been called first. Returns nil if not connected.
func (c *Client) NewDeviceManager() DeviceManager {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client == nil {
		return nil
	}
	return NewDeviceManager(c.client, c.DeviceEndpoint(), c.username, c.password, c.DoRawSOAPWithPasswordText)
}

// NewImagingController creates an ImagingController backed by this client's onvif-go connection.
// Requires Connect() to have been called first. Returns nil if not connected.
func (c *Client) NewImagingController(profileToken string) *ImagingControllerImpl {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client == nil {
		return nil
	}
	ctrl := NewImagingController(c.client, profileToken)
	ctrl.SetCredentials(c.username, c.password)
	return ctrl
}

// GetEndpoint returns the device service endpoint URL.
func (c *Client) GetEndpoint() string {
	return c.endpoint
}

// NewSnapshotProvider creates a SnapshotProvider backed by this client's onvif-go connection.
// Requires Connect() to have been called first. Returns nil if not connected.
func (c *Client) NewSnapshotProvider(profileToken string) SnapshotProvider {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client == nil {
		return nil
	}
	return NewSnapshotProvider(c.client, profileToken)
}

// NewEventSubscriber creates an EventSubscriber backed by this client's onvif-go connection.
// Requires Connect() to have been called first. Returns nil if not connected.
func (c *Client) NewEventSubscriber(opts ...EventSubscriberOption) EventSubscriber {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client == nil {
		return nil
	}
	return NewEventSubscriber(c.client, opts...)
}
