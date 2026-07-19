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
		WriteError(w, http.StatusInternalServerError, "config not available")
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
		WriteError(w, http.StatusInternalServerError, "config not available")
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
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if body.Enabled != nil {
		h.config.Merge.Enabled = *body.Enabled
	}
	if body.CheckInterval != nil {
		if _, err := time.ParseDuration(*body.CheckInterval); err != nil {
			WriteError(w, http.StatusBadRequest, "check_interval must be a valid duration (e.g., \"30m\", \"1h\")")
			return
		}
		h.config.Merge.CheckInterval = *body.CheckInterval
	}
	if body.WindowSize != nil {
		if _, err := time.ParseDuration(*body.WindowSize); err != nil {
			WriteError(w, http.StatusBadRequest, "window_size must be a valid duration (e.g., \"24h\", \"48h\")")
			return
		}
		h.config.Merge.WindowSize = *body.WindowSize
	}
	if body.BatchLimit != nil {
		if *body.BatchLimit < 1 {
			WriteError(w, http.StatusBadRequest, "batch_limit must be >= 1")
			return
		}
		h.config.Merge.BatchLimit = *body.BatchLimit
	}
	if body.MinSegmentAge != nil {
		if _, err := time.ParseDuration(*body.MinSegmentAge); err != nil {
			WriteError(w, http.StatusBadRequest, "min_segment_age must be a valid duration (e.g., \"1h\", \"6h\")")
			return
		}
		h.config.Merge.MinSegmentAge = *body.MinSegmentAge
	}
	if body.MinSegmentsToMerge != nil {
		if *body.MinSegmentsToMerge < 1 {
			WriteError(w, http.StatusBadRequest, "min_segments_to_merge must be >= 1")
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
		WriteError(w, http.StatusInternalServerError, "database not available")
		return
	}

	cameraID := chi.URLParam(r, "id")
	if cameraID == "" {
		WriteError(w, http.StatusBadRequest, "camera ID is required")
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
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Validate duration fields
	for _, d := range []*string{body.CheckInterval, body.WindowSize, body.MinSegmentAge} {
		if d != nil {
			if _, err := time.ParseDuration(*d); err != nil {
				WriteError(w, http.StatusBadRequest, "duration fields must be valid (e.g., \"30m\", \"1h\")")
				return
			}
		}
	}
	if body.BatchLimit != nil && *body.BatchLimit < 1 {
		WriteError(w, http.StatusBadRequest, "batch_limit must be >= 1")
		return
	}
	if body.MinSegmentsToMerge != nil && *body.MinSegmentsToMerge < 1 {
		WriteError(w, http.StatusBadRequest, "min_segments_to_merge must be >= 1")
		return
	}

	if err := h.db.UpsertCameraMerge(r.Context(), cameraID,
		body.Enabled, body.CheckInterval, body.WindowSize, body.MinSegmentAge,
		body.BatchLimit, body.MinSegmentsToMerge); err != nil {
		logger.Warn("failed to update camera merge config", "error", err, "camera_id", cameraID)
		WriteError(w, http.StatusInternalServerError, "failed to update merge config")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (h *Handler) handleDeleteCameraMergeConfig(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		WriteError(w, http.StatusInternalServerError, "database not available")
		return
	}

	cameraID := chi.URLParam(r, "id")
	if cameraID == "" {
		WriteError(w, http.StatusBadRequest, "camera ID is required")
		return
	}

	// Actually NULL out all per-camera merge fields so the camera reverts to the
	// global defaults. (Previously called UpsertCameraMerge with all-nil args,
	// which COALESCEs to existing values — a no-op — so the override never
	// cleared and the editor kept reopening in "(customized)" state. Issue #68-3.)
	if err := h.db.ClearCameraMerge(r.Context(), cameraID); err != nil {
		logger.Warn("failed to clear camera merge config", "error", err, "camera_id", cameraID)
		WriteError(w, http.StatusInternalServerError, "failed to clear merge config")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "cleared"})
}

func (h *Handler) handleGetCameraMergeConfig(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		WriteError(w, http.StatusInternalServerError, "database not available")
		return
	}

	cameraID := chi.URLParam(r, "id")
	if cameraID == "" {
		WriteError(w, http.StatusBadRequest, "camera ID is required")
		return
	}

	cam, err := h.db.GetCamera(r.Context(), cameraID)
	if err != nil {
		logger.Warn("failed to get camera", "error", err, "camera_id", cameraID)
		WriteError(w, http.StatusInternalServerError, "failed to get camera")
		return
	}
	if cam == nil {
		WriteError(w, http.StatusNotFound, "camera not found")
		return
	}

	// customized=false means every per-camera field is NULL → the camera uses
	// the global defaults and the editor should render collapsed / "using
	// default". Previously this endpoint always returned a non-null object, so
	// the editor could not tell "has override" from "all defaults" and reopened
	// expanded every time (issue #68-3).
	customized := cam.MergeEnabled != nil ||
		cam.MergeCheckInterval != nil ||
		cam.MergeWindowSize != nil ||
		cam.MergeBatchLimit != nil ||
		cam.MergeMinSegmentAge != nil ||
		cam.MergeMinSegmentsToMerge != nil

	writeJSON(w, http.StatusOK, map[string]any{
		"customized":            customized,
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

// handleMergeReclassify converts existing merge_status='failed' recordings with
// empty merge_error to 'incompatible'. This backfills undersized SPS/PPS groups
// that were incorrectly classified as failures.
func (h *Handler) handleMergeReclassify(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		WriteError(w, http.StatusInternalServerError, "database not available")
		return
	}

	result, err := h.db.DB().ExecContext(r.Context(), `
		UPDATE recordings SET merge_status = 'incompatible'
		WHERE merge_status = 'failed' AND (merge_error IS NULL OR merge_error = '')
	`)
	if err != nil {
		logger.Warn("failed to reclassify failed recordings", "error", err)
		WriteError(w, http.StatusInternalServerError, "failed to reclassify recordings")
		return
	}

	affected, _ := result.RowsAffected()
	logger.Info("reclassified failed recordings as incompatible", "count", affected)

	writeJSON(w, http.StatusOK, map[string]any{
		"status":       "ok",
		"reclassified": affected,
	})
}

// --- Rolling merge backfill endpoints ---

// handleMergeBackfillCamera triggers an immediate rolling merge backfill for a single camera.
// Processes all pending (and optionally failed) MP4 segments into window buckets.
//
// Query params:
//
//	include_failed=true  — also re-process previously failed/incompatible segments
func (h *Handler) handleMergeBackfillCamera(w http.ResponseWriter, r *http.Request) {
	if h.rollingMergeMgr == nil {
		WriteError(w, http.StatusServiceUnavailable, "rolling merge is not enabled")
		return
	}

	cameraID := chi.URLParam(r, "id")
	if cameraID == "" {
		WriteError(w, http.StatusBadRequest, "camera id is required")
		return
	}

	includeFailed := r.URL.Query().Get("include_failed") == "true"

	merged, err := h.rollingMergeMgr.BackfillCamera(r.Context(), cameraID, includeFailed)
	if err != nil {
		logger.Warn("backfill failed for camera",
			"camera_id", cameraID, "error", err, "include_failed", includeFailed)
		WriteError(w, http.StatusInternalServerError, "backfill failed: "+err.Error())
		return
	}

	logger.Info("backfill complete for camera",
		"camera_id", cameraID, "merged", merged, "include_failed", includeFailed)

	writeJSON(w, http.StatusOK, map[string]any{
		"status":          "ok",
		"camera_id":       cameraID,
		"segments_merged": merged,
		"include_failed":  includeFailed,
	})
}

// handleMergeBackfillAll triggers an immediate rolling merge backfill for ALL cameras
// that have rolling merge enabled.
//
// Query params:
//
//	include_failed=true  — also re-process previously failed/incompatible segments
func (h *Handler) handleMergeBackfillAll(w http.ResponseWriter, r *http.Request) {
	if h.rollingMergeMgr == nil {
		WriteError(w, http.StatusServiceUnavailable, "rolling merge is not enabled")
		return
	}

	includeFailed := r.URL.Query().Get("include_failed") == "true"

	// List all pending segments across all cameras. This is a status/inspection
	// endpoint — no throttling (needs the true total).
	recs, err := h.db.ListPendingSegmentsForRolling(r.Context(), "", includeFailed, 0, time.Time{})
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to list pending segments: "+err.Error())
		return
	}

	// Group by camera.
	byCamera := make(map[string]int)
	for _, rec := range recs {
		byCamera[rec.CameraID]++
	}

	totalMerged := 0
	for cameraID := range byCamera {
		merged, err := h.rollingMergeMgr.BackfillCamera(r.Context(), cameraID, includeFailed)
		if err != nil {
			logger.Warn("backfill failed for camera during global backfill",
				"camera_id", cameraID, "error", err)
			continue
		}
		totalMerged += merged
	}

	logger.Info("global backfill complete", "total_merged", totalMerged, "cameras", len(byCamera))

	writeJSON(w, http.StatusOK, map[string]any{
		"status":                "ok",
		"total_segments_merged": totalMerged,
		"cameras_processed":     len(byCamera),
		"include_failed":        includeFailed,
	})
}

// handleMergeConsolidate finds merge_quality='short' recordings and attempts to
// merge them with adjacent recordings of the same format to reach the minimum
// duration threshold.
func (h *Handler) handleMergeConsolidate(w http.ResponseWriter, r *http.Request) {
	if h.rollingMergeMgr == nil {
		WriteError(w, http.StatusServiceUnavailable, "rolling merge is not enabled")
		return
	}

	// Parse min_duration from query or config.
	minDurStr := r.URL.Query().Get("min_duration")
	cfg := h.config.Merge
	if minDurStr == "" {
		minDurStr = cfg.RollingMinDuration
	}
	if minDurStr == "" {
		minDurStr = "5m"
	}
	minDur, err := time.ParseDuration(minDurStr)
	if err != nil || minDur <= 0 {
		WriteError(w, http.StatusBadRequest, "invalid min_duration")
		return
	}

	// Find short merged recordings.
	cameraID := r.URL.Query().Get("camera_id")
	shortRecs, err := h.db.ListShortMergedRecordings(r.Context(), cameraID, minDur.Seconds())
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to list short recordings: "+err.Error())
		return
	}

	// Group by camera for processing.
	cameras := make(map[string]bool)
	for _, rec := range shortRecs {
		cameras[rec.CameraID] = true
	}

	totalConsolidated := 0
	for cam := range cameras {
		merged, err := h.rollingMergeMgr.ConsolidateShortRecord(r.Context(), cam, minDur)
		if err != nil {
			logger.Warn("consolidate failed for camera", "camera_id", cam, "error", err)
			continue
		}
		totalConsolidated += merged
	}

	logger.Info("consolidate complete", "total_consolidated", totalConsolidated, "short_recordings", len(shortRecs))

	writeJSON(w, http.StatusOK, map[string]any{
		"status":           "ok",
		"short_recordings": len(shortRecs),
		"consolidated":     totalConsolidated,
		"min_duration":     minDurStr,
	})
}
