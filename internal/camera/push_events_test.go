package camera

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/onvif"

	"github.com/stretchr/testify/require"
)

func TestEnsureMotionSubscriptionPrefersPush(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	cfg.Server.AdvertiseURL = "http://nvr.test:9090"
	cm := NewCameraManager(cfg, nil, nil, "")

	var pushCalls, pullCalls int
	cm.SetPushSubscriberFactory(func(ctx context.Context, cameraID, notifyURL string, cb onvif.EventCallback, onFallback func(string, string)) (onvif.EventSubscriber, error) {
		pushCalls++
		require.Equal(t, "http://nvr.test:9090/api/onvif/notify/cam-o/", notifyURL[:len("http://nvr.test:9090/api/onvif/notify/cam-o/")])
		return &onvif.MockEventSubscriber{}, nil
	})
	cm.SetEventSubscriberFactory(func(ctx context.Context, cameraID string, cb onvif.EventCallback) (onvif.EventSubscriber, error) {
		pullCalls++
		return &onvif.MockEventSubscriber{}, nil
	})

	cm.EnsureMotionSubscription(context.Background(), config.CameraConfig{
		ID: "cam-o", Protocol: "onvif", MotionSource: config.MotionSourceCameraONVIF,
	})
	require.Equal(t, 1, pushCalls, "advertise_url set → push transport first")
	require.Equal(t, 0, pullCalls, "push success must not start pull-point")

	// Reconcile again: subscriber exists, no further probes.
	cm.EnsureMotionSubscription(context.Background(), config.CameraConfig{
		ID: "cam-o", Protocol: "onvif", MotionSource: config.MotionSourceCameraONVIF,
	})
	require.Equal(t, 1, pushCalls)
}

func TestEnsureMotionSubscriptionWithoutAdvertiseURLStaysPull(t *testing.T) {
	t.Parallel()
	cfg := testConfig() // no advertise_url
	cm := NewCameraManager(cfg, nil, nil, "")

	var pushCalls, pullCalls int
	cm.SetPushSubscriberFactory(func(ctx context.Context, cameraID, notifyURL string, cb onvif.EventCallback, onFallback func(string, string)) (onvif.EventSubscriber, error) {
		pushCalls++
		return &onvif.MockEventSubscriber{}, nil
	})
	cm.SetEventSubscriberFactory(func(ctx context.Context, cameraID string, cb onvif.EventCallback) (onvif.EventSubscriber, error) {
		pullCalls++
		return &onvif.MockEventSubscriber{}, nil
	})

	cm.EnsureMotionSubscription(context.Background(), config.CameraConfig{
		ID: "cam-o", Protocol: "onvif", MotionSource: config.MotionSourceCameraONVIF,
	})
	require.Equal(t, 0, pushCalls, "no advertise_url → push never attempted")
	require.Equal(t, 1, pullCalls)
}

func TestEnsureMotionSubscriptionPushDeclineFallsBackToPull(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	cfg.Server.AdvertiseURL = "http://nvr.test:9090"
	cm := NewCameraManager(cfg, nil, nil, "")

	declined := &onvif.MockEventSubscriber{Error: fmt.Errorf("%w: not supported", onvif.ErrPushNotSupported)}
	pullSub := &onvif.MockEventSubscriber{}
	var pullCalls int
	cm.SetPushSubscriberFactory(func(ctx context.Context, cameraID, notifyURL string, cb onvif.EventCallback, onFallback func(string, string)) (onvif.EventSubscriber, error) {
		return declined, nil
	})
	cm.SetEventSubscriberFactory(func(ctx context.Context, cameraID string, cb onvif.EventCallback) (onvif.EventSubscriber, error) {
		pullCalls++
		return pullSub, nil
	})

	cam := config.CameraConfig{ID: "cam-o", Protocol: "onvif", MotionSource: config.MotionSourceCameraONVIF}
	cm.EnsureMotionSubscription(context.Background(), cam)
	require.Equal(t, 1, declined.SubscribeCalls)
	require.Equal(t, 1, pullCalls, "Sender-fault decline must fall through to pull-point")
	require.True(t, cm.ONVIFEventsStatus("cam-o").Subscribed)

	// The decline tombstone holds for the process until teardown: tearing the
	// (pull) subscriber down clears it, so a later reconcile re-probes push.
	cm.UnsubscribeONVIFEvents(context.Background(), "cam-o")
	require.Empty(t, cm.pushDeclinedReason("cam-o"))
}

func TestPushFallbackDegradesToPull(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	cfg.Server.AdvertiseURL = "http://nvr.test:9090"
	// The degrade path re-runs the reconcile from the manager's own config
	// snapshot, so the camera must be registered there.
	cfg.Cameras = []config.CameraConfig{{ID: "cam-o", Protocol: "onvif", MotionSource: config.MotionSourceCameraONVIF}}
	cm := NewCameraManager(cfg, nil, nil, "")

	var mu sync.Mutex
	var onFallback func(cameraID, reason string)
	pushSub := &onvif.MockEventSubscriber{}
	pullSub := &onvif.MockEventSubscriber{}
	cm.SetPushSubscriberFactory(func(ctx context.Context, cameraID, notifyURL string, cb onvif.EventCallback, onFallback_ func(string, string)) (onvif.EventSubscriber, error) {
		mu.Lock()
		onFallback = onFallback_
		mu.Unlock()
		return pushSub, nil
	})
	cm.SetEventSubscriberFactory(func(ctx context.Context, cameraID string, cb onvif.EventCallback) (onvif.EventSubscriber, error) {
		return pullSub, nil
	})

	cam := config.CameraConfig{ID: "cam-o", Protocol: "onvif", MotionSource: config.MotionSourceCameraONVIF}
	cm.EnsureMotionSubscription(context.Background(), cam)
	require.Equal(t, 1, pushSub.SubscribeCalls)

	mu.Lock()
	fb := onFallback
	mu.Unlock()
	require.NotNil(t, fb, "factory must receive the fallback hook")

	fb("cam-o", "renew failed")

	// The degrade tears the push subscriber down and the re-run reconcile
	// takes the pull path (tombstoned). The async goroutine writes the mock
	// counters under the mock's lock — poll through the race-safe accessors.
	require.Eventually(t, func() bool {
		return pullSub.SubscribeCallCount() == 1 && pushSub.UnsubscribeCallCount() == 1
	}, 5*time.Second, 100*time.Millisecond, "degrade must switch to pull-point transport")
	require.Equal(t, 1, pushSub.SubscribeCallCount(), "declined camera must not re-probe push")

	st := cm.ONVIFEventsStatus("cam-o")
	require.True(t, st.Subscribed)
}

func TestHandleOnvifNotify(t *testing.T) {
	t.Parallel()
	bus := event.NewEventBus(16)
	cfg := testConfig()
	cfg.Server.AdvertiseURL = "http://nvr.test:9090"
	cfg.Cameras = []config.CameraConfig{{ID: "cam-o", Name: "门口", Protocol: "onvif"}}
	cm := NewCameraManager(cfg, nil, nil, "")
	cm.eventBus = bus

	token := cm.pushTokenFor("cam-o")
	require.Len(t, token, 32, "16 random bytes hex-encoded")

	ch := make(chan event.Event, 8)
	require.NoError(t, bus.SubscribeByPrefix("onvif.", ch, 16))

	// Unknown camera / wrong token → 404, and the two are indistinguishable.
	code, _ := cm.HandleOnvifNotify("cam-o", "deadbeef", []byte(`<x/>`))
	require.Equal(t, 404, code)
	code, _ = cm.HandleOnvifNotify("cam-unknown", token, []byte(`<x/>`))
	require.Equal(t, 404, code)

	// Valid token + MotionAlarm true → 200 and the event flows the same path
	// as pull-point events (SSE publish).
	notify := `<?xml version="1.0"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body>
<wsnt:Notify xmlns:wsnt="http://docs.oasis-open.org/wsn/b-2">
 <wsnt:NotificationMessage>
  <wsnt:Topic>tns1:VideoSource/MotionAlarm</wsnt:Topic>
  <wsnt:Message UtcTime="2026-09-30T08:00:00Z">
   <tt:Data xmlns:tt="http://www.onvif.org/ver10/schema"><tt:SimpleItem Name="State" Value="true"/></tt:Data>
  </wsnt:Message>
 </wsnt:NotificationMessage>
</wsnt:Notify></s:Body></s:Envelope>`
	code, msg := cm.HandleOnvifNotify("cam-o", token, []byte(notify))
	require.Equal(t, 200, code)
	require.Empty(t, msg)
	select {
	case evt := <-ch:
		require.Equal(t, "onvif.motionalarm", evt.Topic)
		payload := evt.Data.(map[string]any)
		require.Equal(t, "cam-o", payload["camera_id"])
	case <-time.After(2 * time.Second):
		t.Fatal("pushed event must be republished on the SSE bus")
	}

	// Empty Notify (device heartbeat) → 200 without events.
	code, _ = cm.HandleOnvifNotify("cam-o", token, []byte(`<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><wsnt:Notify xmlns:wsnt="http://docs.oasis-open.org/wsn/b-2"/></s:Body></s:Envelope>`))
	require.Equal(t, 200, code)

	// Malformed body → 400.
	code, _ = cm.HandleOnvifNotify("cam-o", token, []byte(`not xml at all`))
	require.Equal(t, 400, code)
}

func TestPushTokenStableAcrossTeardowns(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	cm := NewCameraManager(cfg, nil, nil, "")
	first := cm.pushTokenFor("cam-o")
	second := cm.pushTokenFor("cam-o")
	require.Equal(t, first, second)
	require.NotEqual(t, first, cm.pushTokenFor("cam-p"), "tokens must differ per camera")
}
