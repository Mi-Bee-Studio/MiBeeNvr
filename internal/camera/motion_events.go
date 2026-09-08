package camera

import (
	"context"
	"strings"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/onvif"
)

// Camera-side motion events (#711): a camera with motion_source: camera:onvif
// runs a Pull-Point subscription against its ONVIF event service. Received
// events are (a) republished to the SSE event bus under onvif.* topics (the
// web UI's ONVIF events panel), and (b) MotionAlarm State=true drives the
// recorder: the shared trigger dispatcher action surface (record — identical
// semantics to the MQTT/webhook triggers) plus, for adaptive cameras, a
// timelapse exit with reason=onvif_motion (the same path as the audio and
// pixel triggers). State=false intentionally triggers nothing: the adaptive
// gate's calm logic re-enters timelapse on its own, and a dispatcher "stop"
// would kill recording sessions other sources started.
//
// mibee_cam contract notes pinned here (v1.5 §13): single-subscription model
// (a new CreatePullPointSubscription replaces the old one — later client
// wins), TerminationTime granted at 1h with 120s no-pull expiry (the
// subscriber's supervised loop rebuilds), per-subscription queue depth 12 with
// drop-oldest overflow (events lost while the NVR is down are NOT replayed —
// recording decisions must not depend on backfill).

// motionTriggerHold is how long a confirmed MotionAlarm defers timelapse
// re-entry on adaptive cameras: bridges inter-poll gaps and the occasional
// dropped clear, without pinning the camera in full-rate forever.
const motionTriggerHold = 10 * time.Second

// onvifEventTopic maps a device topic ("tns1:VideoSource/MotionAlarm") to the
// event-bus topic ("onvif.motionalarm") the SSE feed and the web UI's
// ONVIFEvents panel consume (they match the "onvif." prefix).
func onvifEventTopic(deviceTopic string) string {
	leaf := deviceTopic
	if idx := strings.LastIndex(leaf, "/"); idx >= 0 {
		leaf = leaf[idx+1:]
	}
	// Strip a trailing namespace prefix ("tns1:Device" → "device") — the
	// leaf after the last ":" is the event name.
	if idx := strings.LastIndex(leaf, ":"); idx >= 0 {
		leaf = leaf[idx+1:]
	}
	leaf = strings.ToLower(strings.TrimSpace(leaf))
	if leaf == "" {
		return ""
	}
	return "onvif." + leaf
}

// motionTriggerable is the recorder surface a MotionAlarm drives (satisfied by
// *recorder.ONVIFRecorder via delegate forwarding; interface-local so the
// camera package does not import recorder).
type motionTriggerable interface {
	MotionTriggerEvent(at time.Time, hold time.Duration) error
}

// SetMotionActionHandler wires the shared trigger dispatcher (the same
// mqtt.NewActionDispatcher instance the MQTT and webhook triggers use). Called
// from pkg/app wiring after the dispatcher is built — the camera manager owns
// the recorders, the dispatcher owns the camera manager, so the handler is
// injected rather than constructed here.
func (cm *CameraManager) SetMotionActionHandler(h func(cameraID, action string)) {
	cm.motionAction = h
}

// SetEventSubscriberFactory overrides how ONVIF event subscribers are built
// (test seam; production path goes through the per-camera ONVIF client).
func (cm *CameraManager) SetEventSubscriberFactory(f func(ctx context.Context, cameraID string, cb onvif.EventCallback) (onvif.EventSubscriber, error)) {
	cm.eventSubscriberFactory = f
}

// EnsureMotionSubscription reconciles the camera's Pull-Point subscription
// with its motion_source setting: subscribe when camera:onvif is active on an
// ONVIF camera, tear down otherwise. Idempotent — called at boot, after
// camera updates, and after archive/restore.
func (cm *CameraManager) EnsureMotionSubscription(ctx context.Context, cam config.CameraConfig) {
	want := cam.MotionSource == config.MotionSourceCameraONVIF &&
		cam.Protocol == string(model.ProtoONVIF) &&
		cam.ActivationState != "pending_activation"

	cm.onvifMu.Lock()
	_, exists := cm.eventSubscribers[cam.ID]
	factory := cm.eventSubscriberFactory
	cm.onvifMu.Unlock()

	if !want {
		if exists {
			_ = cm.UnsubscribeONVIFEvents(ctx, cam.ID)
		}
		return
	}
	if exists {
		return
	}

	cameraID := cam.ID
	cb := func(evt onvif.ONVIFEvent) { cm.onONVIFEvent(cameraID, evt) }

	if factory != nil {
		sub, err := factory(ctx, cameraID, cb)
		if err != nil {
			logger.Warn("camera:onvif motion subscription failed",
				"camera_id", cameraID, "error", err)
			return
		}
		if err := sub.Subscribe(ctx, cameraID); err != nil {
			logger.Info("camera:onvif motion subscription not established",
				"camera_id", cameraID, "error", err)
			return
		}
		cm.onvifMu.Lock()
		cm.eventSubscribers[cameraID] = sub
		cm.onvifMu.Unlock()
		logger.Info("subscribed to camera-side ONVIF motion events", "camera_id", cameraID)
		return
	}

	// Production path: per-camera ONVIF client → subscriber. 1s poll keeps
	// trigger latency low (mibee_cam contract suggests 0.5–1s; the 120s
	// device-side expiry gives huge headroom).
	if err := cm.SubscribeONVIFEvents(ctx, cameraID, cb, onvif.WithPollInterval(time.Second)); err != nil {
		logger.Info("camera:onvif motion subscription not established",
			"camera_id", cameraID, "error", err)
	}
}

// ONVIFEventsStatus reports the per-camera subscription diagnostics.
func (cm *CameraManager) ONVIFEventsStatus(cameraID string) onvif.EventSubscriptionStatus {
	cm.onvifMu.Lock()
	sub := cm.eventSubscribers[cameraID]
	cm.onvifMu.Unlock()
	if sub == nil {
		return onvif.EventSubscriptionStatus{}
	}
	return sub.Status(cameraID)
}

// onONVIFEvent is the per-camera callback for every event the PullPoint
// subscription delivers: SSE republish + MotionAlarm handling.
func (cm *CameraManager) onONVIFEvent(cameraID string, evt onvif.ONVIFEvent) {
	// (a) Republish under onvif.* for the SSE feed / UI events panel.
	if topic := onvifEventTopic(evt.Topic); topic != "" && cm.eventBus != nil {
		payload := map[string]any{
			"camera_id": cameraID,
			"topic":     evt.Topic,
			"timestamp": evt.Timestamp,
			"data":      evt.Data,
		}
		if cam := cm.snapshotConfig(cameraID); cam != nil {
			payload["camera_name"] = cam.Name
		}
		cm.eventBus.Publish(context.Background(), topic, payload)
	}

	// (b) MotionAlarm → recorder.
	ma, ok := onvif.ParseMotionAlarm(evt)
	if !ok {
		return
	}
	if !ma.Active {
		// Clears trigger nothing (see file comment); logged for arrival-rate
		// diagnosis — the 12-deep device queue drops oldest, so a clear can
		// also arrive without its pairing true.
		logger.Debug("onvif motion cleared", "camera_id", cameraID,
			"source", ma.Source, "score", ma.Score)
		return
	}

	logger.Info("onvif motion alarm",
		"camera_id", cameraID, "source", ma.Source, "score", ma.Score)

	// Same action surface as the MQTT (#660) and webhook (#709) triggers.
	if cm.motionAction != nil {
		cm.motionAction(cameraID, "record")
	}

	// Adaptive cameras: exit timelapse now (GOP + pre-capture backfill), the
	// natural "有人才拍摄" path — the gate re-enters timelapse via its calm
	// logic once motion stops.
	if rec := cm.GetRecorder(cameraID); rec != nil {
		if trig, ok := rec.(motionTriggerable); ok {
			if err := trig.MotionTriggerEvent(time.Now(), motionTriggerHold); err != nil {
				logger.Debug("onvif motion trigger not applied (not adaptive)",
					"camera_id", cameraID, "error", err)
			}
		}
	}
}
