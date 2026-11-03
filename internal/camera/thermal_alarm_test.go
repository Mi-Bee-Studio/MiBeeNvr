package camera

// Thermal-channel GB alarm linkage: a gb28181.alarm event for a thermal
// camera drives record + an evidence snapshot through the shared dispatcher;
// visible-light GB cameras and unknown channels stay display-only.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/stretchr/testify/require"
)

func TestThermalGBAlarmLinkage(t *testing.T) {
	t.Parallel()
	bus := event.NewEventBus(16)
	cfg := testConfig()
	cfg.Cameras = []config.CameraConfig{
		{
			ID: "gb-hot", Name: "炉体热成像", Protocol: "gb28181", ImagingType: config.ImagingTypeThermal,
			GB28181: config.GB28181ChannelConfig{DeviceID: "34020000001110000001", ChannelID: "34020000001320000001"},
		},
		{
			ID: "gb-vis", Name: "普通GB", Protocol: "gb28181",
			GB28181: config.GB28181ChannelConfig{DeviceID: "34020000001110000002", ChannelID: "34020000001320000002"},
		},
	}
	cm := NewCameraManager(cfg, nil, nil, "")
	cm.eventBus = bus
	cm.armThermalGBAlarmLinkage()

	// The handler runs on the consumer goroutine, assertions on the test
	// goroutine — guard the shared action log.
	var mu sync.Mutex
	var actions []string
	cm.SetMotionActionHandler(func(_, act string, _ time.Duration) {
		mu.Lock()
		defer mu.Unlock()
		actions = append(actions, act)
	})

	alarm := event.GB28181AlarmEvent{
		CameraID: "gb-hot", DeviceID: "34020000001110000001", ChannelID: "34020000001320000001",
		AlarmMethod: "5", AlarmType: "2", AlarmPriority: "4",
	}
	// The consumer subscribes from its own goroutine and the bus never
	// replays to late subscribers — republish until the linkage consumed
	// one (idempotent: repeated alarms legitimately re-trigger).
	require.Eventually(t, func() bool {
		bus.Publish(context.Background(), event.TopicGB28181Alarm, alarm)
		mu.Lock()
		defer mu.Unlock()
		return len(actions) >= 2 && actions[0] == "record" && actions[1] == "snapshot:gb"
	}, 5*time.Second, 100*time.Millisecond, "a thermal camera's GB alarm must drive record + evidence snapshot")

	// Let any in-flight republish drain, then pin the baseline.
	time.Sleep(300 * time.Millisecond)
	mu.Lock()
	base := len(actions)
	mu.Unlock()

	// Visible-light GB camera: display-only, no linkage.
	vis := alarm
	vis.CameraID, vis.DeviceID, vis.ChannelID = "gb-vis", "34020000001110000002", "34020000001320000002"
	bus.Publish(context.Background(), event.TopicGB28181Alarm, vis)

	// Unknown/un-enrolled channel: silent.
	unk := alarm
	unk.CameraID = ""
	bus.Publish(context.Background(), event.TopicGB28181Alarm, unk)

	require.Never(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(actions) > base
	}, 500*time.Millisecond, 50*time.Millisecond, "non-thermal cameras must not trigger linkage")
}
