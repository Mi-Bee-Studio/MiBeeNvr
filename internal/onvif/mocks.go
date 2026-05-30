package onvif

import (
	"context"
	"sync"
	"time"
)

// MockDiscoverer is a testable Discoverer that returns configured values.
type MockDiscoverer struct {
	mu     sync.Mutex
	Devices []DiscoveredDevice
	Error   error

	DiscoverCalls    int
	ProbeDeviceCalls int
}

func (m *MockDiscoverer) Discover(ctx context.Context, timeout time.Duration) ([]DiscoveredDevice, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.DiscoverCalls++
	return m.Devices, m.Error
}

func (m *MockDiscoverer) ProbeDevice(ctx context.Context, host string, port int, timeout time.Duration) (*DiscoveredDevice, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ProbeDeviceCalls++
	if len(m.Devices) == 0 {
		return nil, m.Error
	}
	return &m.Devices[0], m.Error
}

// MockDeviceClient is a testable DeviceClient that returns configured values.
type MockDeviceClient struct {
	mu           sync.Mutex
	DeviceInfo   *DeviceInfo
	Profiles     []DeviceProfile
	StreamURI    *StreamInfo
	Capabilities *DeviceCapabilities
	ConnectError error

	ConnectCalls              int
	GetDeviceInformationCalls int
	GetProfilesCalls          int
	GetStreamURICalls         int
	GetCapabilitiesCalls      int
}

func (m *MockDeviceClient) Connect(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ConnectCalls++
	return m.ConnectError
}

func (m *MockDeviceClient) GetDeviceInformation(ctx context.Context) (*DeviceInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.GetDeviceInformationCalls++
	return m.DeviceInfo, nil
}

func (m *MockDeviceClient) GetProfiles(ctx context.Context) ([]DeviceProfile, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.GetProfilesCalls++
	return m.Profiles, nil
}

func (m *MockDeviceClient) GetStreamURI(ctx context.Context, profileToken string) (*StreamInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.GetStreamURICalls++
	return m.StreamURI, nil
}

func (m *MockDeviceClient) GetCapabilities(ctx context.Context) (*DeviceCapabilities, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.GetCapabilitiesCalls++
	return m.Capabilities, nil
}

// MockPTZController is a testable PTZController that records calls and returns configured values.
type MockPTZController struct {
	mu          sync.Mutex
	Position    PTZVector
	Moving      bool
	Error       error
	MoveHistory []PTZVector

	ContinuousMoveCalls int
	AbsoluteMoveCalls   int
	RelativeMoveCalls   int
	StopCalls           int
	GetStatusCalls      int
	GetPresetsCalls     int
	SetPresetCalls      int
	GoToPresetCalls     int
	RemovePresetCalls   int

	Presets       []PTZPreset
	SetPresetToken string
}

func (m *MockPTZController) ContinuousMove(ctx context.Context, velocity PTZVector) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ContinuousMoveCalls++
	m.MoveHistory = append(m.MoveHistory, velocity)
	return m.Error
}

func (m *MockPTZController) AbsoluteMove(ctx context.Context, position PTZVector) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.AbsoluteMoveCalls++
	m.MoveHistory = append(m.MoveHistory, position)
	return m.Error
}

func (m *MockPTZController) RelativeMove(ctx context.Context, displacement PTZVector) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.RelativeMoveCalls++
	m.MoveHistory = append(m.MoveHistory, displacement)
	return m.Error
}

func (m *MockPTZController) Stop(ctx context.Context, stopPanTilt, stopZoom bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.StopCalls++
	return m.Error
}

func (m *MockPTZController) GetStatus(ctx context.Context) (PTZVector, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.GetStatusCalls++
	return m.Position, m.Moving, m.Error
}

func (m *MockPTZController) GetPresets(ctx context.Context) ([]PTZPreset, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.GetPresetsCalls++
	return m.Presets, m.Error
}

func (m *MockPTZController) SetPreset(ctx context.Context, name string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.SetPresetCalls++
	return m.SetPresetToken, m.Error
}

func (m *MockPTZController) GoToPreset(ctx context.Context, token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.GoToPresetCalls++
	return m.Error
}

func (m *MockPTZController) RemovePreset(ctx context.Context, token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.RemovePresetCalls++
	return m.Error
}


// MockImagingController is a testable ImagingController.
type MockImagingController struct {
	mu              sync.Mutex
	Settings        *ImagingSettings
	Options         *ImagingOptions
	Error           error

	GetImagingSettingsCalls int
	SetImagingSettingsCalls int
	GetImagingOptionsCalls  int
}

func (m *MockImagingController) GetImagingSettings(ctx context.Context) (*ImagingSettings, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.GetImagingSettingsCalls++
	return m.Settings, m.Error
}

func (m *MockImagingController) SetImagingSettings(ctx context.Context, settings ImagingSettings) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.SetImagingSettingsCalls++
	return m.Error
}

func (m *MockImagingController) GetImagingOptions(ctx context.Context) (*ImagingOptions, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.GetImagingOptionsCalls++
	return m.Options, m.Error
}

var _ ImagingController = (*MockImagingController)(nil)

// MockPresetManager is a testable PresetManager.
type MockPresetManager struct {
	mu        sync.Mutex
	Presets   []PTZPreset
	Error     error

	GetPresetsCalls   int
	SetPresetCalls    int
	GoToPresetCalls   int
	RemovePresetCalls int
}

func (m *MockPresetManager) GetPresets(ctx context.Context) ([]PTZPreset, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.GetPresetsCalls++
	return m.Presets, m.Error
}

func (m *MockPresetManager) SetPreset(ctx context.Context, name string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.SetPresetCalls++
	return "preset-token-" + name, m.Error
}

func (m *MockPresetManager) GoToPreset(ctx context.Context, token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.GoToPresetCalls++
	return m.Error
}

func (m *MockPresetManager) RemovePreset(ctx context.Context, token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.RemovePresetCalls++
	return m.Error
}

var _ PresetManager = (*MockPresetManager)(nil)

// MockEventSubscriber is a testable EventSubscriber.
type MockEventSubscriber struct {
	mu        sync.Mutex
	Events    []ONVIFEvent
	Error     error

	SubscribeCalls        int
	UnsubscribeCalls      int
	GetEventMessagesCalls int
}

func (m *MockEventSubscriber) Subscribe(ctx context.Context, cameraID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.SubscribeCalls++
	return m.Error
}

func (m *MockEventSubscriber) Unsubscribe(ctx context.Context, cameraID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.UnsubscribeCalls++
	return m.Error
}

func (m *MockEventSubscriber) GetEventMessages(ctx context.Context) ([]ONVIFEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.GetEventMessagesCalls++
	return m.Events, m.Error
}

var _ EventSubscriber = (*MockEventSubscriber)(nil)

// MockDeviceManager is a testable DeviceManager.
type MockDeviceManager struct {
	mu                  sync.Mutex
	NetworkInterfaces   []NetworkInterface
	Users               []ONVIFUser
	Error               error

	SystemRebootCalls          int
	GetNetworkInterfacesCalls int
	SetNetworkInterfacesCalls int
	GetUsersCalls             int
	CreateUsersCalls          int
	DeleteUsersCalls          int
	SetUserCalls              int
}

func (m *MockDeviceManager) SystemReboot(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.SystemRebootCalls++
	return m.Error
}

func (m *MockDeviceManager) GetNetworkInterfaces(ctx context.Context) ([]NetworkInterface, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.GetNetworkInterfacesCalls++
	return m.NetworkInterfaces, m.Error
}

func (m *MockDeviceManager) SetNetworkInterfaces(ctx context.Context, interfaces []NetworkInterface) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.SetNetworkInterfacesCalls++
	return m.Error
}

func (m *MockDeviceManager) GetUsers(ctx context.Context) ([]ONVIFUser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.GetUsersCalls++
	return m.Users, m.Error
}

func (m *MockDeviceManager) CreateUsers(ctx context.Context, users []ONVIFUser) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.CreateUsersCalls++
	return m.Error
}

func (m *MockDeviceManager) DeleteUsers(ctx context.Context, usernames []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.DeleteUsersCalls++
	return m.Error
}

func (m *MockDeviceManager) SetUser(ctx context.Context, username, password string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.SetUserCalls++
	return m.Error
}

var _ DeviceManager = (*MockDeviceManager)(nil)

// MockSnapshotProvider is a testable SnapshotProvider.
type MockSnapshotProvider struct {
	mu    sync.Mutex
	URI   string
	Error error

	GetSnapshotUriCalls int
}

func (m *MockSnapshotProvider) GetSnapshotUri(ctx context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.GetSnapshotUriCalls++
	return m.URI, m.Error
}

var _ SnapshotProvider = (*MockSnapshotProvider)(nil)
