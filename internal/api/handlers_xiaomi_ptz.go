// SPDX-License-Identifier: MIT
//
// Xiaomi PTZ motor control and device info API handlers.

package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/xiaomi"
	"github.com/go-chi/chi/v5"
)

// requireXiaomiCamera checks that the camera in the URL exists and is a Xiaomi camera.
func (h *Handler) requireXiaomiCamera(w http.ResponseWriter, r *http.Request) bool {
	if h.db == nil {
		WriteError(w, http.StatusNotFound, "camera not found")
		return false
	}
	cameraID := chi.URLParam(r, "id")
	camera, err := h.db.GetCamera(r.Context(), cameraID)
	if err != nil || camera == nil {
		WriteError(w, http.StatusNotFound, "camera not found")
		return false
	}
	if camera.Protocol != "xiaomi" {
		WriteError(w, http.StatusBadRequest, "this endpoint is only available for Xiaomi cameras")
		return false
	}
	return true
}

// getXiaomiRecorder retrieves the XiaomiRecorder from the camera manager for the given camera ID.
// Returns nil if the recorder is not available or is not a Xiaomi recorder.
func (h *Handler) getXiaomiRecorder(cameraID string) *xiaomi.XiaomiRecorder {
	if h.camMgr == nil {
		return nil
	}
	rec := h.camMgr.GetRecorder(cameraID)
	if rec == nil {
		return nil
	}
	xiaomiRec, ok := rec.(*xiaomi.XiaomiRecorder)
	if !ok {
		return nil
	}
	return xiaomiRec
}

// handleXiaomiPTZMove handles POST /api/cameras/{id}/xiaomi/ptz/move
// JSON body: {"direction":"left","speed":5}
func (h *Handler) handleXiaomiPTZMove(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "id")

	var req struct {
		Direction string `json:"direction"`
		Speed     int    `json:"speed"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Direction == "" {
		WriteError(w, http.StatusBadRequest, "direction is required")
		return
	}
	if req.Speed < 1 || req.Speed > 100 {
		WriteError(w, http.StatusBadRequest, "speed must be between 1 and 100")
		return
	}

	if !h.requireXiaomiCamera(w, r) {
		return
	}
	if h.camMgr == nil {
		WriteError(w, http.StatusInternalServerError, "camera manager not available")
		return
	}

	xiaomiRec := h.getXiaomiRecorder(cameraID)
	if xiaomiRec == nil {
		WriteError(w, http.StatusServiceUnavailable, "camera is not connected")
		return
	}

	if err := xiaomiRec.MotorControl(req.Direction, req.Speed); err != nil {
		logger.Error("Xiaomi PTZ move failed", "camera_id", cameraID, "error", err, "direction", req.Direction, "speed", req.Speed)
		WriteError(w, http.StatusInternalServerError, fmt.Sprintf("PTZ command failed: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleXiaomiPTZStop handles POST /api/cameras/{id}/xiaomi/ptz/stop
func (h *Handler) handleXiaomiPTZStop(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "id")

	if !h.requireXiaomiCamera(w, r) {
		return
	}
	if h.camMgr == nil {
		WriteError(w, http.StatusInternalServerError, "camera manager not available")
		return
	}

	xiaomiRec := h.getXiaomiRecorder(cameraID)
	if xiaomiRec == nil {
		WriteError(w, http.StatusServiceUnavailable, "camera is not connected")
		return
	}

	// Send stop command — speed=0 with direction "stop" stops the motor.
	if err := xiaomiRec.MotorControl("stop", 0); err != nil {
		logger.Error("Xiaomi PTZ stop failed", "camera_id", cameraID, "error", err)
		WriteError(w, http.StatusInternalServerError, fmt.Sprintf("PTZ stop failed: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleXiaomiDeviceInfo handles GET /api/cameras/{id}/xiaomi/device-info
func (h *Handler) handleXiaomiDeviceInfo(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "id")

	if !h.requireXiaomiCamera(w, r) {
		return
	}
	if h.camMgr == nil {
		WriteError(w, http.StatusInternalServerError, "camera manager not available")
		return
	}

	xiaomiRec := h.getXiaomiRecorder(cameraID)
	if xiaomiRec == nil {
		WriteError(w, http.StatusServiceUnavailable, "camera is not connected")
		return
	}

	info, err := xiaomiRec.GetDeviceInfo()
	if err != nil {
		logger.Error("Xiaomi device info failed", "camera_id", cameraID, "error", err)
		WriteError(w, http.StatusInternalServerError, fmt.Sprintf("device info failed: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, info)
}
