// Package notifyapi serves the outbound notification status and test-send
// endpoints.
//
// Extraction pattern shared by the api domain packages: a local Deps
// holding a narrow interface, plus Register. A nil Service means notifications
// are disabled — the endpoints still answer so the settings panel can explain
// itself instead of 404-ing.
package notifyapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/httpx"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/notify"
)

// Service is the dispatcher surface these endpoints need. Satisfied by
// *notify.Service.
type Service interface {
	// SendTest enqueues a notify.test alert to every sink (bypasses filters).
	SendTest(severity, title string)
	// Stats snapshots per-sink delivery counters.
	Stats() []notify.SinkStats
}

// Deps is what the API assembly injects.
type Deps struct {
	Service Service
}

// Register mounts the notification endpoints (inside the authed group).
func Register(r chi.Router, deps Deps) {
	r.Get("/api/notifications/status", status(deps))
	r.Post("/api/notifications/test", test(deps))
}

// status reports whether forwarding is enabled, the per-sink delivery counters
// and the event-type vocabulary (drives the settings panel's checkboxes).
func status(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"enabled": deps.Service != nil,
			"types":   notify.KnownTypes(),
		}
		if deps.Service != nil {
			resp["sinks"] = deps.Service.Stats()
		} else {
			resp["sinks"] = []any{}
		}
		httpx.WriteJSON(w, http.StatusOK, resp)
	}
}

// test fires a notify.test alert to every configured sink so a receiver setup
// can be verified end to end. Delivery is asynchronous — 202 means "queued",
// not "delivered"; check /status counters for the outcome.
func test(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps.Service == nil {
			httpx.WriteError(w, http.StatusConflict, "notifications are not enabled")
			return
		}
		var body struct {
			Severity string `json:"severity"`
			Title    string `json:"title"`
		}
		if r.Body != nil {
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
				httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
				return
			}
		}
		if body.Severity != "" && !notify.IsValidSeverity(body.Severity) {
			httpx.WriteError(w, http.StatusBadRequest, "severity must be one of info|warning|error|critical")
			return
		}
		deps.Service.SendTest(body.Severity, body.Title)
		httpx.WriteJSON(w, http.StatusAccepted, map[string]any{"status": "queued"})
	}
}
