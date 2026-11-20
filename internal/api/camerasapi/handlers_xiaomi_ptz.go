// SPDX-License-Identifier: MIT
//
// Xiaomi PTZ motor control and device info API handlers.

package camerasapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/xiaomi"
	"github.com/go-chi/chi/v5"
)

// requireXiaomiCamera checks that the camera in the URL exists and is a Xiaomi camera.
func (s *server) requireXiaomiCamera(w http.ResponseWriter, r *http.Request) bool {
	if s.deps.DB == nil {
		WriteError(w, http.StatusNotFound, "camera not found")
		return false
	}
	cameraID := chi.URLParam(r, "id")
	camera, err := s.deps.DB.GetCamera(r.Context(), cameraID)
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
func (s *server) getXiaomiRecorder(cameraID string) *xiaomi.XiaomiRecorder {
	if s.deps.Cam == nil {
		return nil
	}
	rec := s.deps.Cam.GetRecorder(cameraID)
	if rec == nil {
		return nil
	}
	xiaomiRec, ok := rec.(*xiaomi.XiaomiRecorder)
	if !ok {
		return nil
	}
	return xiaomiRec
}

// handleXiaomiVectorPTZMove routes the unified continuous-move vector
// (POST /api/cameras/{id}/ptz/move) onto the Xiaomi motor command, so every
// surface drives Xiaomi PT cams through the same endpoint as ONVIF/GB.
// HandleXiaomiVectorPTZMove is the exported form — the GB domain's unified
// PTZ endpoints dispatch Xiaomi cameras here (api root shim wires deps).
func HandleXiaomiVectorPTZMove(deps Deps, w http.ResponseWriter, cameraID string, pan, tilt, zoom float64) {
	(&server{deps: deps}).handleXiaomiVectorPTZMove(w, cameraID, pan, tilt, zoom)
}

func (s *server) handleXiaomiVectorPTZMove(w http.ResponseWriter, cameraID string, pan, tilt, zoom float64) {
	direction, speed, err := xiaomiPTZFromVector(pan, tilt, zoom)
	if err != nil {
		WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.xiaomiMotorCommand(cameraID, direction, speed); err != nil {
		logger.Error("Xiaomi PTZ move failed", "camera_id", cameraID, "error", err, "direction", direction, "speed", speed)
		WriteError(w, s.xiaomiPTZStatusCode(err), fmt.Sprintf("PTZ command failed: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// errXiaomiPTZNotConnected marks a missing Xiaomi recorder so callers can
// answer 503 (camera offline) instead of a generic 500.
var errXiaomiPTZNotConnected = errors.New("camera is not connected")

// xiaomiPTZStatusCode maps motor dispatch errors onto HTTP statuses.
// Both not-connected shapes answer 503: a recorder missing from the
// manager (restarting) and one registered with its control plane down
// (xiaomi.ErrNotConnected, the reconnecting state) are runtime states,
// not server defects (mibeescout#40).
// XiaomiPTZStatusCode maps a Xiaomi PTZ error onto the HTTP status the
// unified PTZ endpoints return.
func XiaomiPTZStatusCode(err error) int {
	return (&server{}).xiaomiPTZStatusCode(err)
}

func (s *server) xiaomiPTZStatusCode(err error) int {
	if errors.Is(err, errXiaomiPTZNotConnected) ||
		errors.Is(err, xiaomi.ErrNotConnected) {
		return http.StatusServiceUnavailable
	}
	return http.StatusInternalServerError
}

// xiaomiMotorCommand sends a motor command to the camera's Xiaomi recorder.
// The dispatch can be overridden via SetXiaomiPTZMotor (tests); production
// always goes through the camera manager's recorder.
// XiaomiMotorCommand is the exported form (see HandleXiaomiVectorPTZMove).
func XiaomiMotorCommand(deps Deps, cameraID, direction string, speed int) error {
	return (&server{deps: deps}).xiaomiMotorCommand(cameraID, direction, speed)
}

func (s *server) xiaomiMotorCommand(cameraID, direction string, speed int) error {
	if s.deps.XiaomiPTZMotor != nil {
		return s.deps.XiaomiPTZMotor(cameraID, direction, speed)
	}
	if s.deps.Cam == nil {
		return fmt.Errorf("camera manager not available")
	}
	rec := s.getXiaomiRecorder(cameraID)
	if rec == nil {
		return errXiaomiPTZNotConnected
	}
	return rec.MotorControl(direction, speed)
}

// handleXiaomiPTZMove handles POST /api/cameras/{id}/xiaomi/ptz/move
// JSON body: {"direction":"left","speed":5}
func (s *server) handleXiaomiPTZMove(w http.ResponseWriter, r *http.Request) {
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

	if !s.requireXiaomiCamera(w, r) {
		return
	}
	if s.deps.Cam == nil {
		WriteError(w, http.StatusInternalServerError, "camera manager not available")
		return
	}

	xiaomiRec := s.getXiaomiRecorder(cameraID)
	if xiaomiRec == nil {
		WriteError(w, http.StatusServiceUnavailable, "camera is not connected")
		return
	}

	if err := xiaomiRec.MotorControl(req.Direction, req.Speed); err != nil {
		logger.Error("Xiaomi PTZ move failed", "camera_id", cameraID, "error", err, "direction", req.Direction, "speed", req.Speed)
		WriteError(w, s.xiaomiPTZStatusCode(err), fmt.Sprintf("PTZ command failed: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleXiaomiPTZStop handles POST /api/cameras/{id}/xiaomi/ptz/stop
func (s *server) handleXiaomiPTZStop(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "id")

	if !s.requireXiaomiCamera(w, r) {
		return
	}
	if s.deps.Cam == nil {
		WriteError(w, http.StatusInternalServerError, "camera manager not available")
		return
	}

	xiaomiRec := s.getXiaomiRecorder(cameraID)
	if xiaomiRec == nil {
		WriteError(w, http.StatusServiceUnavailable, "camera is not connected")
		return
	}

	// Send stop command — speed=0 with direction "stop" stops the motor.
	if err := xiaomiRec.MotorControl("stop", 0); err != nil {
		logger.Error("Xiaomi PTZ stop failed", "camera_id", cameraID, "error", err)
		WriteError(w, s.xiaomiPTZStatusCode(err), fmt.Sprintf("PTZ stop failed: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleXiaomiDeviceInfo handles GET /api/cameras/{id}/xiaomi/device-info
func (s *server) handleXiaomiDeviceInfo(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "id")

	if !s.requireXiaomiCamera(w, r) {
		return
	}
	if s.deps.Cam == nil {
		WriteError(w, http.StatusInternalServerError, "camera manager not available")
		return
	}

	xiaomiRec := s.getXiaomiRecorder(cameraID)
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

// xiaomiPTZFromVector maps an ONVIF-style continuous-move vector onto the
// Xiaomi motor wire command. Xiaomi PT cameras have no optical zoom — a
// zoom-dominant vector is an explicit error so callers surface the
// capability mismatch instead of a silent no-op. A zero vector maps to
// "stop". Speed scales as magnitude*10 (the legacy pad's default speed 5
// corresponds to the unified API's 0.5 default magnitude).
func xiaomiPTZFromVector(pan, tilt, zoom float64) (direction string, speed int, err error) {
	ax, ay, az := math.Abs(pan), math.Abs(tilt), math.Abs(zoom)
	switch {
	case az > 0 && az >= ax && az >= ay:
		return "", 0, errors.New("PTZ zoom is not supported on Xiaomi cameras")
	case ay > 0 && ay >= ax:
		if tilt > 0 {
			return "up", xiaomiSpeedFromMagnitude(ay), nil
		}
		return "down", xiaomiSpeedFromMagnitude(ay), nil
	case ax > 0:
		if pan > 0 {
			return "right", xiaomiSpeedFromMagnitude(ax), nil
		}
		return "left", xiaomiSpeedFromMagnitude(ax), nil
	default:
		return "stop", 0, nil
	}
}

func xiaomiSpeedFromMagnitude(v float64) int {
	s := int(v*10 + 0.5)
	if s < 1 {
		s = 1
	}
	if s > 100 {
		s = 100
	}
	return s
}
