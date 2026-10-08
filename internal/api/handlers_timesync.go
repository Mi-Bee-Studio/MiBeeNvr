package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/onvif"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/timesync"
	"github.com/go-chi/chi/v5"
)

// TimeSyncOps is the camera time-sync surface consumed by the API layer
// (satisfied by *timesync.Service; faked in tests).
type TimeSyncOps interface {
	Status(ctx context.Context, cameraID string) (*timesync.CameraTimeStatus, error)
	CorrectCameraTime(ctx context.Context, cameraID, tz string) (*timesync.CorrectResult, error)
	PointCameraAtNTP(ctx context.Context, cameraID, server string) error
}

// --- Camera time-sync endpoints (#time-sync) ---

// handleCameraTimeStatus GET /api/cameras/{id}/time
// Reports the camera's clock vs the NVR's. Never 500s on device trouble —
// an unreadable clock is a status (available:false + error), not a server
// failure, so the UI can render it.
func (h *Handler) handleCameraTimeStatus(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "id")
	if !h.requireTimeSyncCamera(w, r, cameraID) {
		return
	}
	st, err := h.timeSync.Status(r.Context(), cameraID)
	if err != nil {
		if errors.Is(err, onvif.ErrNoCredentials) {
			WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		handleCameraTimeError(w, cameraID, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// handleCameraTimeSync POST /api/cameras/{id}/time/sync
// Measures and (when the skew ≥ 1s) writes the NVR time to the camera.
// Body: {"timezone": "CST-8"} (optional POSIX TZ override).
func (h *Handler) handleCameraTimeSync(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "id")
	if !h.requireTimeSyncCamera(w, r, cameraID) {
		return
	}
	var req struct {
		Timezone string `json:"timezone"`
	}
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
			return
		}
	}
	res, err := h.timeSync.CorrectCameraTime(r.Context(), cameraID, req.Timezone)
	if err != nil {
		if errors.Is(err, onvif.ErrNoCredentials) {
			WriteError(w, http.StatusBadRequest, "camera credentials required to set its clock (configure the camera's ONVIF account first)")
			return
		}
		handleCameraTimeError(w, cameraID, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleCameraTimeNTP POST /api/cameras/{id}/time/ntp
// Points the camera at an NTP server (default: the NVR's own address as
// routable from the camera) and flips it to NTP mode — the long-term
// self-healing path. Body: {"server": "192.168.1.10"} (optional override).
func (h *Handler) handleCameraTimeNTP(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "id")
	if !h.requireTimeSyncCamera(w, r, cameraID) {
		return
	}
	var req struct {
		Server string `json:"server"`
	}
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
			return
		}
	}
	if err := h.timeSync.PointCameraAtNTP(r.Context(), cameraID, req.Server); err != nil {
		if errors.Is(err, onvif.ErrNoCredentials) {
			WriteError(w, http.StatusBadRequest, "camera credentials required to set its NTP server (configure the camera's ONVIF account first)")
			return
		}
		handleCameraTimeError(w, cameraID, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "server": req.Server})
}

// requireTimeSyncCamera validates handler-level preconditions shared by the
// three endpoints: service wired, camera manager present, camera exists and
// is ONVIF. Writes the error response and returns false on failure.
func (h *Handler) requireTimeSyncCamera(w http.ResponseWriter, r *http.Request, cameraID string) bool {
	if h.timeSync == nil {
		WriteError(w, http.StatusServiceUnavailable, "time sync service not available")
		return false
	}
	if h.camMgr == nil {
		WriteError(w, http.StatusInternalServerError, "camera manager not available")
		return false
	}
	cam := h.camMgr.GetCameraConfig(cameraID)
	if cam == nil {
		WriteError(w, http.StatusNotFound, "camera not found")
		return false
	}
	if cam.Protocol != "onvif" {
		WriteError(w, http.StatusBadRequest, "time sync is only available for ONVIF cameras")
		return false
	}
	return true
}

func handleCameraTimeError(w http.ResponseWriter, cameraID string, err error) {
	logger.Error("camera time operation failed", "camera_id", cameraID, "error", err)
	WriteError(w, http.StatusBadGateway, "camera time operation failed: "+err.Error())
}
