package camera

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
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
func (cm *CameraManager) SetMotionActionHandler(h func(cameraID, action string, duration time.Duration)) {
	cm.motionAction = h
}

// SetEventSubscriberFactory overrides how ONVIF event subscribers are built
// (test seam; production path goes through the per-camera ONVIF client).
func (cm *CameraManager) SetEventSubscriberFactory(f func(ctx context.Context, cameraID string, cb onvif.EventCallback) (onvif.EventSubscriber, error)) {
	cm.eventSubscriberFactory = f
}

// SetPushSubscriberFactory overrides how push event subscribers are built
// (test seam for the #922 push transport; production path goes through the
// per-camera ONVIF client).
func (cm *CameraManager) SetPushSubscriberFactory(f func(ctx context.Context, cameraID, notifyURL string, cb onvif.EventCallback, onFallback func(cameraID, reason string)) (onvif.EventSubscriber, error)) {
	cm.pushSubscriberFactory = f
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

	// Remember the last subscription outcome so the diagnostics endpoint can
	// tell "device does not implement events" from "never attempted" even
	// though a failed subscriber is discarded (its tombstone dies with it).
	recordOutcome := func(err error) {
		cm.onvifMu.Lock()
		if err != nil {
			cm.motionSubErrors[cam.ID] = err.Error()
		} else {
			delete(cm.motionSubErrors, cam.ID)
		}
		cm.onvifMu.Unlock()
	}

	if !want {
		if exists {
			_ = cm.UnsubscribeONVIFEvents(ctx, cam.ID)
		}
		recordOutcome(nil)
		return
	}
	if exists {
		return
	}

	cameraID := cam.ID
	cb := func(evt onvif.ONVIFEvent) { cm.onONVIFEvent(cameraID, evt) }

	// Push transport first (#922): only when the NVR advertises a base URL
	// devices can POST back to, and this camera hasn't already declined push
	// (probe fault or a renew degrade — both tombstoned until teardown).
	if declined := cm.pushDeclinedReason(cameraID); declined == "" && cm.pushConfigured() {
		if cm.startPushSubscription(ctx, cam, cb) {
			recordOutcome(nil)
			return
		}
		// Push unavailable for this camera right now — fall through to the
		// Pull-Point path unchanged.
	}

	if factory != nil {
		sub, err := factory(ctx, cameraID, cb)
		if err != nil {
			recordOutcome(err)
			logger.Warn("camera:onvif motion subscription failed",
				"camera_id", cameraID, "error", err)
			return
		}
		if err := sub.Subscribe(ctx, cameraID); err != nil {
			recordOutcome(err)
			logger.Info("camera:onvif motion subscription not established",
				"camera_id", cameraID, "error", err)
			return
		}
		cm.onvifMu.Lock()
		cm.eventSubscribers[cameraID] = sub
		cm.onvifMu.Unlock()
		recordOutcome(nil)
		logger.Info("subscribed to camera-side ONVIF motion events", "camera_id", cameraID)
		return
	}

	// Production path: per-camera ONVIF client → subscriber. 1s poll keeps
	// trigger latency low (mibee_cam contract suggests 0.5–1s; the 120s
	// device-side expiry gives huge headroom).
	if err := cm.SubscribeONVIFEvents(ctx, cameraID, cb, onvif.WithPollInterval(time.Second)); err != nil {
		recordOutcome(err)
		logger.Info("camera:onvif motion subscription not established",
			"camera_id", cameraID, "error", err)
		return
	}
	recordOutcome(nil)
}

// ONVIFEventsStatus reports the per-camera subscription diagnostics.
func (cm *CameraManager) ONVIFEventsStatus(cameraID string) onvif.EventSubscriptionStatus {
	cm.onvifMu.Lock()
	sub := cm.eventSubscribers[cameraID]
	lastErr := cm.motionSubErrors[cameraID]
	cm.onvifMu.Unlock()
	if sub == nil {
		st := onvif.EventSubscriptionStatus{}
		if lastErr != "" {
			st.LastError = lastErr
			if onvif.IsEventsNotSupported(lastErr) {
				st.State = onvif.StateUnsupported
			} else {
				st.State = onvif.StateResubscribing
			}
		}
		return st
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
		// A MotionAlarm-shaped event that failed to parse is the one failure
		// mode that can hide a device-side encoding mismatch (e.g. a vendor
		// sending State=active instead of true): the true-leg silently never
		// fires and "device emits nothing" is indistinguishable from "emits
		// something we drop" (#711). WARN with the raw payload; other topics
		// are simply not our contract and stay silent.
		if strings.Contains(strings.ToLower(evt.Topic), "motionalarm") {
			logger.Warn("motionalarm event unparseable — State missing or not a bool",
				"camera_id", cameraID, "topic", evt.Topic, "data", fmt.Sprintf("%v", evt.Data))
		}
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
		cm.motionAction(cameraID, "record", 0)
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

// --- push transport (wsnt:Subscribe + device-POSTed Notify, #922) ----------

// pushConfigured reports whether server.advertise_url enables the push
// transport at all. Nil-config (tests) = disabled.
func (cm *CameraManager) pushConfigured() bool {
	return cm.cfg != nil && cm.cfg.Server.AdvertiseURL != ""
}

// pushDeclinedReason returns why the camera is not on push ("" = eligible).
func (cm *CameraManager) pushDeclinedReason(cameraID string) string {
	cm.onvifMu.Lock()
	defer cm.onvifMu.Unlock()
	return cm.pushDeclined[cameraID]
}

// pushTokenFor returns the camera's stable notify-path token, generating one
// (crypto/rand, hex) on first use.
func (cm *CameraManager) pushTokenFor(cameraID string) string {
	cm.onvifMu.Lock()
	defer cm.onvifMu.Unlock()
	if t, ok := cm.pushTokens[cameraID]; ok {
		return t
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failure is practically fatal system-wide; a fixed
		// fallback token still keeps the endpoint non-guessable per process.
		logger.Warn("crypto/rand unavailable for ONVIF notify token", "error", err)
		for i := range b {
			b[i] = byte(time.Now().UnixNano() >> (i % 8))
		}
	}
	t := hex.EncodeToString(b)
	cm.pushTokens[cameraID] = t
	return t
}

// onvifNotifyURL builds the ConsumerReference address for a camera: the
// advertised base URL plus the token-credentialed notify path.
func (cm *CameraManager) onvifNotifyURL(cameraID, token string) string {
	base := strings.TrimSuffix(cm.cfg.Server.AdvertiseURL, "/")
	return base + "/api/onvif/notify/" + cameraID + "/" + token
}

// startPushSubscription probes the camera with a wsnt:Subscribe and, on
// success, records it as the camera's event subscriber. Returns false when
// push is unavailable (probe fault / transport error) — the caller then uses
// the Pull-Point path. Renew failures later degrade the camera back via the
// fallback hook: tombstone the camera, tear the push subscriber down, and
// re-run the reconcile (which starts Pull-Point).
func (cm *CameraManager) startPushSubscription(ctx context.Context, cam config.CameraConfig, cb onvif.EventCallback) bool {
	cameraID := cam.ID
	token := cm.pushTokenFor(cameraID)
	notifyURL := cm.onvifNotifyURL(cameraID, token)

	decline := func(reason string) {
		cm.onvifMu.Lock()
		cm.pushDeclined[cameraID] = reason
		cm.onvifMu.Unlock()
	}
	fallback := func(id, reason string) {
		logger.Warn("ONVIF push subscription degraded to pull-point", "camera_id", id, "reason", reason)
		// Tear the dead push subscriber down first (the teardown clears the
		// decline tombstone), THEN tombstone the camera so the reconcile
		// below sees "no subscriber" and takes the Pull-Point path without
		// re-probing push.
		_ = cm.UnsubscribeONVIFEvents(context.Background(), id)
		decline(reason)
		if c := cm.GetCameraConfig(id); c != nil {
			go cm.EnsureMotionSubscription(context.Background(), *c)
		}
	}

	var sub onvif.EventSubscriber
	if factory := cm.pushSubscriberFactory; factory != nil {
		s, err := factory(ctx, cameraID, notifyURL, cb, fallback)
		if err != nil {
			logger.Debug("push subscriber construction failed; using pull-point", "camera_id", cameraID, "error", err)
			return false
		}
		sub = s
	} else {
		client, err := cm.getOrCreateONVIFClient(ctx, cameraID)
		if err != nil || client == nil {
			logger.Debug("push probe skipped (no ONVIF client); using pull-point", "camera_id", cameraID)
			return false
		}
		sub = client.NewPushSubscriber(
			onvif.WithPushCallback(cb),
			onvif.WithPushNotifyURL(notifyURL),
			onvif.WithPushFallback(fallback),
		)
		if sub == nil {
			return false
		}
	}

	if err := sub.Subscribe(ctx, cameraID); err != nil {
		if errors.Is(err, onvif.ErrPushNotSupported) {
			logger.Info("device declined ONVIF push events; using pull-point", "camera_id", cameraID)
			decline(err.Error())
			return false
		}
		logger.Debug("ONVIF push subscribe failed; trying pull-point", "camera_id", cameraID, "error", err)
		return false
	}

	cm.onvifMu.Lock()
	cm.eventSubscribers[cameraID] = sub
	cm.onvifMu.Unlock()
	logger.Info("subscribed to ONVIF events via push", "camera_id", cameraID)
	return true
}

// HandleOnvifNotify is the HTTP consumer entry for device-POSTed wsnt:Notify
// bodies: it validates the path token (constant-time), parses the payload and
// dispatches each event through the same path as Pull-Point events. The
// returned HTTP status/message pair maps: 200 valid (zero events included —
// some devices heartbeat empty Notifies), 404 unknown camera or token, 400
// malformed body.
func (cm *CameraManager) HandleOnvifNotify(cameraID, token string, body []byte) (int, string) {
	cm.onvifMu.Lock()
	want, known := cm.pushTokens[cameraID]
	sub := cm.eventSubscribers[cameraID]
	cm.onvifMu.Unlock()

	// Constant-time compare on equal-length assumption; unknown camera and
	// wrong token are the same 404 so the endpoint reveals nothing.
	if !known || subtle.ConstantTimeCompare([]byte(want), []byte(token)) != 1 {
		return http.StatusNotFound, "not found"
	}

	events, err := onvif.ParseNotifyEvents(body, cameraID)
	if err != nil {
		return http.StatusBadRequest, err.Error()
	}
	if len(events) == 0 {
		return http.StatusOK, ""
	}

	deliverer, _ := sub.(onvif.EventDeliverer)
	for _, evt := range events {
		if deliverer != nil {
			deliverer.DeliverEvent(evt)
			continue
		}
		cm.onONVIFEvent(cameraID, evt)
	}
	return http.StatusOK, ""
}
