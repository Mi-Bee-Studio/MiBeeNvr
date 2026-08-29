package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/go-chi/chi/v5"
)

func (h *Handler) handleCameraRecordingStats(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	count, totalSize, err := h.db.GetCameraRecordingStats(r.Context(), id)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to get camera stats")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"recording_count": count, "total_size": totalSize})
}

func (h *Handler) handleStartCamera(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if h.camMgr == nil {
		WriteError(w, http.StatusServiceUnavailable, "camera manager not available")
		return
	}
	if err := h.camMgr.StartCamera(r.Context(), id); err != nil {
		switch {
		case errors.As(err, new(*model.CameraNotFoundError)):
			writeAPIError(w, http.StatusNotFound, err)
		case errors.As(err, new(*model.CameraAlreadyRunningError)):
			writeAPIError(w, http.StatusConflict, err)
		default:
			logger.Error("failed to start camera", "camera_id", id, "error", err, "path", r.URL.Path)
			WriteError(w, http.StatusInternalServerError, "failed to start camera")
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "started"})
}

// handleActivateCamera transitions a pending_activation camera to active by
// applying credentials and starting the recorder. Used by the auto-discover
// flow: an authenticated ONVIF device discovered without valid credentials is
// persisted as pending_activation; the user supplies credentials via this
// endpoint to bring it live.

// handleActivateCamera transitions a pending_activation camera to active by
// applying credentials and starting the recorder. Used by the auto-discover
// flow: an authenticated ONVIF device discovered without valid credentials is
// persisted as pending_activation; the user supplies credentials via this
// endpoint to bring it live.
func (h *Handler) handleActivateCamera(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if h.camMgr == nil {
		WriteError(w, http.StatusServiceUnavailable, "camera manager not available")
		return
	}
	if err := h.camMgr.ActivateCamera(r.Context(), id, body.Username, body.Password); err != nil {
		switch {
		case errors.As(err, new(*model.CameraNotFoundError)):
			writeAPIError(w, http.StatusNotFound, err)
		case errors.As(err, new(*model.CameraAlreadyRunningError)):
			// Idempotent: the recorder auto-restored (e.g. on NVR restart) and raced
			// with this request. Mirrors handleStartCamera's 409 — the camera is
			// already in the desired state, not a server error.
			writeAPIError(w, http.StatusConflict, err)
		default:
			logger.Error("failed to activate camera", "camera_id", id, "error", err, "path", r.URL.Path)
			WriteError(w, http.StatusInternalServerError, "failed to activate camera")
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "activated"})
}

func (h *Handler) handleStopCamera(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if h.camMgr == nil {
		WriteError(w, http.StatusServiceUnavailable, "camera manager not available")
		return
	}
	if err := h.camMgr.StopCamera(r.Context(), id); err != nil {
		var cnf *model.CameraNotFoundError
		if errors.As(err, &cnf) {
			writeAPIError(w, http.StatusNotFound, err)
		} else {
			logger.Error("failed to stop camera", "camera_id", id, "error", err, "path", r.URL.Path)
			WriteError(w, http.StatusInternalServerError, "failed to stop camera")
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "stopped"})
}

// handleRediscoverCamera manually triggers IP self-healing for a camera whose
// network address may have changed (e.g. after an AP reboot across per-subnet
// DHCP). It scans candidate subnets for a device whose ONVIF serial matches the
// camera's StableID and, if found, updates the config and reconnects.

// handleRediscoverCamera manually triggers IP self-healing for a camera whose
// network address may have changed (e.g. after an AP reboot across per-subnet
// DHCP). It scans candidate subnets for a device whose ONVIF serial matches the
// camera's StableID and, if found, updates the config and reconnects.
func (h *Handler) handleRediscoverCamera(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if h.camMgr == nil {
		WriteError(w, http.StatusServiceUnavailable, "camera manager not available")
		return
	}
	// The unicast scan can take up to MaxDuration (default 30s) on a wide subnet
	// hint list, so do not bind it to the request's lifetime.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	found, err := h.camMgr.RediscoverAndReconnect(ctx, id)
	if err != nil {
		var cnf *model.CameraNotFoundError
		switch {
		case errors.As(err, &cnf):
			writeAPIError(w, http.StatusNotFound, err)
		default:
			logger.Error("rediscover camera failed", "camera_id", id, "error", err, "path", r.URL.Path)
			WriteError(w, http.StatusInternalServerError, "rediscovery failed")
		}
		return
	}
	if !found {
		// Not an error: camera may be unsupported (non-ONVIF), have no stable_id,
		// or simply not be online in any candidate subnet yet.
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"found":  false,
			"reason": "camera not found in candidate subnets (is it powered on? consider adding subnet_hints)",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"found":  true,
		"status": "reconnected",
	})
}

// handleTestConnection attempts to connect to a camera URL with a short timeout.
// Returns success/failure, a human-readable message, and the latency in milliseconds.

// registerCameraRoutes registers all /api/cameras* routes (including nested
// stream, ONVIF, PTZ, snapshot, merge-config, timelapse config, events, and
// Xiaomi sub-routes) on the given (already auth-protected) router.
// handleAdaptiveTrigger handles POST /api/cameras/{id}/adaptive/trigger
// (issue #478): an external audio-activity event (e.g. a semantic classifier
// running outside the NVR) forces the camera's adaptive gate out of timelapse
// with the same GOP + pre-trigger back-fill as a loud window. Body:
// {"source": "...", "hold": "10s", "dbfs": -23.4} — source/dbfs are
// diagnostics only; hold (optional duration, 0–10m) extends how long
// timelapse entry stays deferred after the event.
func (h *Handler) handleAdaptiveTrigger(w http.ResponseWriter, r *http.Request) {
	if h.camMgr == nil {
		WriteError(w, http.StatusInternalServerError, "camera manager not available")
		return
	}
	id := chi.URLParam(r, "id")
	var body struct {
		Source string  `json:"source"`
		Hold   string  `json:"hold"`
		DBFS   float64 `json:"dbfs"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	var hold time.Duration
	if body.Hold != "" {
		d, err := time.ParseDuration(body.Hold)
		if err != nil || d < 0 || d > 10*time.Minute {
			WriteError(w, http.StatusBadRequest, "hold must be a duration between 0 and 10m")
			return
		}
		hold = d
	}
	rec := h.camMgr.GetRecorder(id)
	if rec == nil {
		WriteError(w, http.StatusNotFound, "camera not found")
		return
	}
	trig, ok := rec.(interface {
		AudioTriggerEvent(at time.Time, hold time.Duration) error
	})
	if !ok {
		WriteError(w, http.StatusBadRequest, "camera does not support adaptive triggers")
		return
	}
	if err := trig.AudioTriggerEvent(time.Now(), hold); err != nil {
		writeAPIError(w, http.StatusBadRequest, err)
		return
	}
	logger.Info("external adaptive trigger",
		"camera_id", id, "source", body.Source,
		"hold", hold.String(), "dbfs", body.DBFS)
	writeJSON(w, http.StatusOK, map[string]string{"status": "triggered"})
}
