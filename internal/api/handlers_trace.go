package api

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/frametrace"
	"github.com/go-chi/chi/v5"
)

// --- Per-camera frame-trace sampling (#482) ---

// handleCameraTrace serves POST/DELETE /api/cameras/{id}/trace[?duration=30s].
//
// POST starts (or extends) a sampling window during which that camera's
// frame_trace breadcrumbs are emitted at Info level through the dedicated
// "frame-trace" component logger — tail it with:
//
//	journalctl -u mibee-nvr -t mibee-nvr | grep frame-trace
//
// The window auto-expires (default 30s, max 5m) so per-frame logging can
// never be left on by accident. DELETE ends it early. GET reports the
// remaining window without changing it.
func (h *Handler) handleCameraTrace(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	if cam, err := h.db.GetCamera(r.Context(), id); err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to get camera")
		return
	} else if cam == nil {
		WriteError(w, http.StatusNotFound, "camera not found")
		return
	}

	switch r.Method {
	case http.MethodGet:
		active := frametrace.Active(id)
		writeJSON(w, http.StatusOK, map[string]any{
			"camera_id": id,
			"active":    active,
		})
	case http.MethodPost:
		d := frametrace.DefaultDuration
		if q := r.URL.Query().Get("duration"); q != "" {
			parsed, err := time.ParseDuration(q)
			if err != nil || parsed <= 0 {
				WriteError(w, http.StatusBadRequest, "invalid duration (use e.g. 30s, 2m; max 5m)")
				return
			}
			d = parsed
		}
		until := frametrace.Enable(id, d)
		slog.Info("frame-trace sampling started", "camera_id", id, "duration", time.Until(until).Round(time.Second).String())
		writeJSON(w, http.StatusOK, map[string]any{
			"camera_id":    id,
			"active":       true,
			"active_until": until.UTC().Format(time.RFC3339),
		})
	case http.MethodDelete:
		frametrace.Disable(id)
		slog.Info("frame-trace sampling stopped", "camera_id", id)
		writeJSON(w, http.StatusOK, map[string]any{
			"camera_id": id,
			"active":    false,
		})
	}
}
