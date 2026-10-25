package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/notify"
	"github.com/go-chi/chi/v5"
)

// NotifyService is the notifications dispatcher surface the API needs.
// Satisfied by *notify.Service; interface-typed for handler tests.
type NotifyService interface {
	// SendTest enqueues a notify.test alert to every sink (bypasses filters).
	SendTest(severity, title string)
	// Stats snapshots per-sink delivery counters.
	Stats() []notify.SinkStats
}

// SetNotifyService wires the outbound alert dispatcher. nil (notifications
// disabled) leaves the endpoints reporting disabled instead of 404-ing, so
// the settings panel can explain itself.
func (h *Handler) SetNotifyService(svc NotifyService) {
	h.notifySvc = svc
}

// registerNotifyRoutes mounts the notification test/status endpoints
// (inside the authed group).
func (h *Handler) registerNotifyRoutes(r chi.Router) {
	r.Get("/api/notifications/status", h.handleNotifyStatus)
	r.Post("/api/notifications/test", h.handleNotifyTest)
}

// handleNotifyStatus reports whether forwarding is enabled, the per-sink
// delivery counters, and the event-type vocabulary (drives the settings
// panel's type checkboxes).
func (h *Handler) handleNotifyStatus(w http.ResponseWriter, r *http.Request) {
	resp := map[string]any{
		"enabled": h.notifySvc != nil,
		"types":   notify.KnownTypes(),
	}
	if h.notifySvc != nil {
		resp["sinks"] = h.notifySvc.Stats()
	} else {
		resp["sinks"] = []any{}
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleNotifyTest fires a notify.test alert to every configured sink so a
// receiver setup can be verified end to end. Delivery is asynchronous — 202
// means "queued", not "delivered"; check /status counters for the outcome.
func (h *Handler) handleNotifyTest(w http.ResponseWriter, r *http.Request) {
	if h.notifySvc == nil {
		WriteError(w, http.StatusConflict, "notifications are not enabled")
		return
	}
	var body struct {
		Severity string `json:"severity"`
		Title    string `json:"title"`
	}
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
			WriteError(w, http.StatusBadRequest, "invalid request body")
			return
		}
	}
	if body.Severity != "" && !notify.IsValidSeverity(body.Severity) {
		WriteError(w, http.StatusBadRequest, "severity must be one of info|warning|error|critical")
		return
	}
	h.notifySvc.SendTest(body.Severity, body.Title)
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "queued"})
}
