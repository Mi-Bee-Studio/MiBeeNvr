package camera

import (
	"bytes"
	"context"
	"log/slog"
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

func (f *fakeMotionRecorder) Start(ctx context.Context) error { return nil }
func (f *fakeMotionRecorder) Stop() error                     { return nil }
func (f *fakeMotionRecorder) Status() model.RecorderStatus    { return model.StatusRecording }
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
	cm.SetMotionActionHandler(func(cameraID, act string, _ time.Duration) { actionCam, action = cameraID, act })

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

// TestOnRadiometryEventDispatchesPublishesAndTriggers mirrors the motion
// dispatch test for the thermal RadiometryAlarm path: an active alarm drives
// the record action PLUS an evidence snapshot (onvif-labelled) and the
// adaptive exit; a clear triggers nothing; the SSE republish rides the same
// onvif.* feed.
func TestOnRadiometryEventDispatchesPublishesAndTriggers(t *testing.T) {
	t.Parallel()
	bus := event.NewEventBus(16)
	cfg := testConfig()
	cfg.Cameras = []config.CameraConfig{{
		ID: "cam-t", Name: "配电箱热成像", Protocol: "onvif",
		MotionSource: config.MotionSourceCameraONVIF,
		ImagingType:  config.ImagingTypeThermal,
	}}
	cm := NewCameraManager(cfg, nil, nil, "")
	cm.eventBus = bus

	rec := &fakeMotionRecorder{}
	cm.apply(func(s *snapshot) *snapshot {
		s.recorders["cam-t"] = rec
		return s
	})

	var actions []string
	var actionCam string
	cm.SetMotionActionHandler(func(cameraID, act string, _ time.Duration) {
		actionCam = cameraID
		actions = append(actions, act)
	})

	ch := make(chan event.Event, 8)
	require.NoError(t, bus.SubscribeByPrefix("onvif.", ch, 16))

	// RadiometryAlarm true → SSE publish + record + snapshot + adaptive trigger.
	cm.onONVIFEvent("cam-t", onvif.ONVIFEvent{
		Topic: "tns1:Thermal/RadiometryAlarm",
		Data:  map[string]any{"Active": "true", "Temperature": "91.2"},
	})

	require.Equal(t, "cam-t", actionCam)
	require.Equal(t, []string{"record", "snapshot:onvif"}, actions)
	require.Equal(t, 1, rec.motionCalls)
	require.Equal(t, motionTriggerHold, rec.lastHold)

	evt := <-ch
	require.Equal(t, "onvif.radiometryalarm", evt.Topic)
	payload, ok := evt.Data.(map[string]any)
	require.True(t, ok)
	require.Equal(t, "cam-t", payload["camera_id"])
	require.Equal(t, "配电箱热成像", payload["camera_name"])

	// Clear → published to SSE but triggers NOTHING.
	actions = nil
	cm.onONVIFEvent("cam-t", onvif.ONVIFEvent{
		Topic: "tns1:Thermal/RadiometryAlarm",
		Data:  map[string]any{"Active": "false"},
	})
	require.Empty(t, actions, "radiometry clear must not dispatch anything")
	require.Equal(t, 1, rec.motionCalls)
	<-ch
}

// TestOnRadiometryEventWarnsOnUnparseable pins the same observability
// contract as motion: a radiometry-shaped event whose state is unparseable
// must WARN (vendor encoding mismatch ≠ device emits nothing), while other
// unparseable topics stay silent.
func TestOnRadiometryEventWarnsOnUnparseable(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	defer slog.SetDefault(prev)

	cm := NewCameraManager(testConfig(), nil, nil, "")
	buf.Reset() // construction warnings are not under test

	cm.onONVIFEvent("cam-t", onvif.ONVIFEvent{
		Topic: "tns1:Thermal/RadiometryAlarm",
		Data:  map[string]any{"Temperature": "91.2"},
	})
	require.Contains(t, buf.String(), "level=WARN")
	require.Contains(t, buf.String(), "RadiometryAlarm")

	buf.Reset()
	cm.onONVIFEvent("cam-t", onvif.ONVIFEvent{
		Topic: "tns1:Thermal/Temperature",
		Data:  map[string]any{"Value": "91.2"},
	})
	require.NotContains(t, buf.String(), "level=WARN")
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

// TestOnONVIFEventWarnsOnUnparseableMotionAlarm pins the #711 observability
// gap: a MotionAlarm-shaped event whose State is missing or not a bool was
// dropped with NO log line — on a device encoding mismatch the true-leg
// silently never fires and production cannot tell "nothing emitted" from
// "emitted but unparsed". Must WARN with the raw payload; non-MotionAlarm
// topics stay silent (they are not our contract).
func TestOnONVIFEventWarnsOnUnparseableMotionAlarm(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	defer slog.SetDefault(prev)

	cm := NewCameraManager(testConfig(), nil, nil, "")
	buf.Reset() // construction warnings are not under test

	// State missing → ParseMotionAlarm !ok on a MotionAlarm topic.
	cm.onONVIFEvent("cam-m", onvif.ONVIFEvent{
		Topic: "tns1:VideoSource/MotionAlarm",
		Data:  map[string]any{"Score": "73"},
	})
	require.Contains(t, buf.String(), "level=WARN")
	require.Contains(t, buf.String(), "MotionAlarm")
	require.Contains(t, buf.String(), "Score")

	// Non-MotionAlarm unparseable topics are not our contract — silent.
	buf.Reset()
	cm.onONVIFEvent("cam-m", onvif.ONVIFEvent{
		Topic: "tns1:Device/Tamper",
		Data:  map[string]any{"Score": "73"},
	})
	require.NotContains(t, buf.String(), "level=WARN")
}
