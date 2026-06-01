package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/go-chi/chi/v5"
)

// --- Timelapse configuration endpoints ---

// handleGetCameraTimelapse returns the timelapse configuration for a camera.
// GET /api/cameras/{id}/timelapse
func (h *Handler) handleGetCameraTimelapse(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	if h.config == nil {
		writeError(w, http.StatusInternalServerError, "config not available")
		return
	}

	// Find camera in config
	var tl *config.CameraTimelapseConfig
	for i := range h.config.Cameras {
		if h.config.Cameras[i].ID == id {
			tl = h.config.Cameras[i].Timelapse
			break
		}
	}

	// Return timelapse config (nil means disabled/no config)
	if tl == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"enabled":         false,
			"interval":        "30s",
			"output_fps":      30,
			"video_codec":     "h264",
			"delete_original": false,
		})
		return
	}

	writeJSON(w, http.StatusOK, tl)
}

// handlePutCameraTimelapse updates the timelapse configuration for a camera.
// PUT /api/cameras/{id}/timelapse
func (h *Handler) handlePutCameraTimelapse(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	if h.config == nil {
		writeError(w, http.StatusInternalServerError, "config not available")
		return
	}
	if h.configPath == "" {
		writeError(w, http.StatusInternalServerError, "config path not available")
		return
	}

	var body config.CameraTimelapseConfig
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Validate interval
	if body.Interval != "" {
		dur, err := time.ParseDuration(body.Interval)
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("interval must be a valid duration (e.g., \"5s\", \"1m\"): %v", err))
			return
		}
		if dur < time.Second {
			writeError(w, http.StatusBadRequest, "interval must be at least 1s")
			return
		}
	}

	// Validate output_fps
	if body.OutputFPS < 0 || body.OutputFPS > 60 {
		writeError(w, http.StatusBadRequest, "output_fps must be between 0 and 60")
		return
	}

	// Validate video_codec
	if body.VideoCodec != "" && body.VideoCodec != "h264" && body.VideoCodec != "h265" {
		writeError(w, http.StatusBadRequest, "video_codec must be \"h264\" or \"h265\"")
		return
	}

	// Find and update camera config in memory
	found := false
	for i := range h.config.Cameras {
		if h.config.Cameras[i].ID == id {
			h.config.Cameras[i].Timelapse = &body
			// Apply defaults to zero-value fields
			if body.Interval == "" {
				h.config.Cameras[i].Timelapse.Interval = "30s"
			}
			if body.OutputFPS == 0 {
				h.config.Cameras[i].Timelapse.OutputFPS = 30
			}
			if body.VideoCodec == "" {
				h.config.Cameras[i].Timelapse.VideoCodec = "h264"
			}
			found = true
			break
		}
	}

	if !found {
		writeError(w, http.StatusNotFound, "camera not found")
		return
	}

	// Persist config to disk
	if err := config.Save(h.configPath, h.config); err != nil {
		logger.Warn("failed to save config after timelapse update", "camera_id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save config")
		return
	}

	writeJSON(w, http.StatusOK, h.config.Cameras)
}
