package camera

// Thermal-channel simulation tests: the dual-spectrum device side is faked
// two ways — (1) wire-level wsnt:Notify bodies through HandleOnvifNotify
// (the push transport's parse path, real SOAP bytes), and (2) the embedded
// onvif-go virtual camera configured with a visible + a thermal profile and
// a live pull-point event service, driving the full subscribe → poll →
// dispatch chain. Together they pin the RadiometryAlarm path end to end
// without any real thermal hardware.

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	onvifserver "github.com/mickeyzzc/onvif-go/v2/server"
	"github.com/stretchr/testify/require"
)

// TestHandleOnvifNotifyRadiometryAlarm pins the wire level: a device-POSTed
// wsnt:Notify carrying tns1:Thermal/RadiometryAlarm parses through the real
// SOAP path and drives the full trigger surface (record + evidence snapshot
// + SSE republish), exactly like a thermal camera posting its verdict.
func TestHandleOnvifNotifyRadiometryAlarm(t *testing.T) {
	t.Parallel()
	bus := event.NewEventBus(16)
	cfg := testConfig()
	cfg.Server.AdvertiseURL = "http://nvr.test:9090"
	cfg.Cameras = []config.CameraConfig{{ID: "cam-t", Name: "配电箱热成像", Protocol: "onvif"}}
	cm := NewCameraManager(cfg, nil, nil, "")
	cm.eventBus = bus

	token := cm.pushTokenFor("cam-t")

	var actions []string
	cm.SetMotionActionHandler(func(_, act string, _ time.Duration) { actions = append(actions, act) })

	ch := make(chan event.Event, 8)
	require.NoError(t, bus.SubscribeByPrefix("onvif.", ch, 8))

	notify := `<?xml version="1.0"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body>
<wsnt:Notify xmlns:wsnt="http://docs.oasis-open.org/wsn/b-2">
 <wsnt:NotificationMessage>
  <wsnt:Topic>tns1:Thermal/RadiometryAlarm</wsnt:Topic>
  <wsnt:Message UtcTime="2026-11-02T08:00:00Z">
   <tt:Source xmlns:tt="http://www.onvif.org/ver10/schema"><tt:SimpleItem Name="Source" Value="ThermalSource_1"/></tt:Source>
   <tt:Data xmlns:tt="http://www.onvif.org/ver10/schema">
    <tt:SimpleItem Name="Active" Value="true"/>
    <tt:SimpleItem Name="Temperature" Value="91.2"/>
   </tt:Data>
  </wsnt:Message>
 </wsnt:NotificationMessage>
</wsnt:Notify></s:Body></s:Envelope>`
	code, msg := cm.HandleOnvifNotify("cam-t", token, []byte(notify))
	require.Equal(t, 200, code)
	require.Empty(t, msg)

	require.Equal(t, []string{"record", "snapshot:onvif"}, actions,
		"an active radiometry alarm must drive the recording trigger plus an evidence snapshot")

	select {
	case evt := <-ch:
		require.Equal(t, "onvif.radiometryalarm", evt.Topic)
		payload := evt.Data.(map[string]any)
		require.Equal(t, "cam-t", payload["camera_id"])
		require.Equal(t, "配电箱热成像", payload["camera_name"])
		data := payload["data"].(map[string]any)
		require.Equal(t, "true", data["Active"])
	case <-time.After(2 * time.Second):
		t.Fatal("radiometry event never reached the SSE feed")
	}
}

// newDualSpectrumSimulator starts the embedded onvif-go virtual camera with
// the dual-spectrum shape: a high-resolution visible-light profile (the one
// automatic selection picks) and a low-resolution thermal profile.
func newDualSpectrumSimulator(t *testing.T) (*onvifserver.Server, string) {
	t.Helper()
	cfg := onvifserver.DefaultConfig()
	cfg.Host = "127.0.0.1"
	cfg.Port = 0 // ephemeral
	cfg.Username = "admin"
	cfg.Password = "sim-pass"
	cfg.SupportEvents = true
	cfg.Profiles = []onvifserver.ProfileConfig{
		{
			Token: "Profile_visual", Name: "Visible Main",
			VideoSource: onvifserver.VideoSourceConfig{
				Token: "vs-0", Name: "Visible",
				Resolution: onvifserver.Resolution{Width: 2560, Height: 1440},
				Framerate:  25,
				Bounds:     onvifserver.Bounds{Width: 2560, Height: 1440},
			},
			VideoEncoder: onvifserver.VideoEncoderConfig{
				Encoding: "H264",
				Resolution: onvifserver.Resolution{
					Width: 2560, Height: 1440,
				},
				Framerate: 25, GovLength: 25, Bitrate: 4096,
			},
		},
		{
			Token: "Profile_thermal", Name: "Thermal",
			VideoSource: onvifserver.VideoSourceConfig{
				Token: "vs-1", Name: "Thermal",
				Resolution: onvifserver.Resolution{Width: 384, Height: 288},
				Framerate:  9,
				Bounds:     onvifserver.Bounds{Width: 384, Height: 288},
			},
			VideoEncoder: onvifserver.VideoEncoderConfig{
				Encoding: "H264",
				Resolution: onvifserver.Resolution{
					Width: 384, Height: 288,
				},
				Framerate: 9, GovLength: 9, Bitrate: 512,
			},
		},
	}

	// Port 0 would leak into the advertised service XAddrs (the simulator
	// publishes config.Port, not the ephemeral port the listener got) —
	// reserve a concrete free port instead.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())
	cfg.Port = port

	sim, err := onvifserver.New(cfg)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = sim.Start(ctx) }()
	require.Eventually(t, func() bool { return sim.ListenAddr() != "" },
		5*time.Second, 50*time.Millisecond, "simulator listener must come up")
	return sim, "http://" + sim.ListenAddr() + "/onvif/device_service"
}

// TestRadiometryFullChainAgainstSimulator drives the production pull-point
// path against the embedded virtual camera: EnsureMotionSubscription
// subscribes through the per-camera ONVIF client, the simulator's
// PublishEvent injects a RadiometryAlarm, and the verdict must surface as
// dispatcher actions plus the onvif.radiometryalarm SSE event.
func TestRadiometryFullChainAgainstSimulator(t *testing.T) {
	sim, endpoint := newDualSpectrumSimulator(t)

	bus := event.NewEventBus(16)
	cfg := testConfig()
	cfg.Cameras = []config.CameraConfig{{
		ID: "cam-sim", Name: "双光谱模拟", Protocol: "onvif",
		ONVIFEndpoint: endpoint, Username: "admin", Password: "sim-pass",
		MotionSource: config.MotionSourceCameraONVIF,
		ImagingType:  config.ImagingTypeThermal,
	}}
	cm := NewCameraManager(cfg, nil, nil, "")
	cm.eventBus = bus

	var actions []string
	cm.SetMotionActionHandler(func(_, act string, _ time.Duration) { actions = append(actions, act) })

	ch := make(chan event.Event, 8)
	require.NoError(t, bus.SubscribeByPrefix("onvif.", ch, 8))

	cm.EnsureMotionSubscription(context.Background(), cfg.Cameras[0])
	require.Eventually(t, func() bool {
		return cm.ONVIFEventsStatus("cam-sim").Subscribed
	}, 15*time.Second, 200*time.Millisecond, "pull-point subscription against the simulator must come up")

	sim.PublishEvent(onvifserver.Event{
		Topic:  "tns1:Thermal/RadiometryAlarm",
		Source: []onvifserver.SimpleItem{{Name: "Source", Value: "ThermalSource_1"}},
		Data:   []onvifserver.SimpleItem{{Name: "Active", Value: "true"}, {Name: "Temperature", Value: "88.4"}},
	})

	require.Eventually(t, func() bool {
		return len(actions) >= 2 && actions[0] == "record" && actions[1] == "snapshot:onvif"
	}, 15*time.Second, 200*time.Millisecond, "the alarm must drive record + evidence snapshot through the pull-point chain")

	select {
	case evt := <-ch:
		require.Equal(t, "onvif.radiometryalarm", evt.Topic)
		payload := evt.Data.(map[string]any)
		require.Equal(t, "cam-sim", payload["camera_id"])
		data := payload["data"].(map[string]any)
		require.Equal(t, "true", data["Active"])
		require.Equal(t, "88.4", data["Temperature"])
	case <-time.After(5 * time.Second):
		t.Fatal("radiometry event never reached the SSE feed")
	}
}
