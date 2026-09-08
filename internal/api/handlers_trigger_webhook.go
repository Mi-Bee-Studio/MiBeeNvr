package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/slogx"
	"github.com/go-chi/chi/v5"
)

// Webhook trigger endpoint (issue #709): the HTTP counterpart of the MQTT
// trigger — external systems (sensors, automation platforms, IoT gateways)
// POST a signed request to start/stop recording or capture a snapshot.
//
// POST /api/trigger/webhook/{camera_id}?action=record|stop|snapshot
//
// The endpoint is mounted on the PUBLIC rate-limited route group (no
// BasicAuth): the credential is the HMAC-SHA256 signature over the request
// body, carried in the X-MiBee-Signature header Stripe-style:
//
//	X-MiBee-Signature: t=<unix-seconds>,v1=<hex(HMAC-SHA256(secret, "<t>.<body>"))>
//
// The timestamp inside the signed payload bounds replay (default ±5 min).
// Action semantics are identical to the MQTT trigger by construction —
// builders.go wires BOTH endpoints to the same action dispatcher.

var (
	webhookLogger = slogx.Component("webhook-trigger")

	// errWebhookStaleTimestamp marks a signature whose timestamp falls outside
	// the replay window (either direction) — distinct from a bad MAC so the
	// audit log can tell clock skew / replay from a wrong secret.
	errWebhookStaleTimestamp = errors.New("timestamp outside replay window")

	// webhookMaxBody bounds the request body. Triggers carry small JSON
	// payloads (if any); 1MB is orders of magnitude beyond legitimate use and
	// keeps a hostile client from buffering unbounded memory before the
	// signature check.
	webhookMaxBody = int64(1 << 20)
)

// webhookActions are the accepted ?action= values. Kept in sync with the
// MQTT dispatcher's action set by the shared dispatcher itself — anything the
// dispatcher rejects is also rejected here, synchronously, with a 400.
var webhookActions = map[string]bool{
	"record":   true,
	"stop":     true,
	"snapshot": true,
}

// SetTriggerDispatcher wires the trigger action dispatcher (the same
// func(cameraID, action) the MQTT client uses — see mqtt.NewActionDispatcher).
// Nil disables the endpoint (503 on call, route still mounted).
func (h *Handler) SetTriggerDispatcher(dispatch func(cameraID, action string)) {
	h.triggerDispatcher = dispatch
}

// registerTriggerRoutes mounts the webhook endpoint when the config enables
// it. Mounted inside the rate-limited public group (60 req/min per IP), same
// exposure class as /api/health.
func (h *Handler) registerTriggerRoutes(r chi.Router) {
	if h.config == nil || !h.config.Trigger.Webhook.Enabled {
		return
	}
	if strings.TrimSpace(h.config.Trigger.Webhook.Secret) == "" {
		// Validate() rejects this combination at load; guard anyway so a
		// hand-wired Handler in tests can't mount an unverifiable endpoint.
		return
	}
	r.Post("/api/trigger/webhook/{cameraID}", h.handleTriggerWebhook)
}

func (h *Handler) handleTriggerWebhook(w http.ResponseWriter, r *http.Request) {
	cameraID := r.PathValue("cameraID")
	action := r.URL.Query().Get("action")
	remote := r.RemoteAddr

	// Read the raw body FIRST — it is part of the signed payload, so the
	// signature must be verified over exactly these bytes.
	body, err := io.ReadAll(io.LimitReader(r.Body, webhookMaxBody))
	if err != nil {
		webhookLogger.Warn("webhook trigger: body read failed", "camera_id", cameraID, "remote", remote, "error", err)
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	secret := h.config.Trigger.Webhook.Secret
	window := time.Duration(h.config.Trigger.Webhook.ReplayWindow()) * time.Second
	if err := verifyWebhookSignature(secret, r.Header.Get("X-MiBee-Signature"), body, time.Now(), window); err != nil {
		// Signature failures are the audit trail for unauthorized trigger
		// attempts — log with source IP, respond uniformly 401 (no oracle
		// distinguishing stale vs forged vs missing).
		webhookLogger.Warn("webhook trigger: signature rejected",
			"camera_id", cameraID, "remote", remote, "error", err)
		WriteError(w, http.StatusUnauthorized, "invalid or expired signature")
		return
	}

	if !webhookActions[action] {
		webhookLogger.Warn("webhook trigger: unknown action", "camera_id", cameraID, "remote", remote, "action", action)
		WriteError(w, http.StatusBadRequest, "action must be one of record, stop, snapshot")
		return
	}

	if h.triggerDispatcher == nil {
		webhookLogger.Error("webhook trigger dropped: no dispatcher wired", "camera_id", cameraID, "remote", remote)
		WriteError(w, http.StatusServiceUnavailable, "trigger dispatcher not available")
		return
	}

	if h.camMgr == nil || h.camMgr.GetCameraConfig(cameraID) == nil {
		// Unknown camera AFTER signature verification: the requester is
		// authenticated, so a plain 404 is the honest answer (no existence
		// oracle for unsigned callers — those were already 401'd above).
		webhookLogger.Warn("webhook trigger: unknown camera", "camera_id", cameraID, "remote", remote)
		WriteError(w, http.StatusNotFound, "unknown camera")
		return
	}

	// The dispatcher is internally async (per-action goroutine, 30s bounded),
	// so this returns immediately; outcomes land in the structured log below.
	h.triggerDispatcher(cameraID, action)
	webhookLogger.Info("webhook trigger accepted",
		"camera_id", cameraID, "action", action, "remote", remote, "body_bytes", len(body))
	writeJSON(w, http.StatusAccepted, map[string]any{
		"status":    "accepted",
		"camera_id": cameraID,
		"action":    action,
	})
}

// verifyWebhookSignature checks a Stripe-style signed webhook header:
//
//	X-MiBee-Signature: t=<unix-seconds>,v1=<hex(HMAC-SHA256(secret, "<t>.<body>"))>
//
// The timestamp is INSIDE the MAC input, so an attacker cannot re-fresh an old
// signature; the replay window (both directions) bounds how long a captured
// request stays valid. Returns errWebhookStaleTimestamp for out-of-window
// timestamps and a generic error for anything malformed or mismatched.
func verifyWebhookSignature(secret, header string, body []byte, now time.Time, window time.Duration) error {
	if secret == "" {
		return errors.New("no webhook secret configured")
	}
	var ts int64
	var v1hex string
	for _, part := range strings.Split(header, ",") {
		key, value, found := strings.Cut(strings.TrimSpace(part), "=")
		if !found {
			continue
		}
		switch key {
		case "t":
			var err error
			ts, err = strconv.ParseInt(value, 10, 64)
			if err != nil {
				return fmt.Errorf("invalid timestamp %q", value)
			}
		case "v1":
			v1hex = value
		}
	}
	if ts == 0 || v1hex == "" {
		return errors.New("signature header missing t or v1")
	}

	signedAt := time.Unix(ts, 0)
	if d := now.Sub(signedAt); d > window || -d > window {
		return fmt.Errorf("%w: signed %v, window %v", errWebhookStaleTimestamp, signedAt.UTC(), window)
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(ts, 10)))
	mac.Write([]byte("."))
	mac.Write(body)
	expected := mac.Sum(nil)

	got, err := hex.DecodeString(v1hex)
	if err != nil || len(got) != len(expected) {
		return errors.New("malformed v1 signature")
	}
	if subtle.ConstantTimeCompare(got, expected) != 1 {
		return errors.New("signature mismatch")
	}
	return nil
}
