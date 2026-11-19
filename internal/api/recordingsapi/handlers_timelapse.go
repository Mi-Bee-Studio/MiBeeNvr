package recordingsapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/httpx"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/timelapse"
	"github.com/go-chi/chi/v5"
)

// --- Timelapse configuration endpoints ---

// handleGetCameraTimelapse returns the timelapse configuration for a camera.
// GET /api/cameras/{id}/timelapse
func (s *server) handleGetCameraTimelapse(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	if s.deps.Config == nil {
		WriteError(w, http.StatusInternalServerError, "config not available")
		return
	}

	var tl *config.CameraTimelapseConfig
	for i := range s.deps.Config.Cameras {
		if s.deps.Config.Cameras[i].ID == id {
			tl = s.deps.Config.Cameras[i].Timelapse
			break
		}
	}

	// Return timelapse config (nil means disabled/no config)
	if tl == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"enabled":                       false,
			"interval":                      "30s",
			"frame_source":                  "auto",
			"paused":                        false,
			"delete_original":               false,
			"delete_recordings_after_merge": false,
			"merge_output_fps":              30,
			"merge_mode":                    "auto",
			"daily_merge":                   true,
			"merge_duration":                "natural-day",
		})
		return
	}

	writeJSON(w, http.StatusOK, tl)
}

// handlePutCameraTimelapse updates the timelapse configuration for a camera.
// PUT /api/cameras/{id}/timelapse
func (s *server) handlePutCameraTimelapse(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	if s.deps.Config == nil {
		WriteError(w, http.StatusInternalServerError, "config not available")
		return
	}
	if s.deps.ConfigPath == "" {
		WriteError(w, http.StatusInternalServerError, "config path not available")
		return
	}

	var raw json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	var body config.CameraTimelapseConfig
	if err := json.Unmarshal(raw, &body); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Backward compat: accept mergeDuration as alias for merge_duration
	if body.MergeDuration == "" {
		var legacy struct {
			MergeDuration string `json:"mergeDuration"`
		}
		if err := json.Unmarshal(raw, &legacy); err == nil && legacy.MergeDuration != "" {
			body.MergeDuration = legacy.MergeDuration
		}
	}

	if body.Interval != "" {
		dur, err := time.ParseDuration(body.Interval)
		if err != nil {
			WriteError(w, http.StatusBadRequest, fmt.Sprintf("interval must be a valid duration (e.g., \"5s\", \"1m\"): %v", err))
			return
		}
		if dur < time.Second {
			WriteError(w, http.StatusBadRequest, "interval must be at least 1s")
			return
		}
	}

	if body.FrameSource != "" {
		switch body.FrameSource {
		case "auto", "snapshot", "rtsp_keyframe", "mjpeg", "latest_frame":
			// valid
		default:
			WriteError(w, http.StatusBadRequest, "frame_source must be \"auto\", \"snapshot\", \"rtsp_keyframe\", \"mjpeg\", or \"latest_frame\"")
			return
		}
	}

	if body.MergeMode != "" && body.MergeMode != "auto" && body.MergeMode != "mp4" && body.MergeMode != "jpeg" {
		WriteError(w, http.StatusBadRequest, "merge_mode must be \"auto\", \"mp4\", or \"jpeg\"")
		return
	}

	if body.MergeOutputFPS != 0 && (body.MergeOutputFPS < 1 || body.MergeOutputFPS > 60) {
		WriteError(w, http.StatusBadRequest, "merge_output_fps must be between 1 and 60")
		return
	}

	found := false
	for i := range s.deps.Config.Cameras {
		if s.deps.Config.Cameras[i].ID == id {
			s.deps.Config.Cameras[i].Timelapse = &body
			if body.Interval == "" {
				s.deps.Config.Cameras[i].Timelapse.Interval = "30s"
			}
			if body.FrameSource == "" {
				s.deps.Config.Cameras[i].Timelapse.FrameSource = "auto"
			}
			if body.MergeMode == "" {
				s.deps.Config.Cameras[i].Timelapse.MergeMode = "auto"
			}
			if body.DailyMerge == nil {
				v := true
				s.deps.Config.Cameras[i].Timelapse.DailyMerge = &v
			}
			if body.MergeOutputFPS == 0 {
				s.deps.Config.Cameras[i].Timelapse.MergeOutputFPS = 30
			}
			// MergeEnabled default is nil (auto-detect)
			found = true
			break
		}
	}

	if !found {
		WriteError(w, http.StatusNotFound, "camera not found")
		return
	}

	// Persist config to disk
	if err := config.Save(s.deps.ConfigPath, s.deps.Config); err != nil {
		logger.Warn("failed to save config after timelapse update", "camera_id", id, "error", err)
		WriteError(w, http.StatusInternalServerError, "failed to save config")
		return
	}

	// Update merge scheduler if MergeDuration changed
	if s.deps.MergeScheduler != nil && body.MergeDuration != "" {
		if dur, err := config.ParseMergeDuration(body.MergeDuration); err == nil {
			s.deps.MergeScheduler.AddOrUpdate(id, dur)
			slog.Debug("timelapse: updated merge scheduler", "camera_id", id, "duration", dur)
		}
	}

	writeJSON(w, http.StatusOK, s.deps.Config.Cameras)
}

// handleTimelapseStatus returns global timelapse merge defaults.
// GET /api/timelapse/status
// SupportedTimelapseMergeDurations is the canonical list of named merge-window
// values exposed to the frontend. Order matters — the first entry is the
// default shown in dropdowns. Mirrors config.ParseMergeDuration's named
// windows (1h + 8h/12h/24h/natural-day/7d/30d). Kept here (not in config)
// because it's an API contract, not a parsing concern.
var SupportedTimelapseMergeDurations = []string{
	"1h",
	"8h",
	"12h",
	"24h",
	"natural-day",
	"7d",
	"30d",
}

func (s *server) handleTimelapseStatus(w http.ResponseWriter, r *http.Request) {
	activeCount := 0
	if s.deps.TimelapseMerge != nil {
		activeCount = s.deps.TimelapseMerge.ActiveCount()
	}
	defaultDailyMerge := true
	writeJSON(w, http.StatusOK, map[string]any{
		"merge_enabled":             false,
		"merge_mode":                "auto",
		"daily_merge":               defaultDailyMerge,
		"merge_output_fps":          30,
		"active_count":              activeCount,
		"supported_merge_durations": SupportedTimelapseMergeDurations,
	})
}

// handleTimelapseMergeProgress handles GET /api/timelapse/merge/progress/{id}.
// SSE endpoint that streams merge progress updates for a specific camera.
// It sends progress events as the merge progresses and closes when complete.
func (s *server) handleTimelapseMergeProgress(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "id")

	if s.deps.TimelapseMerge == nil {
		WriteError(w, http.StatusServiceUnavailable, "timelapse merge manager not available")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		WriteError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ctx := r.Context()

	info, ok := s.deps.TimelapseMerge.GetProgress(cameraID)
	if !ok {
		// No progress tracked yet — send an initial event with status idle.
		data, err := json.Marshal(timelapse.MergeProgressInfo{
			CameraID: cameraID,
			Progress: 0,
			Status:   "idle",
		})
		if err != nil {
			slog.Error("failed to marshal progress info", "error", err)
			return
		}
		fmt.Fprintf(w, "event: progress\ndata: %s\n\n", data)
		flusher.Flush()
		return
	}

	// If already completed or failed, send the final event and return.
	if info.Status == "completed" || info.Status == "failed" {
		data, err := json.Marshal(info)
		if err != nil {
			slog.Error("failed to marshal progress info", "error", err)
			return
		}
		fmt.Fprintf(w, "event: progress\ndata: %s\n\n", data)
		flusher.Flush()
		return
	}
	// Stream progress updates until completion.
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.deps.ShutdownSignaled():
			return
		case <-heartbeat.C:
			fmt.Fprintf(w, ": ping\n\n")
			flusher.Flush()
		case <-ticker.C:
			// Poll progress.
			info, ok = s.deps.TimelapseMerge.GetProgress(cameraID)
			if !ok {
				return
			}

			data, err := json.Marshal(info)
			if err != nil {
				slog.Error("failed to marshal progress info", "error", err)
				continue
			}
			fmt.Fprintf(w, "event: progress\ndata: %s\n\n", data)
			flusher.Flush()
			if info.Status == "completed" || info.Status == "failed" {
				return
			}
		}
	}
}

// --- Timelapse API endpoints ---

// handleTimelapseList handles GET /api/timelapse.
// Lists timelapse and MJPEG recordings with pagination.
func (s *server) handleTimelapseList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Parse pagination params with abuse prevention (cap 1000, no default).
	limit, offset := httpx.ParsePagination(r, 0, 1000)

	cameraID := r.URL.Query().Get("camera_id")
	sortBy := r.URL.Query().Get("sort_by")
	sortOrder := r.URL.Query().Get("sort_order")

	filter := model.RecordingFilter{
		Formats:   []model.Format{model.FormatTimelapse, model.FormatMJPEG},
		CameraID:  cameraID,
		Limit:     limit,
		Offset:    offset,
		SortBy:    sortBy,
		SortOrder: sortOrder,
	}

	// Get paginated results and total count in a single query.
	recordings, total, err := s.deps.DB.ListRecordingsWithTotal(ctx, filter)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to list recordings")
		return
	}

	if recordings == nil {
		recordings = []model.Recording{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"recordings": recordings,
		"total":      total,
	})
}

// handleTimelapseMerge handles POST /api/timelapse/{id}/merge.
// Triggers a merge for the specified camera. Accepts optional duration
// query param (e.g., "8h", "12h", "24h", "natural-day", "7d", "30d")
// for custom merge windows. Without duration, defaults to "natural-day"
// (24h, midnight-aligned in the configured timezone).
func (s *server) handleTimelapseMerge(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "id")

	// Optional duration query param for custom merge windows. Default to
	// "natural-day" (24h, midnight-aligned) when not specified.
	durationStr := r.URL.Query().Get("duration")
	if durationStr == "" {
		durationStr = "natural-day"
	}

	refTime, err := s.deps.TriggerManualMerge(cameraID, durationStr, r.URL.Query().Get("date"))
	if err != nil {
		writeTLTriggerError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"status":    "merge_initiated",
		"camera_id": cameraID,
		"date":      refTime.Format("2006-01-02"),
		"duration":  durationStr,
	})
}

// writeTLTriggerError maps ManualTrigger errors onto the coded-error track
// (#1009 batch 3).
func writeTLTriggerError(w http.ResponseWriter, err error) {
	var (
		active *timelapse.MergeAlreadyActiveError
		bad    *timelapse.InvalidDurationError
		down   *timelapse.TriggerShuttingDownError
	)
	switch {
	case errors.As(err, &active):
		httpx.WriteAPIError(w, http.StatusConflict, err)
	case errors.As(err, &bad):
		httpx.WriteAPIError(w, http.StatusBadRequest, err)
	case errors.As(err, &down):
		httpx.WriteAPIError(w, http.StatusServiceUnavailable, err)
	default:
		httpx.WriteAPIError(w, http.StatusInternalServerError, err)
	}
}

func (s *server) handleTimelapsePause(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "id")

	if s.deps.Config == nil {
		WriteError(w, http.StatusInternalServerError, "config not available")
		return
	}
	if s.deps.ConfigPath == "" {
		WriteError(w, http.StatusInternalServerError, "config path not available")
		return
	}

	found := false
	for i := range s.deps.Config.Cameras {
		if s.deps.Config.Cameras[i].ID == cameraID {
			if s.deps.Config.Cameras[i].Timelapse == nil {
				WriteError(w, http.StatusNotFound, "camera has no timelapse configuration")
				return
			}
			s.deps.Config.Cameras[i].Timelapse.Paused = true
			found = true
			break
		}
	}

	if !found {
		WriteError(w, http.StatusNotFound, "camera not found")
		return
	}

	// Stop the recorder via camera manager (immediate, not waiting for schedule monitor)
	if s.deps.Cam != nil {
		if err := s.deps.Cam.PauseTimelapse(r.Context(), cameraID); err != nil {
			logger.Warn("failed to pause timelapse recorder", "camera_id", cameraID, "error", err)
		}
	}

	// Persist config to disk
	if err := config.Save(s.deps.ConfigPath, s.deps.Config); err != nil {
		logger.Warn("failed to save config after timelapse pause", "camera_id", cameraID, "error", err)
		WriteError(w, http.StatusInternalServerError, "failed to save config")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "paused"})
}

// handleTimelapseResume handles POST /api/timelapse/{id}/resume.
// Resumes timelapse recording for a camera.
func (s *server) handleTimelapseResume(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "id")

	if s.deps.Config == nil {
		WriteError(w, http.StatusInternalServerError, "config not available")
		return
	}
	if s.deps.ConfigPath == "" {
		WriteError(w, http.StatusInternalServerError, "config path not available")
		return
	}

	found := false
	for i := range s.deps.Config.Cameras {
		if s.deps.Config.Cameras[i].ID == cameraID {
			if s.deps.Config.Cameras[i].Timelapse == nil {
				WriteError(w, http.StatusNotFound, "camera has no timelapse configuration")
				return
			}
			s.deps.Config.Cameras[i].Timelapse.Paused = false
			found = true
			break
		}
	}

	if !found {
		WriteError(w, http.StatusNotFound, "camera not found")
		return
	}

	// Start the recorder via camera manager (immediate, not waiting for schedule monitor)
	if s.deps.Cam != nil {
		if err := s.deps.Cam.ResumeTimelapse(r.Context(), cameraID); err != nil {
			logger.Warn("failed to resume timelapse recorder", "camera_id", cameraID, "error", err)
		}
	}

	// Persist config to disk
	if err := config.Save(s.deps.ConfigPath, s.deps.Config); err != nil {
		logger.Warn("failed to save config after timelapse resume", "camera_id", cameraID, "error", err)
		WriteError(w, http.StatusInternalServerError, "failed to save config")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "resumed"})
}

// handleTimelapseGet handles GET /api/timelapse/{id}.
// Returns metadata for a single timelapse or MJPEG recording.
func (s *server) handleTimelapseGet(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	rec, err := s.deps.DB.GetRecording(r.Context(), id)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to get recording")
		return
	}
	if rec == nil {
		WriteError(w, http.StatusNotFound, "recording not found")
		return
	}

	if rec.Format != model.Format("timelapse") && rec.Format != "mjpeg" {
		WriteError(w, http.StatusNotFound, "not a timelapse recording")
		return
	}

	writeJSON(w, http.StatusOK, rec)
}

// handleTimelapseDelete handles DELETE /api/timelapse/{id}.
// Deletes a timelapse or MJPEG recording and its associated files.
func (s *server) handleTimelapseDelete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx := r.Context()

	rec, err := s.deps.DB.GetRecording(ctx, id)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to get recording")
		return
	}
	if rec == nil {
		WriteError(w, http.StatusNotFound, "recording not found")
		return
	}

	if rec.Format != model.Format("timelapse") && rec.Format != "mjpeg" {
		WriteError(w, http.StatusNotFound, "not a timelapse recording")
		return
	}

	// Delete from DB first (authoritative source)
	if err := s.deps.DB.DeleteRecording(ctx, id); err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to delete recording")
		return
	}

	if rec.MergePath != "" {
		if err := os.RemoveAll(rec.MergePath); err != nil {
			logger.Warn("failed to delete merged file", "merge_path", rec.MergePath, "error", err)
		}
	}

	if rec.FilePath != "" {
		info, err := os.Stat(rec.FilePath)
		if err == nil && info.IsDir() {
			if err := os.RemoveAll(rec.FilePath); err != nil {
				logger.Warn("failed to delete segment directory", "file_path", rec.FilePath, "error", err)
			}
		} else if err == nil && !info.IsDir() {
			// File-based recording
			if err := s.deps.Store.DeleteFile(rec.FilePath); err != nil {
				logger.Warn("failed to delete file", "file_path", rec.FilePath, "error", err)
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// handleTimelapseDownload handles POST /api/timelapse/{id}/download.
// Serves the merged MP4 file for a timelapse recording.
func (s *server) handleTimelapseDownload(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	rec, err := s.deps.DB.GetRecording(r.Context(), id)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to get recording")
		return
	}
	if rec == nil {
		WriteError(w, http.StatusNotFound, "recording not found")
		return
	}

	if rec.MergeStatus != "merged" || rec.MergePath == "" {
		WriteError(w, http.StatusNotFound, "merged recording not available")
		return
	}

	// Verify the merged MP4 exists on disk before serving
	if _, err := os.Stat(rec.MergePath); err != nil {
		logger.Warn("timelapse download: merged MP4 missing on disk",
			"recording_id", id, "merge_path", rec.MergePath, "error", err)
		WriteError(w, http.StatusNotFound, "merged recording file not available")
		return
	}

	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filepath.Base(rec.MergePath)))
	s.deps.ServeFileBudgeted(w, r, rec.MergePath)
}

// handleRetryTimelapseMerge handles POST /api/recordings/{id}/retry-merge.
// Retries the merge for a failed timelapse recording by re-triggering the rolling merge.
func (s *server) handleRetryTimelapseMerge(w http.ResponseWriter, r *http.Request) {
	recordingID := chi.URLParam(r, "id")

	if s.deps.TimelapseMerge == nil {
		WriteError(w, http.StatusServiceUnavailable, "timelapse merge manager not available")
		return
	}

	if s.deps.DB == nil {
		WriteError(w, http.StatusInternalServerError, "database not available")
		return
	}

	// Fetch the recording from DB. GetRecording returns (nil, nil) for a
	// missing row (storage convention) — the nil check is not optional.
	rec, err := s.deps.DB.GetRecording(r.Context(), recordingID)
	if err != nil || rec == nil {
		WriteError(w, http.StatusNotFound, "recording not found")
		return
	}

	// Only timelapse recordings can be retried.
	if rec.Format != "timelapse" {
		WriteError(w, http.StatusBadRequest, "only timelapse recordings can be retried")
		return
	}

	// Allow retry for failed or pending recordings.
	if rec.MergeStatus != "failed" && rec.MergeStatus != "pending" {
		WriteError(w, http.StatusBadRequest, "recording is not in a retryable state (current: "+rec.MergeStatus+")")
		return
	}

	// The file_path points to the frame directory.
	frameDir := rec.FilePath
	if frameDir == "" {
		WriteError(w, http.StatusBadRequest, "recording has no frame directory")
		return
	}

	outputPath := frameDir + ".mp4"

	// Delete old broken MP4 if it exists.
	os.Remove(outputPath)

	// Reset merge progress in DB.
	if dbErr := s.deps.DB.UpdateMergeProgress(r.Context(), recordingID, 0); dbErr != nil {
		logger.Warn("retry-merge: failed to reset progress", "recording_id", recordingID, "error", dbErr)
	}

	// Trigger the rolling merge.
	s.deps.TimelapseMerge.StartSegmentMerge(context.Background(), rec.CameraID, frameDir, outputPath, recordingID)

	writeJSON(w, http.StatusAccepted, map[string]any{
		"status":       "merge_initiated",
		"recording_id": recordingID,
		"camera_id":    rec.CameraID,
		"frame_dir":    frameDir,
	})
}

// --- Task 7: Merge Cancellation ---
// handleTimelapseMergeCancel handles DELETE /api/timelapse/{id}/merge.
// Cancels an active rolling merge for the specified camera.
func (s *server) handleTimelapseMergeCancel(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "id")

	if s.deps.TimelapseMerge == nil {
		WriteError(w, http.StatusServiceUnavailable, "timelapse merge manager not available")
		return
	}

	if !s.deps.TimelapseMerge.IsActive(cameraID) {
		WriteError(w, http.StatusNotFound, "no active merge for this camera")
		return
	}

	s.deps.TimelapseMerge.StopSegmentMerge(cameraID)

	writeJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
}

// --- Task 12: Batch Merge ---
// handleTimelapseBatchMerge handles POST /api/timelapse/batch-merge.
// Triggers a merge for multiple cameras at once (max 10).
func (s *server) handleTimelapseBatchMerge(w http.ResponseWriter, r *http.Request) {
	var body struct {
		CameraIDs []string `json:"camera_ids"`
		Duration  string   `json:"duration"`
		Date      string   `json:"date"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if len(body.CameraIDs) == 0 {
		WriteError(w, http.StatusBadRequest, "camera_ids must not be empty")
		return
	}

	if len(body.CameraIDs) > 10 {
		WriteError(w, http.StatusBadRequest, "batch size exceeds maximum of 10 cameras")
		return
	}

	if body.Duration == "" {
		body.Duration = "natural-day"
	}

	results, err := s.deps.TriggerBatchMerge(body.CameraIDs, body.Duration, body.Date)
	if err != nil {
		writeTLTriggerError(w, err)
		return
	}

	triggered := 0
	for _, res := range results {
		if res.Status == "merge_initiated" {
			triggered++
		}
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"results":   results,
		"triggered": triggered,
	})
}

// --- Periodic timelapse-merge outputs (timelapse_merges table) ---
//
// These endpoints expose the long-window timelapse videos (8h / 12h / 24h /
// natural-day / 7d / 30d) produced by the PeriodicMergeManager. Each row
// represents one synthesized MP4 covering a window of source segments.

// handleListTimelapseMerges handles GET /api/timelapse/merges.
// Returns a paginated list of periodic-merge outputs.
//
// Query params:
//
//	camera_id       — filter by camera
//	start, end      — RFC3339 bounds on window_start (inclusive)
//	duration        — exact duration_label match (e.g. "24h", "natural-day")
//	status          — exact status match (pending/merging/completed/failed)
//	limit, offset   — pagination (limit capped at 1000)
func (s *server) handleListTimelapseMerges(w http.ResponseWriter, r *http.Request) {
	if s.deps.DB == nil {
		WriteError(w, http.StatusInternalServerError, "database not available")
		return
	}

	limit, offset := httpx.ParsePagination(r, 100, 1000)

	f := storage.TimelapseMergeFilter{
		CameraID:      r.URL.Query().Get("camera_id"),
		DurationLabel: r.URL.Query().Get("duration"),
		Status:        r.URL.Query().Get("status"),
		Limit:         limit,
		Offset:        offset,
	}
	if v := r.URL.Query().Get("start"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.StartTime = t
		}
	}
	if v := r.URL.Query().Get("end"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.EndTime = t
		}
	}

	merges, err := s.deps.DB.ListTimelapseMerges(r.Context(), f)
	if err != nil {
		logger.Error("list timelapse merges failed", "error", err)
		WriteError(w, http.StatusInternalServerError, "failed to list timelapse merges")
		return
	}
	total, err := s.deps.DB.CountTimelapseMerges(r.Context(), f)
	if err != nil {
		// Count is best-effort — a failed COUNT must not fail the list request.
		total = 0
	}
	if merges == nil {
		merges = []model.TimelapseMerge{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"merges": merges,
		"total":  total,
	})
}

// handleGetTimelapseMerge handles GET /api/timelapse/merges/{id}.
func (s *server) handleGetTimelapseMerge(w http.ResponseWriter, r *http.Request) {
	if s.deps.DB == nil {
		WriteError(w, http.StatusInternalServerError, "database not available")
		return
	}
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		WriteError(w, http.StatusBadRequest, "invalid merge id")
		return
	}
	m, err := s.deps.DB.GetTimelapseMerge(r.Context(), id)
	if err != nil {
		WriteError(w, http.StatusNotFound, "timelapse merge not found")
		return
	}
	writeJSON(w, http.StatusOK, m)
}

// handleDownloadTimelapseMerge handles GET /api/timelapse/merges/{id}/download.
// Serves the merged MP4 with range support (http.ServeFile). The
// X-Timelapse-Codec header is set so the frontend player can decide whether
// to use <video> (h264/h265) or fall back to the JPEG frame cycler (mjpeg).
func (s *server) handleDownloadTimelapseMerge(w http.ResponseWriter, r *http.Request) {
	if s.deps.DB == nil {
		WriteError(w, http.StatusInternalServerError, "database not available")
		return
	}
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		WriteError(w, http.StatusBadRequest, "invalid merge id")
		return
	}
	m, err := s.deps.DB.GetTimelapseMerge(r.Context(), id)
	if err != nil || m == nil {
		WriteError(w, http.StatusNotFound, "timelapse merge not found")
		return
	}
	if m.Status != model.TimelapseMergeStatusCompleted || m.OutputPath == "" {
		WriteError(w, http.StatusNotFound, "timelapse merge output not available")
		return
	}
	if _, err := os.Stat(m.OutputPath); err != nil {
		logger.Warn("timelapse merge output missing on disk",
			"merge_id", id, "output_path", m.OutputPath, "error", err)
		WriteError(w, http.StatusNotFound, "timelapse merge output file not available")
		return
	}
	// Surface codec so the frontend can pick the right player path.
	if m.Codec != "" {
		w.Header().Set("X-Timelapse-Codec", m.Codec)
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filepath.Base(m.OutputPath)))
	s.deps.ServeFileBudgeted(w, r, m.OutputPath)
}

// handleDeleteTimelapseMerge handles DELETE /api/timelapse/merges/{id}.
// Removes the DB row and the output file on disk. Does NOT touch the source
// segments that were folded into the merge (those are independent recordings).
func (s *server) handleDeleteTimelapseMerge(w http.ResponseWriter, r *http.Request) {
	if s.deps.DB == nil {
		WriteError(w, http.StatusInternalServerError, "database not available")
		return
	}
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		WriteError(w, http.StatusBadRequest, "invalid merge id")
		return
	}
	m, err := s.deps.DB.GetTimelapseMerge(r.Context(), id)
	if err != nil || m == nil {
		WriteError(w, http.StatusNotFound, "timelapse merge not found")
		return
	}
	// DB-first: remove the row, then best-effort remove the file.
	if err := s.deps.DB.DeleteTimelapseMerge(r.Context(), id); err != nil {
		logger.Error("delete timelapse merge row failed", "id", id, "error", err)
		WriteError(w, http.StatusInternalServerError, "failed to delete timelapse merge")
		return
	}
	if m.OutputPath != "" {
		if err := os.Remove(m.OutputPath); err != nil && !os.IsNotExist(err) {
			logger.Warn("delete timelapse merge output file failed (DB row already removed)",
				"id", id, "output_path", m.OutputPath, "error", err)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "deleted"})
}

// registerTimelapseRoutes registers timelapse list/status/merge/pause/resume routes.
// IMPORTANT: the /api/timelapse/merges/* static paths MUST be registered BEFORE
// the /api/timelapse/{id} wildcard routes, or chi will route /merges to {id}.
