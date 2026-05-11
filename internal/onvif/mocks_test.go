package onvif

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var errTestNotFound = &testError{msg: "not found"}

type testError struct { msg string }

func (e *testError) Error() string { return e.msg }

func helperMockDiscoverer(t *testing.T) *MockDiscoverer {
	t.Helper()
	return &MockDiscoverer{
		Devices: []DiscoveredDevice{
			{
				UUID:   "uuid-test-001",
				Name:   "TestCamera1",
				XAddrs: []string{"http://192.168.1.100/onvif/device_service"},
				Scopes: []string{"onvif://www.onvif.org/Profile/Streaming"},
			},
			{
				UUID:   "uuid-test-002",
				Name:   "TestCamera2",
				XAddrs: []string{"http://192.168.1.101/onvif/device_service"},
			},
		},
	}
}

func helperMockDeviceClient(t *testing.T) *MockDeviceClient {
	t.Helper()
	return &MockDeviceClient{
		DeviceInfo: &DeviceInfo{
			Manufacturer: "TestMfg",
			Model:        "CamModel-X",
			Firmware:     "1.0.0",
			SerialNumber: "SN001",
			HardwareID:   "HW001",
		},
		Profiles: []DeviceProfile{
			{Token: "profile_1", Name: "HD", Encoding: "H264", Width: 1920, Height: 1080},
			{Token: "profile_2", Name: "SD", Encoding: "H264", Width: 640, Height: 480},
		},
		StreamURI: &StreamInfo{
			URI:          "rtsp://192.168.1.100/stream1",
			Protocol:     "RTSP",
			Encoding:     "H264",
			ProfileToken: "profile_1",
		},
		Capabilities: &DeviceCapabilities{
			PTZ:       true,
			Streaming: true,
		},
	}
}

func helperMockPTZController(t *testing.T) *MockPTZController {
	t.Helper()
	return &MockPTZController{
		Position: PTZVector{Pan: 0.5, Tilt: -0.2, Zoom: 1.0},
		Moving:   false,
	}
}

// --- Interface compliance checks ---

func TestMockDiscovererImplementsDiscoverer(t *testing.T) {
	t.Helper()
	var _ Discoverer = &MockDiscoverer{}
}

func TestMockDeviceClientImplementsDeviceClient(t *testing.T) {
	t.Helper()
	var _ DeviceClient = &MockDeviceClient{}
}

func TestMockPTZControllerImplementsPTZController(t *testing.T) {
	t.Helper()
	var _ PTZController = &MockPTZController{}
}

// --- MockDiscoverer tests ---

func TestMockDiscoverer_Discover(t *testing.T) {
	m := helperMockDiscoverer(t)
	ctx := context.Background()

	devices, err := m.Discover(ctx, 5*time.Second)
	require.NoError(t, err)
	require.Len(t, devices, 2)
	require.Equal(t, "uuid-test-001", devices[0].UUID)
	require.Equal(t, "TestCamera1", devices[0].Name)
	require.Equal(t, "uuid-test-002", devices[1].UUID)
	require.Equal(t, 1, m.DiscoverCalls)
}

func TestMockDiscoverer_DiscoverError(t *testing.T) {
	m := &MockDiscoverer{Error: errTestNotFound}
	ctx := context.Background()

	devices, err := m.Discover(ctx, 3*time.Second)
	require.Error(t, err)
	require.ErrorIs(t, err, errTestNotFound)
	require.Nil(t, devices)
	require.Equal(t, 1, m.DiscoverCalls)
}

func TestMockDiscoverer_ProbeDevice(t *testing.T) {
	m := helperMockDiscoverer(t)
	ctx := context.Background()

	device, err := m.ProbeDevice(ctx, "192.168.1.100", 80, 3*time.Second)
	require.NoError(t, err)
	require.NotNil(t, device)
	require.Equal(t, "uuid-test-001", device.UUID)
	require.Equal(t, "TestCamera1", device.Name)
	require.Equal(t, 1, m.ProbeDeviceCalls)
}

func TestMockDiscoverer_ProbeDeviceEmpty(t *testing.T) {
	m := &MockDiscoverer{}
	ctx := context.Background()

	device, err := m.ProbeDevice(ctx, "192.168.1.200", 80, 3*time.Second)
	require.NoError(t, err)
	require.Nil(t, device)
}

// --- MockDeviceClient tests ---

func TestMockDeviceClient_Connect(t *testing.T) {
	m := helperMockDeviceClient(t)
	ctx := context.Background()

	err := m.Connect(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, m.ConnectCalls)
}

func TestMockDeviceClient_ConnectError(t *testing.T) {
	m := &MockDeviceClient{ConnectError: errTestNotFound}
	ctx := context.Background()

	err := m.Connect(ctx)
	require.Error(t, err)
	require.ErrorIs(t, err, errTestNotFound)
	require.Equal(t, 1, m.ConnectCalls)
}

func TestMockDeviceClient_GetDeviceInformation(t *testing.T) {
	m := helperMockDeviceClient(t)
	ctx := context.Background()

	info, err := m.GetDeviceInformation(ctx)
	require.NoError(t, err)
	require.NotNil(t, info)
	require.Equal(t, "TestMfg", info.Manufacturer)
	require.Equal(t, "CamModel-X", info.Model)
	require.Equal(t, "1.0.0", info.Firmware)
	require.Equal(t, "SN001", info.SerialNumber)
	require.Equal(t, "HW001", info.HardwareID)
	require.Equal(t, 1, m.GetDeviceInformationCalls)
}

func TestMockDeviceClient_GetProfiles(t *testing.T) {
	m := helperMockDeviceClient(t)
	ctx := context.Background()

	profiles, err := m.GetProfiles(ctx)
	require.NoError(t, err)
	require.Len(t, profiles, 2)
	require.Equal(t, "profile_1", profiles[0].Token)
	require.Equal(t, "HD", profiles[0].Name)
	require.Equal(t, "H264", profiles[0].Encoding)
	require.Equal(t, 1920, profiles[0].Width)
	require.Equal(t, 1080, profiles[0].Height)
	require.Equal(t, "profile_2", profiles[1].Token)
	require.Equal(t, 1, m.GetProfilesCalls)
}

func TestMockDeviceClient_GetStreamURI(t *testing.T) {
	m := helperMockDeviceClient(t)
	ctx := context.Background()

	stream, err := m.GetStreamURI(ctx, "profile_1")
	require.NoError(t, err)
	require.NotNil(t, stream)
	require.Equal(t, "rtsp://192.168.1.100/stream1", stream.URI)
	require.Equal(t, "RTSP", stream.Protocol)
	require.Equal(t, "H264", stream.Encoding)
	require.Equal(t, "profile_1", stream.ProfileToken)
	require.Equal(t, 1, m.GetStreamURICalls)
}

func TestMockDeviceClient_GetCapabilities(t *testing.T) {
	m := helperMockDeviceClient(t)
	ctx := context.Background()

	caps, err := m.GetCapabilities(ctx)
	require.NoError(t, err)
	require.NotNil(t, caps)
	require.True(t, caps.PTZ)
	require.True(t, caps.Streaming)
	require.Equal(t, 1, m.GetCapabilitiesCalls)
}

// --- MockPTZController tests ---

func TestMockPTZController_ContinuousMove(t *testing.T) {
	m := helperMockPTZController(t)
	ctx := context.Background()
	vel := PTZVector{Pan: 0.1, Tilt: 0.0, Zoom: 0.0}

	err := m.ContinuousMove(ctx, vel)
	require.NoError(t, err)
	require.Equal(t, 1, m.ContinuousMoveCalls)
	require.Len(t, m.MoveHistory, 1)
	require.Equal(t, vel, m.MoveHistory[0])
}

func TestMockPTZController_AbsoluteMove(t *testing.T) {
	m := helperMockPTZController(t)
	ctx := context.Background()
	pos := PTZVector{Pan: 0.5, Tilt: -0.2, Zoom: 1.0}

	err := m.AbsoluteMove(ctx, pos)
	require.NoError(t, err)
	require.Equal(t, 1, m.AbsoluteMoveCalls)
	require.Len(t, m.MoveHistory, 1)
	require.Equal(t, pos, m.MoveHistory[0])
}

func TestMockPTZController_RelativeMove(t *testing.T) {
	m := helperMockPTZController(t)
	ctx := context.Background()
	disp := PTZVector{Pan: 0.1, Tilt: 0.05, Zoom: 0.2}

	err := m.RelativeMove(ctx, disp)
	require.NoError(t, err)
	require.Equal(t, 1, m.RelativeMoveCalls)
	require.Len(t, m.MoveHistory, 1)
	require.Equal(t, disp, m.MoveHistory[0])
}

func TestMockPTZController_Stop(t *testing.T) {
	m := helperMockPTZController(t)
	ctx := context.Background()

	err := m.Stop(ctx, true, true)
	require.NoError(t, err)
	require.Equal(t, 1, m.StopCalls)
}

func TestMockPTZController_GetStatus(t *testing.T) {
	m := helperMockPTZController(t)
	ctx := context.Background()

	pos, moving, err := m.GetStatus(ctx)
	require.NoError(t, err)
	require.Equal(t, PTZVector{Pan: 0.5, Tilt: -0.2, Zoom: 1.0}, pos)
	require.False(t, moving)
	require.Equal(t, 1, m.GetStatusCalls)
}

func TestMockPTZController_Error(t *testing.T) {
	m := &MockPTZController{Error: errTestNotFound}
	ctx := context.Background()

	err := m.ContinuousMove(ctx, PTZVector{})
	require.Error(t, err)
	require.ErrorIs(t, err, errTestNotFound)

	err = m.AbsoluteMove(ctx, PTZVector{})
	require.Error(t, err)
	require.ErrorIs(t, err, errTestNotFound)

	err = m.RelativeMove(ctx, PTZVector{})
	require.Error(t, err)
	require.ErrorIs(t, err, errTestNotFound)

	err = m.Stop(ctx, true, true)
	require.Error(t, err)
	require.ErrorIs(t, err, errTestNotFound)

	_, _, err = m.GetStatus(ctx)
	require.Error(t, err)
	require.ErrorIs(t, err, errTestNotFound)
}

func TestMockPTZController_MoveHistory(t *testing.T) {
	m := helperMockPTZController(t)
	ctx := context.Background()

	v1 := PTZVector{Pan: 0.1, Tilt: 0.0, Zoom: 0.0}
	v2 := PTZVector{Pan: -0.1, Tilt: 0.0, Zoom: 0.5}

	require.NoError(t, m.ContinuousMove(ctx, v1))
	require.NoError(t, m.AbsoluteMove(ctx, v2))
	require.NoError(t, m.RelativeMove(ctx, v1))

	require.Len(t, m.MoveHistory, 3)
	require.Equal(t, v1, m.MoveHistory[0])
	require.Equal(t, v2, m.MoveHistory[1])
	require.Equal(t, v1, m.MoveHistory[2])
}

// --- Multiple calls tracking ---

func TestMockDeviceClient_CallTracking(t *testing.T) {
	m := helperMockDeviceClient(t)
	ctx := context.Background()

	require.NoError(t, m.Connect(ctx))
	_, _ = m.GetDeviceInformation(ctx)
	_, _ = m.GetProfiles(ctx)
	_, _ = m.GetStreamURI(ctx, "p1")
	_, _ = m.GetCapabilities(ctx)
	_, _ = m.GetDeviceInformation(ctx)

	require.Equal(t, 1, m.ConnectCalls)
	require.Equal(t, 2, m.GetDeviceInformationCalls)
	require.Equal(t, 1, m.GetProfilesCalls)
	require.Equal(t, 1, m.GetStreamURICalls)
	require.Equal(t, 1, m.GetCapabilitiesCalls)
}
