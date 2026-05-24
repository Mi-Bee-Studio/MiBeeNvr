package api

import (
	"net/http"
	"strconv"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/go-chi/chi/v5"
)

// HealthManager provides camera health status data.
type HealthManager interface {
	GetAllHealth() map[string]*model.CameraHealth
	GetCameraHealth(cameraID string) *model.CameraHealth
}

// handleGetHealthStatus returns the current health status for all monitored cameras.
func (h *Handler) handleGetHealthStatus(w http.ResponseWriter, r *http.Request) {
	if h.healthMgr == nil {
		writeJSON(w, http.StatusOK, map[string]*model.CameraHealth{})
		return
	}
	health := h.healthMgr.GetAllHealth()
	if health == nil {
		health = map[string]*model.CameraHealth{}
	}
	writeJSON(w, http.StatusOK, health)
}

// handleGetHealthEvents returns paginated health events from the database.
func (h *Handler) handleGetHealthEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	filter := storage.HealthEventsFilter{
		CameraID: q.Get("camera_id"),
		Since:    q.Get("since"),
	}

	if v := q.Get("limit"); v != "" {
		limit, err := strconv.Atoi(v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid limit parameter")
			return
		}
		filter.Limit = limit
	}

	if v := q.Get("offset"); v != "" {
		offset, err := strconv.Atoi(v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid offset parameter")
			return
		}
		filter.Offset = offset
	}

	events, total, err := h.db.ListHealthEvents(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list health events")
		return
	}

	if events == nil {
		events = []model.HealthEvent{}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"events": events,
		"total":  total,
	})
}

// handleGetCameraHealth returns the health status for a single camera.
func (h *Handler) handleGetCameraHealth(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "id")
	if cameraID == "" {
		writeError(w, http.StatusBadRequest, "missing camera id")
		return
	}

	if h.healthMgr == nil {
		writeError(w, http.StatusNotFound, "camera not found")
		return
	}

	health := h.healthMgr.GetCameraHealth(cameraID)
	if health == nil {
		writeError(w, http.StatusNotFound, "camera not found")
		return
	}

	writeJSON(w, http.StatusOK, health)
}
