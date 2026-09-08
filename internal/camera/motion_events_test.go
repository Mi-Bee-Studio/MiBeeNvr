package camera

import (
	"context"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/onvif"

	"github.com/stretchr/testify/require"
)

func TestOnvifEventTopicMapping(t *testing.T) {
	t.Parallel()
	require.Equal(t, "onvif.motionalarm", onvifEventTopic("tns1:VideoSource/MotionAlarm"))
	require.Equal(t, "onvif.tamper", onvifEventTopic("tns1:Device/Tamper"))
	require.Equal(t, "", onvifEventTopic("tns1:"))
	require.Equal(t, "", onvifEventTopic(""))
}

// fakeMotionRecorder satisfies the recorder registry for onONVIFEvent.
type fakeMotionRecorder struct {
	motionCalls int
	lastHold    time.Duration
}

func (f *fakeMotionRecorder) Start(ctx context.Context) error  { return nil }
func (f *fakeMotionRecorder) Stop() error                      { return nil }
func (f *fakeMotionRecorder) Status() model.RecorderStatus     { return model.StatusRecording }
func (f *fakeMotionRecorder) MotionTriggerEvent(at time.Time, hold time.Duration) error {
	f.motionCalls++
	f.lastHold = hold
	return nil
}

func TestOnMotionEventDispatchesPublishesAndTriggers(t *testing.T) {
	t.Parallel()
	bus := event.NewEventBus(16)
	cfg := testConfig()
	cfg.Cameras = []config.CameraConfig{{
		ID: "cam-m", Name: "门口", Protocol: "onvif",
		MotionSource: config.MotionSourceCameraONVIF,
	}}
	cm := NewCameraManager(cfg, nil, nil, "")
	cm.eventBus = bus

	rec := &fakeMotionRecorder{}
	cm.apply(func(s *snapshot) *snapshot {
		s.recorders["cam-m"] = rec
		return s
	})

	var actionCam, action string
	cm.SetMotionActionHandler(func(cameraID, act string) { actionCam, action = cameraID, act })

	ch := make(chan event.Event, 8)
	require.NoError(t, bus.SubscribeByPrefix("onvif.", ch, 16))

	// MotionAlarm true → SSE publish + dispatcher record + adaptive trigger.
	cm.onONVIFEvent("cam-m", onvif.ONVIFEvent{
		Topic: "tns1:VideoSource/MotionAlarm",
		Data:  map[string]any{"State": "true", "Score": "73", "source.Source": "CSI"},
	})

	require.Equal(t, "cam-m", actionCam)
	require.Equal(t, "record", action)
	require.Equal(t, 1, rec.motionCalls)
	require.Equal(t, motionTriggerHold, rec.lastHold)

	evt := <-ch
	require.Equal(t, "onvif.motionalarm", evt.Topic)
	payload, ok := evt.Data.(map[string]any)
	require.True(t, ok)
	require.Equal(t, "cam-m", payload["camera_id"])
	require.Equal(t, "门口", payload["camera_name"])
	data := payload["data"].(map[string]any)
	require.Equal(t, "true", data["State"])
	require.Equal(t, "73", data["Score"])

	// Clear event → published to SSE but triggers NOTHING.
	action = ""
	cm.onONVIFEvent("cam-m", onvif.ONVIFEvent{
		Topic: "tns1:VideoSource/MotionAlarm",
		Data:  map[string]any{"State": "false"},
	})
	require.Equal(t, "", action, "clear must not dispatch a stop")
	require.Equal(t, 1, rec.motionCalls)
	<-ch

	// Non-motion ONVIF topic → SSE publish only.
	cm.onONVIFEvent("cam-m", onvif.ONVIFEvent{
		Topic: "tns1:Device/Tamper",
		Data:  map[string]any{"State": "true"},
	})
	require.Equal(t, "", action)
	require.Equal(t, 1, rec.motionCalls, "non-motion topics must not trigger")
	<-ch
}

func TestEnsureMotionSubscriptionGating(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	cm := NewCameraManager(cfg, nil, nil, "")

	var factoryCalls int
	sub := &onvif.MockEventSubscriber{}
	cm.SetEventSubscriberFactory(func(ctx context.Context, cameraID string, cb onvif.EventCallback) (onvif.EventSubscriber, error) {
		factoryCalls++
		return sub, nil
	})

	onvifCam := config.CameraConfig{ID: "cam-o", Protocol: "onvif", MotionSource: config.MotionSourceCameraONVIF}
	httpCam := config.CameraConfig{ID: "cam-h", Protocol: "http", MotionSource: config.MotionSourceCameraONVIF}
	plainCam := config.CameraConfig{ID: "cam-p", Protocol: "onvif"}

	// Non-onvif protocol: no subscription even with the flag set.
	cm.EnsureMotionSubscription(context.Background(), httpCam)
	require.Equal(t, 0, factoryCalls)

	// Default (no motion_source): no subscription.
	cm.EnsureMotionSubscription(context.Background(), plainCam)
	require.Equal(t, 0, factoryCalls)

	// onvif + camera:onvif: subscribed once.
	cm.EnsureMotionSubscription(context.Background(), onvifCam)
	require.Equal(t, 1, factoryCalls)
	require.True(t, cm.ONVIFEventsStatus("cam-o").Subscribed)

	// Reconcile again: no duplicate.
	cm.EnsureMotionSubscription(context.Background(), onvifCam)
	require.Equal(t, 1, factoryCalls)

	// Switching back to nvr tears it down.
	off := onvifCam
	off.MotionSource = ""
	cm.EnsureMotionSubscription(context.Background(), off)
	cm.onvifMu.Lock()
	_, exists := cm.eventSubscribers["cam-o"]
	cm.onvifMu.Unlock()
	require.False(t, exists, "motion_source=nvr must tear the subscription down")
	require.Equal(t, 1, sub.UnsubscribeCalls)
}

func TestONVIFEventsStatusEmpty(t *testing.T) {
	t.Parallel()
	cm := NewCameraManager(testConfig(), nil, nil, "")
	st := cm.ONVIFEventsStatus("nope")
	require.False(t, st.Subscribed)
	require.Empty(t, st.State)
}

// A failed subscription is discarded with its subscriber — the manager-level
// error memory must still surface "unsupported" / "resubscribing" in the
// diagnostics so the UI can tell a dead subscription from a never-attempted
// one (#711).
func TestONVIFEventsStatusRemembersFailure(t *testing.T) {
	t.Parallel()
	cm := NewCameraManager(testConfig(), nil, nil, "")

	cm.onvifMu.Lock()
	cm.motionSubErrors["cam-x"] = "onvif: device does not support event pull-point subscription: Action not supported"
	cm.motionSubErrors["cam-y"] = "dial tcp timeout"
	cm.onvifMu.Unlock()

	require.Equal(t, onvif.StateUnsupported, cm.ONVIFEventsStatus("cam-x").State)
	require.NotEmpty(t, cm.ONVIFEventsStatus("cam-x").LastError)
	require.Equal(t, onvif.StateResubscribing, cm.ONVIFEventsStatus("cam-y").State)
}
