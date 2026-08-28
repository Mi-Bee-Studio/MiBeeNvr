// SPDX-License-Identifier: MIT
//
// Mock surface test: every mock in mocks.go is exercised once so the test
// binary counts their statements (mocks live in a non-_test file, so unused
// mock methods drag package coverage down without this).

package onvif

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMockSurface(t *testing.T) {
	ctx := context.Background()

	disc := &MockDiscoverer{Devices: []DiscoveredDevice{{UUID: "u"}}}
	devs, err := disc.Discover(ctx, 0)
	require.NoError(t, err)
	require.Len(t, devs, 1)
	require.Equal(t, 1, disc.DiscoverCalls)
	_, _ = disc.ProbeDevice(ctx, "127.0.0.1", 80, 0)
	require.Equal(t, 1, disc.ProbeDeviceCalls)

	dc := &MockDeviceClient{DeviceInfo: &DeviceInfo{}, Profiles: []DeviceProfile{{}}, StreamURI: &StreamInfo{}, Capabilities: &DeviceCapabilitiesDetailed{}}
	require.NoError(t, dc.Connect(ctx))
	_, _ = dc.GetDeviceInformation(ctx)
	_, _ = dc.GetProfiles(ctx)
	_, _ = dc.GetStreamURI(ctx, "tok")
	_, _ = dc.GetStreamURIWithProtocol(ctx, "tok", "RTSP")
	_, _ = dc.GetCapabilities(ctx)
	require.Equal(t, 1, dc.GetStreamURICalls)

	ptz := &MockPTZController{Position: PTZVector{Pan: 1}}
	require.NoError(t, ptz.ContinuousMove(ctx, PTZVector{}))
	require.NoError(t, ptz.AbsoluteMove(ctx, PTZVector{}))
	require.NoError(t, ptz.RelativeMove(ctx, PTZVector{}))
	require.NoError(t, ptz.Stop(ctx, true, true))
	_, _, _ = ptz.GetStatus(ctx)
	_, _ = ptz.GetPresets(ctx)
	_, _ = ptz.SetPreset(ctx, "p")
	require.NoError(t, ptz.GoToPreset(ctx, "tok"))
	require.NoError(t, ptz.RemovePreset(ctx, "tok"))
	require.NotEmpty(t, ptz.MoveHistory)

	img := &MockImagingController{Settings: &ImagingSettings{}, Options: &ImagingOptions{}}
	_, _ = img.GetImagingSettings(ctx)
	require.NoError(t, img.SetImagingSettings(ctx, ImagingSettings{}))
	_, _ = img.GetImagingOptions(ctx)

	pm := &MockPresetManager{Presets: []PTZPreset{{Token: "t1", Name: "n1"}}}
	presets, err := pm.GetPresets(ctx)
	require.NoError(t, err)
	require.Len(t, presets, 1)
	_, _ = pm.SetPreset(ctx, "n")
	require.NoError(t, pm.GoToPreset(ctx, "t1"))
	require.NoError(t, pm.RemovePreset(ctx, "t1"))

	es := &MockEventSubscriber{Events: []ONVIFEvent{{CameraID: "c"}}}
	require.NoError(t, es.Subscribe(ctx, "c"))
	require.NoError(t, es.Unsubscribe(ctx, "c"))
	msgs, err := es.GetEventMessages(ctx)
	require.NoError(t, err)
	require.Len(t, msgs, 1)

	dm := &MockDeviceManager{}
	require.NoError(t, dm.SystemReboot(ctx))
	_, _ = dm.GetNetworkInterfaces(ctx)
	require.NoError(t, dm.SetNetworkInterfaces(ctx, nil))
	_, _ = dm.GetUsers(ctx)
	require.NoError(t, dm.CreateUsers(ctx, nil))
	require.NoError(t, dm.DeleteUsers(ctx, nil))
	require.NoError(t, dm.SetUser(ctx, "u", "p"))

	sp := &MockSnapshotProvider{}
	_, _ = sp.GetSnapshotUri(ctx)
}

func TestMockErrorPaths(t *testing.T) {
	ctx := context.Background()

	dc := &MockDeviceClient{ConnectError: context.DeadlineExceeded}
	require.ErrorIs(t, dc.Connect(ctx), context.DeadlineExceeded)

	ptz := &MockPTZController{Error: context.Canceled}
	require.ErrorIs(t, ptz.ContinuousMove(ctx, PTZVector{}), context.Canceled)

	img := &MockImagingController{Error: context.Canceled}
	_, err := img.GetImagingSettings(ctx)
	require.ErrorIs(t, err, context.Canceled)

	pm := &MockPresetManager{Error: context.Canceled}
	_, err = pm.GetPresets(ctx)
	require.ErrorIs(t, err, context.Canceled)

	es := &MockEventSubscriber{Error: context.Canceled}
	require.ErrorIs(t, es.Subscribe(ctx, "c"), context.Canceled)

	dm := &MockDeviceManager{Error: context.Canceled}
	require.ErrorIs(t, dm.SystemReboot(ctx), context.Canceled)

	sp := &MockSnapshotProvider{Error: context.Canceled}
	_, err = sp.GetSnapshotUri(ctx)
	require.ErrorIs(t, err, context.Canceled)
}
