package onvif

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/slogx"

	onvifgo "github.com/mickeyzzc/onvif-go/v2/onvif"
)

var logger = slogx.Component("onvif-client")

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

// newLibClient builds the library client with the NVR's default configuration:
// the full auth ladder (digest first, then password-text, HTTP basic and none)
// and automatic clock-skew measurement. The extra ladder rungs cover old
// firmwares that answer a rejected digest with an empty 200 body instead of a
// 401/fault (#1051, onvif-go#140); the auto skew makes the very first
// authenticated request carry device-correct digest timestamps (Hikvision
// replay-window rejections).
func newLibClient(endpoint, username, password string, opts ...onvifgo.ClientOption) (*onvifgo.Client, error) {
	opts = append([]onvifgo.ClientOption{
		onvifgo.WithCredentials(username, password),
		onvifgo.WithAuthFallback(onvifgo.AuthPasswordText, onvifgo.AuthHTTPBasic, onvifgo.AuthNone),
		onvifgo.WithAutoClockSkew(),
	}, opts...)

	return onvifgo.NewClient(endpoint, opts...)
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

	onvifClient, err := newLibClient(c.endpoint, c.username, c.password)
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

	info, err := c.client.Device().GetDeviceInformation(ctx)
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

	profiles, err := c.client.Media().GetProfiles(ctx)
	if err != nil {
		return nil, fmt.Errorf("get profiles: %w", err)
	}

	result := make([]DeviceProfile, 0, len(profiles))
	for _, p := range profiles {
		result = append(result, mapProfile(p))
	}
	return result, nil
}

// RequestSyncPoint asks the device to mark the next frame of the profile's
// stream as a sync point (trt:SetSynchronizationPoint), so a recorder that
// just (re)connected receives a keyframe immediately instead of waiting for
// the next GOP boundary. Best-effort by contract: devices without the action
// answer a Sender fault, which callers log and ignore.
func (c *Client) RequestSyncPoint(ctx context.Context, profileToken string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.ready {
		return fmt.Errorf("onvif client not connected, call Connect() first")
	}
	if err := c.client.Media().SetSynchronizationPoint(ctx, profileToken); err != nil {
		return fmt.Errorf("set synchronization point: %w", err)
	}
	return nil
}

func (c *Client) GetStreamURI(ctx context.Context, profileToken string) (*StreamInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.ready {
		return nil, fmt.Errorf("onvif client not connected, call Connect() first")
	}

	uri, err := c.client.Media().GetStreamURI(ctx, profileToken)
	if err != nil {
		return nil, fmt.Errorf("get stream URI: %w", err)
	}

	logger.Info("GetStreamURI response", "profile_token", profileToken, "uri", uri.URI)

	return mapStreamURI(uri, profileToken), nil
}

// GetStreamURIWithProtocol requests a stream URI with a specific transport protocol.
// Valid protocols: "RTSP" (default), "HTTP" (RTSP-over-HTTP tunneling), "UDP".
func (c *Client) GetStreamURIWithProtocol(ctx context.Context, profileToken, protocol string) (*StreamInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.ready {
		return nil, fmt.Errorf("onvif client not connected, call Connect() first")
	}

	uri, err := c.client.Media().GetStreamURIWithOptions(ctx, profileToken, onvifgo.StreamSetup{
		Stream:    onvifgo.StreamRTPUnicast,
		Transport: &onvifgo.Transport{Protocol: protocol},
	})
	if err != nil {
		// The library already rejects an empty Uri in the response
		// (ErrEmptyMediaURI), so a successful return always carries a URI.
		return nil, fmt.Errorf("get stream URI with protocol %q: %w", protocol, err)
	}
	logger.Info("GetStreamURIWithProtocol response", "profile_token", profileToken, "protocol", protocol, "uri", uri.URI)
	return &StreamInfo{URI: uri.URI, Protocol: protocol, ProfileToken: profileToken}, nil
}

// GetCapabilities retrieves device capabilities (PTZ, streaming, etc.).
// Returns cached capabilities if available. On SOAP failure, returns minimal
// capabilities (all flags false) instead of error, and caches the minimal result
// to avoid repeated failing calls to limited devices.
func (c *Client) GetCapabilities(ctx context.Context) (*DeviceCapabilitiesDetailed, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
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

	caps, err := c.client.Device().GetCapabilities(ctx)
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

// DeviceEndpoint returns the device service endpoint URL (same as c.endpoint).
func (c *Client) DeviceEndpoint() string {
	return c.endpoint
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

// NewDeviceManager creates a DeviceManager backed by this client's onvif-go connection.
// Requires Connect() to have been called first. Returns nil if not connected.
func (c *Client) NewDeviceManager() DeviceManager {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client == nil {
		return nil
	}
	return NewDeviceManager(c.client)
}

// NewImagingController creates an ImagingController backed by this client's onvif-go connection.
// Requires Connect() to have been called first. Returns nil if not connected.
func (c *Client) NewImagingController(profileToken string) *ImagingControllerImpl {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client == nil {
		return nil
	}
	return NewImagingController(c.client, profileToken)
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

// NewPushSubscriber creates a push-transport EventSubscriber (wsnt:Subscribe +
// device-POSTed Notify) backed by this client's library events service
// (the wsnt primitives live in onvif-go; NVR policy stays in PushSubscriber).
// Requires Connect() to have been called first; nil-safe like the other
// factories (callers treat nil as "no push").
func (c *Client) NewPushSubscriber(opts ...PushOption) EventSubscriber {
	c.mu.Lock()
	ready := c.ready
	lib := c.client
	c.mu.Unlock()
	if !ready || lib == nil {
		return nil
	}
	return NewPushSubscriber(lib.Events(), opts...)
}

// AuthDiagnosis is the result of probing why an ONVIF device rejects
// credentials, mapped from the library's triage (onvif-go's DiagnoseAuth:
// measure the device clock skew, retry the digest with device time, and
// classify ok / clock-skew / bad-credentials).
type AuthDiagnosis struct {
	SkewDetected bool    // a significant clock skew is part of the picture
	SkewSeconds  float64 // measured deviceTime − localTime, signed
	Diagnosis    string  // human-readable explanation + suggested fix
}

// DiagnoseAuth runs the library's authentication triage against the client's
// endpoint and credentials. Callers surface the diagnosis when a digest-auth
// failure looks skew-related; a non-skew verdict keeps SkewDetected=false so
// callers fall back to their existing error text.
func (c *Client) DiagnoseAuth(ctx context.Context) AuthDiagnosis {
	lib, err := onvifgo.NewClient(c.endpoint,
		onvifgo.WithCredentials(c.username, c.password),
	)
	if err != nil {
		return AuthDiagnosis{Diagnosis: fmt.Sprintf("create ONVIF client: %v", err)}
	}
	diag, err := lib.DiagnoseAuth(ctx)
	if err != nil || diag == nil {
		return AuthDiagnosis{}
	}
	out := AuthDiagnosis{
		SkewSeconds: diag.ClockSkew.Seconds(),
		Diagnosis:   diag.Detail,
	}
	switch diag.Status {
	case onvifgo.AuthStatusClockSkew:
		out.SkewDetected = true
	case onvifgo.AuthStatusBadCredentials:
		// Skew present AND credentials wrong: the old diagnosis surfaced both
		// (skew breaks digest, so fix the clock first, then re-check creds).
		out.SkewDetected = diag.ClockSkew != 0
	}
	return out
}
