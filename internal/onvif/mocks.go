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
