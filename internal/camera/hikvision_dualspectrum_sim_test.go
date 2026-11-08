package camera

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	onvifserver "github.com/mickeyzzc/onvif-go/v2/server"
	"github.com/stretchr/testify/require"
)

// TestRuleEngineTemperatureAlarmAgainstSimulator replays the temperature
// alarm the way a shipping thermographic camera emits it (Hikvision
// DS-2TB2617, from its GetEventProperties): tns1:RuleEngine/Radiometry/
// TemperatureAlarm, Source VideoSourceToken/VideoAnalyticsConfigurationToken/
// RuleName, Data AlarmActive. It must drive record + evidence snapshot like
// the Thermal-service RadiometryAlarm does — before the bridge it fell
// through as an unknown topic and the recorder never heard of it.
func TestRuleEngineTemperatureAlarmAgainstSimulator(t *testing.T) {
	sim, endpoint := newDualSpectrumSimulator(t)

	bus := event.NewEventBus(16)
	cfg := testConfig()
	cfg.Cameras = []config.CameraConfig{{
		ID: "cam-hik", Name: "海康双光谱", Protocol: "onvif",
		ONVIFEndpoint: endpoint, Username: "admin", Password: "sim-pass",
		MotionSource: config.MotionSourceCameraONVIF,
		ImagingType:  config.ImagingTypeThermal,
	}}
	cm := NewCameraManager(cfg, nil, nil, "")
	cm.eventBus = bus

	var mu sync.Mutex
	var actions []string
	cm.SetMotionActionHandler(func(_, act string, _ time.Duration) {
		mu.Lock()
		defer mu.Unlock()
		actions = append(actions, act)
	})
	ch := make(chan event.Event, 8)
	require.NoError(t, bus.SubscribeByPrefix("onvif.", ch, 8))

	cm.EnsureMotionSubscription(context.Background(), cfg.Cameras[0])
	require.Eventually(t, func() bool {
		return cm.ONVIFEventsStatus("cam-hik").Subscribed
	}, 15*time.Second, 200*time.Millisecond, "pull-point subscription must come up")

	sim.PublishEvent(onvifserver.Event{
		Topic: "tns1:RuleEngine/Radiometry/TemperatureAlarm",
		Source: []onvifserver.SimpleItem{
			{Name: "VideoSourceToken", Value: "VideoSource_1"},
			{Name: "VideoAnalyticsConfigurationToken", Value: "VideoAnalyticsToken"},
			{Name: "RuleName", Value: "face_1"},
		},
		Data: []onvifserver.SimpleItem{
			{Name: "AlarmActive", Value: "true"},
			{Name: "TimeStamp", Value: "2026-11-07T10:30:00Z"},
		},
	})

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(actions) >= 2 && actions[0] == "record" && actions[1] == "snapshot:onvif"
	}, 15*time.Second, 200*time.Millisecond, "the rule-engine temperature alarm must drive record + evidence snapshot")

	select {
	case evt := <-ch:
		require.Equal(t, "onvif.temperaturealarm", evt.Topic)
		payload := evt.Data.(map[string]any)
		require.Equal(t, "cam-hik", payload["camera_id"])
	case <-time.After(5 * time.Second):
		t.Fatal("temperature alarm never reached the SSE feed")
	}
}

// TestSnapshotURLFollowsCameraProfile: the add/activate-time snapshot-URL
// fill must ask for the camera's OWN media profile. On a dual-spectrum
// device profiles[0] is the visible lens — the thermal channel used to get a
// visible-light snapshot URL.
func TestSnapshotURLFollowsCameraProfile(t *testing.T) {
	_, endpoint := newDualSpectrumSimulator(t)

	cfg := testConfig()
	cfg.Cameras = []config.CameraConfig{{
		ID: "cam-thermal", Name: "热成像通道", Protocol: "onvif",
		ONVIFEndpoint: endpoint, Username: "admin", Password: "sim-pass",
		ProfileToken: "Profile_thermal",
		ImagingType:  config.ImagingTypeThermal,
	}}
	cm := NewCameraManager(cfg, nil, nil, "")
	cm.autoPopulateSnapshotURL(context.Background(), "cam-thermal")

	got := cm.GetCameraConfig("cam-thermal").SnapshotURL
	require.NotEmpty(t, got, "snapshot URL must be filled")
	require.True(t, strings.Contains(got, "Profile_thermal"), "snapshot URL must target the thermal profile, got %q", got)
}
