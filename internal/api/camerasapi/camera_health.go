package camerasapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// handleGetCameraHealth serves GET /api/cameras/{id}/health — the per-camera
// health snapshot from the health manager (domain-owned flat route).
func (s *server) handleGetCameraHealth(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "id")
	if cameraID == "" {
		WriteError(w, http.StatusBadRequest, "missing camera id")
		return
	}
	if s.deps.CameraHealth == nil {
		WriteError(w, http.StatusNotFound, "camera not found")
		return
	}
	health := s.deps.CameraHealth(cameraID)
	if health == nil {
		WriteError(w, http.StatusNotFound, "camera not found")
		return
	}
	writeJSON(w, http.StatusOK, health)
}
