package recordingsapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/go-chi/chi/v5"
)

// --- Merge settings endpoints ---

func (s *server) handleGetMergeSettings(w http.ResponseWriter, r *http.Request) {
	if s.deps.Config == nil {
		WriteError(w, http.StatusInternalServerError, "config not available")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":                 s.deps.Config.Merge.Enabled,
		"check_interval":          s.deps.Config.Merge.CheckInterval,
		"window_size":             s.deps.Config.Merge.WindowSize,
		"batch_limit":             s.deps.Config.Merge.BatchLimit,
		"min_segment_age":         s.deps.Config.Merge.MinSegmentAge,
		"min_segments_to_merge":   s.deps.Config.Merge.MinSegmentsToMerge,
		"rolling_fragment_hold_s": s.deps.Config.Merge.RollingFragmentHoldValue(),
		"rolling_append_bucket":   s.deps.Config.Merge.RollingAppendBucket,
	})
}

func (s *server) handleUpdateMergeSettings(w http.ResponseWriter, r *http.Request) {
	if s.deps.Config == nil {
		WriteError(w, http.StatusInternalServerError, "config not available")
		return
	}

	var body struct {
		Enabled              *bool   `json:"enabled"`
		CheckInterval        *string `json:"check_interval"`
		WindowSize           *string `json:"window_size"`
		BatchLimit           *int    `json:"batch_limit"`
		MinSegmentAge        *string `json:"min_segment_age"`
		MinSegmentsToMerge   *int    `json:"min_segments_to_merge"`
		RollingFragmentHoldS *int    `json:"rolling_fragment_hold_s"`
		RollingAppendBucket  *bool   `json:"rolling_append_bucket"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if body.Enabled != nil {
		s.deps.Config.Merge.Enabled = *body.Enabled
	}
	if body.CheckInterval != nil {
		// Blank = keep current (partial PUT). time.ParseDuration("") would 400
		// the whole save otherwise (#294). Mirrors handlers_system.go cleanup.
		if v := strings.TrimSpace(*body.CheckInterval); v != "" {
			if _, err := time.ParseDuration(v); err != nil {
				WriteError(w, http.StatusBadRequest, "check_interval must be a valid duration (e.g., \"30m\", \"1h\")")
				return
			}
			s.deps.Config.Merge.CheckInterval = v
		}
	}
	if body.WindowSize != nil {
		if v := strings.TrimSpace(*body.WindowSize); v != "" {
			if _, err := time.ParseDuration(v); err != nil {
				WriteError(w, http.StatusBadRequest, "window_size must be a valid duration (e.g., \"24h\", \"48h\")")
				return
			}
			s.deps.Config.Merge.WindowSize = v
		}
	}
	if body.BatchLimit != nil {
		if *body.BatchLimit < 1 {
			WriteError(w, http.StatusBadRequest, "batch_limit must be >= 1")
			return
		}
		s.deps.Config.Merge.BatchLimit = *body.BatchLimit
	}
	if body.MinSegmentAge != nil {
		if v := strings.TrimSpace(*body.MinSegmentAge); v != "" {
			if _, err := time.ParseDuration(v); err != nil {
				WriteError(w, http.StatusBadRequest, "min_segment_age must be a valid duration (e.g., \"1h\", \"6h\")")
				return
			}
			s.deps.Config.Merge.MinSegmentAge = v
		}
	}
	if body.MinSegmentsToMerge != nil {
		if *body.MinSegmentsToMerge < 1 {
			WriteError(w, http.StatusBadRequest, "min_segments_to_merge must be >= 1")
			return
		}
		s.deps.Config.Merge.MinSegmentsToMerge = *body.MinSegmentsToMerge
	}
	// Fragment batching window (#852): seconds a <30s MP4 fragment waits before
	// its batch folds into the hour bucket. 0 disables batching; nil keeps the
	// current value (partial PUT). Web-operable per the user's standing policy.
	if body.RollingFragmentHoldS != nil {
		if *body.RollingFragmentHoldS < 0 || *body.RollingFragmentHoldS > 3600 {
			WriteError(w, http.StatusBadRequest, "rolling_fragment_hold_s must be between 0 (off) and 3600")
			return
		}
		s.deps.Config.Merge.RollingFragmentHoldS = body.RollingFragmentHoldS
	}
	// Sequential-append bucket (#853): experimental, default OFF (RPi 3B
	// baseline — ~1-2MB RAM mirror per active camera). Existing buckets keep
	// their format; the switch affects new buckets.
	if body.RollingAppendBucket != nil {
		s.deps.Config.Merge.RollingAppendBucket = *body.RollingAppendBucket
	}

	// Persist config to disk
	if err := config.Save(s.deps.ConfigPath, s.deps.Config); err != nil {
		logger.Warn("failed to save config", "error", err)
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (s *server) handleUpdateCameraMergeConfig(w http.ResponseWriter, r *http.Request) {
	if s.deps.DB == nil {
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

	// Treat blank duration strings as "no change" (nil → COALESCE keeps the
	// existing value). A blank "" would otherwise fail time.ParseDuration and
	// 400 the whole save (#294), and ptrToNullString("") is Valid so it would
	// also overwrite the column with empty. Normalize to nil instead.
	normalizeDurationPtr := func(p *string) *string {
		if p == nil || strings.TrimSpace(*p) == "" {
			return nil
		}
		return p
	}
	checkInterval := normalizeDurationPtr(body.CheckInterval)
	windowSize := normalizeDurationPtr(body.WindowSize)
	minSegmentAge := normalizeDurationPtr(body.MinSegmentAge)
	for _, d := range []*string{checkInterval, windowSize, minSegmentAge} {
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

	if err := s.deps.DB.UpsertCameraMerge(r.Context(), cameraID,
		body.Enabled, checkInterval, windowSize, minSegmentAge,
		body.BatchLimit, body.MinSegmentsToMerge); err != nil {
		logger.Warn("failed to update camera merge config", "error", err, "camera_id", cameraID)
		WriteError(w, http.StatusInternalServerError, "failed to update merge config")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (s *server) handleDeleteCameraMergeConfig(w http.ResponseWriter, r *http.Request) {
	if s.deps.DB == nil {
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
	if err := s.deps.DB.ClearCameraMerge(r.Context(), cameraID); err != nil {
		logger.Warn("failed to clear camera merge config", "error", err, "camera_id", cameraID)
		WriteError(w, http.StatusInternalServerError, "failed to clear merge config")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "cleared"})
}

func (s *server) handleGetCameraMergeConfig(w http.ResponseWriter, r *http.Request) {
	if s.deps.DB == nil {
		WriteError(w, http.StatusInternalServerError, "database not available")
		return
	}

	cameraID := chi.URLParam(r, "id")
	if cameraID == "" {
		WriteError(w, http.StatusBadRequest, "camera ID is required")
		return
	}

	cam, err := s.deps.DB.GetCamera(r.Context(), cameraID)
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

func (s *server) handleMergeStatus(w http.ResponseWriter, r *http.Request) {
	if s.deps.Merge == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"enabled": false,
		})
		return
	}
	status := s.deps.Merge.Status()
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":         true,
		"last_run_time":   status.LastRunTime,
		"segments_merged": status.SegmentsMerged,
		"files_created":   status.FilesCreated,
		"error_count":     status.ErrorCount,
	})
}

func (s *server) handleMergePending(w http.ResponseWriter, r *http.Request) {
	if s.deps.Merge == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"enabled": false,
			"pending": map[string]int{},
		})
		return
	}
	counts := s.deps.Merge.PendingCounts(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": true,
		"pending": counts,
	})
}

// handleMergeReclassify converts existing merge_status='failed' recordings with
// empty merge_error to 'incompatible'. This backfills undersized SPS/PPS groups
// that were incorrectly classified as failures.
func (s *server) handleMergeReclassify(w http.ResponseWriter, r *http.Request) {
	if s.deps.DB == nil {
		WriteError(w, http.StatusInternalServerError, "database not available")
		return
	}

	result, err := s.deps.DB.DB().ExecContext(r.Context(), `
		UPDATE recordings SET merge_status = 'incompatible',
		  merge_error = 'reclassified from failed (no error recorded)'
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
func (s *server) handleMergeBackfillCamera(w http.ResponseWriter, r *http.Request) {
	if s.deps.RollingMerge == nil {
		WriteError(w, http.StatusServiceUnavailable, "rolling merge is not enabled")
		return
	}

	cameraID := chi.URLParam(r, "id")
	if cameraID == "" {
		WriteError(w, http.StatusBadRequest, "camera id is required")
		return
	}

	includeFailed := r.URL.Query().Get("include_failed") == "true"

	merged, err := s.deps.RollingMerge.BackfillCamera(r.Context(), cameraID, includeFailed)
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
func (s *server) handleMergeBackfillAll(w http.ResponseWriter, r *http.Request) {
	if s.deps.RollingMerge == nil {
		WriteError(w, http.StatusServiceUnavailable, "rolling merge is not enabled")
		return
	}

	includeFailed := r.URL.Query().Get("include_failed") == "true"

	// List all pending segments across all cameras. This is a status/inspection
	// endpoint — no throttling (needs the true total).
	recs, err := s.deps.DB.ListPendingSegmentsForRolling(r.Context(), "", includeFailed, 0, time.Time{})
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
		merged, err := s.deps.RollingMerge.BackfillCamera(r.Context(), cameraID, includeFailed)
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
func (s *server) handleMergeConsolidate(w http.ResponseWriter, r *http.Request) {
	if s.deps.RollingMerge == nil {
		WriteError(w, http.StatusServiceUnavailable, "rolling merge is not enabled")
		return
	}

	minDurStr := r.URL.Query().Get("min_duration")
	cfg := s.deps.Config.Merge
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

	cameraID := r.URL.Query().Get("camera_id")
	shortRecs, err := s.deps.DB.ListShortMergedRecordings(r.Context(), cameraID, minDur.Seconds())
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
		merged, err := s.deps.RollingMerge.ConsolidateShortRecord(r.Context(), cam, minDur)
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

// registerMergeRoutes registers merge status/backfill/reclassify/consolidate routes.
