package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/go-chi/chi/v5"
)

// --- Merge settings endpoints ---

func (h *Handler) handleGetMergeSettings(w http.ResponseWriter, r *http.Request) {
	if h.config == nil {
		writeError(w, http.StatusInternalServerError, "config not available")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":               h.config.Merge.Enabled,
		"check_interval":        h.config.Merge.CheckInterval,
		"window_size":           h.config.Merge.WindowSize,
		"batch_limit":           h.config.Merge.BatchLimit,
		"min_segment_age":       h.config.Merge.MinSegmentAge,
		"min_segments_to_merge": h.config.Merge.MinSegmentsToMerge,
	})
}

func (h *Handler) handleUpdateMergeSettings(w http.ResponseWriter, r *http.Request) {
	if h.config == nil {
		writeError(w, http.StatusInternalServerError, "config not available")
		return
	}

	var body struct {
		Enabled            *bool   `json:"enabled"`
		CheckInterval      *string `json:"check_interval"`
		WindowSize         *string `json:"window_size"`
		BatchLimit         *int    `json:"batch_limit"`
		MinSegmentAge      *string `json:"min_segment_age"`
		MinSegmentsToMerge *int    `json:"min_segments_to_merge"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if body.Enabled != nil {
		h.config.Merge.Enabled = *body.Enabled
	}
	if body.CheckInterval != nil {
		if _, err := time.ParseDuration(*body.CheckInterval); err != nil {
			writeError(w, http.StatusBadRequest, "check_interval must be a valid duration (e.g., \"30m\", \"1h\")")
			return
		}
		h.config.Merge.CheckInterval = *body.CheckInterval
	}
	if body.WindowSize != nil {
		if _, err := time.ParseDuration(*body.WindowSize); err != nil {
			writeError(w, http.StatusBadRequest, "window_size must be a valid duration (e.g., \"24h\", \"48h\")")
			return
		}
		h.config.Merge.WindowSize = *body.WindowSize
	}
	if body.BatchLimit != nil {
		if *body.BatchLimit < 1 {
			writeError(w, http.StatusBadRequest, "batch_limit must be >= 1")
			return
		}
		h.config.Merge.BatchLimit = *body.BatchLimit
	}
	if body.MinSegmentAge != nil {
		if _, err := time.ParseDuration(*body.MinSegmentAge); err != nil {
			writeError(w, http.StatusBadRequest, "min_segment_age must be a valid duration (e.g., \"1h\", \"6h\")")
			return
		}
		h.config.Merge.MinSegmentAge = *body.MinSegmentAge
	}
	if body.MinSegmentsToMerge != nil {
		if *body.MinSegmentsToMerge < 1 {
			writeError(w, http.StatusBadRequest, "min_segments_to_merge must be >= 1")
			return
		}
		h.config.Merge.MinSegmentsToMerge = *body.MinSegmentsToMerge
	}

	// Persist config to disk
	if err := config.Save(h.configPath, h.config); err != nil {
		logger.Warn("failed to save config", "error", err)
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (h *Handler) handleUpdateCameraMergeConfig(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusInternalServerError, "database not available")
		return
	}

	cameraID := chi.URLParam(r, "id")
	if cameraID == "" {
		writeError(w, http.StatusBadRequest, "camera ID is required")
		return
	}

	var body struct {
		Enabled            *bool   `json:"enabled"`
		CheckInterval      *string `json:"check_interval"`
		WindowSize         *string `json:"window_size"`
		BatchLimit         *int    `json:"batch_limit"`
		MinSegmentAge      *string `json:"min_segment_age"`
		MinSegmentsToMerge *int    `json:"min_segments_to_merge"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Validate duration fields
	for _, d := range []*string{body.CheckInterval, body.WindowSize, body.MinSegmentAge} {
		if d != nil {
			if _, err := time.ParseDuration(*d); err != nil {
				writeError(w, http.StatusBadRequest, "duration fields must be valid (e.g., \"30m\", \"1h\")")
				return
			}
		}
	}
	if body.BatchLimit != nil && *body.BatchLimit < 1 {
		writeError(w, http.StatusBadRequest, "batch_limit must be >= 1")
		return
	}
	if body.MinSegmentsToMerge != nil && *body.MinSegmentsToMerge < 1 {
		writeError(w, http.StatusBadRequest, "min_segments_to_merge must be >= 1")
		return
	}

	if err := h.db.UpsertCameraMerge(r.Context(), cameraID,
		body.Enabled, body.CheckInterval, body.WindowSize, body.MinSegmentAge,
		body.BatchLimit, body.MinSegmentsToMerge); err != nil {
		logger.Warn("failed to update camera merge config", "error", err, "camera_id", cameraID)
		writeError(w, http.StatusInternalServerError, "failed to update merge config")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (h *Handler) handleDeleteCameraMergeConfig(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusInternalServerError, "database not available")
		return
	}

	cameraID := chi.URLParam(r, "id")
	if cameraID == "" {
		writeError(w, http.StatusBadRequest, "camera ID is required")
		return
	}

	// Pass all nil to clear (revert to global defaults)
	if err := h.db.UpsertCameraMerge(r.Context(), cameraID,
		nil, nil, nil, nil, nil, nil); err != nil {
		logger.Warn("failed to clear camera merge config", "error", err, "camera_id", cameraID)
		writeError(w, http.StatusInternalServerError, "failed to clear merge config")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "cleared"})
}

func (h *Handler) handleGetCameraMergeConfig(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusInternalServerError, "database not available")
		return
	}

	cameraID := chi.URLParam(r, "id")
	if cameraID == "" {
		writeError(w, http.StatusBadRequest, "camera ID is required")
		return
	}

	cam, err := h.db.GetCamera(r.Context(), cameraID)
	if err != nil {
		logger.Warn("failed to get camera", "error", err, "camera_id", cameraID)
		writeError(w, http.StatusInternalServerError, "failed to get camera")
		return
	}
	if cam == nil {
		writeError(w, http.StatusNotFound, "camera not found")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":               cam.MergeEnabled,
		"check_interval":        cam.MergeCheckInterval,
		"window_size":           cam.MergeWindowSize,
		"batch_limit":           cam.MergeBatchLimit,
		"min_segment_age":       cam.MergeMinSegmentAge,
		"min_segments_to_merge": cam.MergeMinSegmentsToMerge,
	})
}

// --- Merge status endpoints ---

func (h *Handler) handleMergeStatus(w http.ResponseWriter, r *http.Request) {
	if h.mergeMgr == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"enabled": false,
		})
		return
	}
	status := h.mergeMgr.Status()
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":         true,
		"last_run_time":   status.LastRunTime,
		"segments_merged": status.SegmentsMerged,
		"files_created":   status.FilesCreated,
		"error_count":     status.ErrorCount,
	})
}

func (h *Handler) handleMergePending(w http.ResponseWriter, r *http.Request) {
	if h.mergeMgr == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"enabled": false,
			"pending": map[string]int{},
		})
		return
	}
	counts := h.mergeMgr.PendingCounts(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": true,
		"pending": counts,
	})
}
