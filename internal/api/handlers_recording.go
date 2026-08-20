package api

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/avi"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/mediaprobe"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/middleware"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/go-chi/chi/v5"
)

// --- Recording endpoints ---

func (h *Handler) handleListRecordings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	filter := model.RecordingFilter{
		CameraID: r.URL.Query().Get("camera_id"),
		Format:   model.Format(r.URL.Query().Get("format")),
	}

	if v := r.URL.Query().Get("merged"); v != "" {
		merged := v == "true" || v == "1"
		filter.Merged = &merged
	}

	if v := r.URL.Query().Get("start"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			filter.StartTime = t
		}
	}

	if v := r.URL.Query().Get("end"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			filter.EndTime = t
		}
	}

	// limit/offset parsing centralized in parsePagination (#222); recordings use a
	// safe default of 50 and a hard cap of 500 to prevent accidental full-table scans.
	filter.Limit, filter.Offset = parsePagination(r, 50, 500)

	// Keyset (cursor) pagination: ?cursor=<RFC3339 started_at of last row on prev page>.
	// When provided with default sort, the DB uses WHERE started_at < cursor (O(1) deep page)
	// instead of OFFSET (O(N) scan-skip). The frontend sends the last row's started_at.
	filter.Cursor = r.URL.Query().Get("cursor")

	// Sorting
	filter.SortBy = r.URL.Query().Get("sort_by")
	filter.SortOrder = r.URL.Query().Get("order")

	filter.Search = r.URL.Query().Get("search")

	// AI class filter: only recordings that have an AI event with this class_name.
	filter.AiClass = r.URL.Query().Get("ai_class")

	// Motion filters (issue #435): ?min_motion_score=0.2 keeps only segments
	// with activity; ?activity=static|motion|scene_cut matches flags.
	if v := r.URL.Query().Get("min_motion_score"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			filter.MinMotionScore = &f
		}
	}
	filter.Activity = r.URL.Query().Get("activity")

	// List + cached count. Cursor-based requests still get the total from cache.
	recordings, total, err := h.db.ListRecordingsWithTotal(ctx, filter)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to list recordings")
		return
	}

	if recordings == nil {
		recordings = []model.Recording{}
	}

	// Compute next_cursor for the frontend: the started_at of the last row in this page.
	// The client passes it back as ?cursor= for O(1) deep pagination. Empty when no more rows.
	nextCursor := ""
	if filter.Limit > 0 && len(recordings) == filter.Limit {
		last := recordings[len(recordings)-1]
		nextCursor = last.StartedAt.Format(time.RFC3339Nano)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"recordings":  recordings,
		"total":       total,
		"next_cursor": nextCursor,
	})
}

// handleTimelineSegments returns the lightweight timeline projection of a day's
// recordings for the recordings-page day strip and the player DVR bar. Unlike
// handleListRecordings (capped at 500 full rows), this selects only 7 small
// columns per row and caps at maxTimelineSegments (10k), so a full fragmented
// day ships in one response without silently truncating the afternoon.
//
// Query params mirror handleListRecordings: start/end (RFC3339), camera_id,
// format, merged. Sorting is fixed to started_at ASC (timelines render L→R).
// Response: {segments: [...], total: N, truncated: bool}. Issue #115.
//
// GET /api/recordings/timeline
func (h *Handler) handleTimelineSegments(w http.ResponseWriter, r *http.Request) {
	filter := model.RecordingFilter{
		CameraID: r.URL.Query().Get("camera_id"),
		Format:   model.Format(r.URL.Query().Get("format")),
	}
	if v := r.URL.Query().Get("merged"); v != "" {
		merged := v == "true" || v == "1"
		filter.Merged = &merged
	}
	if v := r.URL.Query().Get("start"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			filter.StartTime = t
		}
	}
	if v := r.URL.Query().Get("end"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			filter.EndTime = t
		}
	}
	filter.AiClass = r.URL.Query().Get("ai_class")
	if v := r.URL.Query().Get("min_motion_score"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			filter.MinMotionScore = &f
		}
	}
	filter.Activity = r.URL.Query().Get("activity")

	segments, total, err := h.db.ListRecordingTimelineSegments(r.Context(), filter)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to list timeline segments")
		return
	}
	if segments == nil {
		segments = []model.TimelineSegment{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"segments":  segments,
		"total":     total,
		"truncated": total > len(segments),
	})
}

// handleDailyRecordingSummary returns per-day recording counts and format categories for
// calendar rendering. Unlike handleListRecordings, this is a lightweight GROUP BY query
// with no row-level limit — the result is bounded by the number of days in the range.
// GET /api/recordings/daily-summary?start=&end=&camera_id=&format=&formats=&tz_offset=
func (h *Handler) handleDailyRecordingSummary(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	filter := model.RecordingFilter{
		CameraID: r.URL.Query().Get("camera_id"),
		Format:   model.Format(r.URL.Query().Get("format")),
	}

	if v := r.URL.Query().Get("merged"); v != "" {
		merged := v == "true" || v == "1"
		filter.Merged = &merged
	}
	if v := r.URL.Query().Get("start"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			filter.StartTime = t
		}
	}
	if v := r.URL.Query().Get("end"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			filter.EndTime = t
		}
	}
	filter.AiClass = r.URL.Query().Get("ai_class")

	// formats: comma-separated list (e.g. "timelapse,mjpeg")
	if v := r.URL.Query().Get("formats"); v != "" {
		for _, f := range strings.Split(v, ",") {
			if f = strings.TrimSpace(f); f != "" {
				filter.Formats = append(filter.Formats, model.Format(f))
			}
		}
	}

	filter.Search = r.URL.Query().Get("search")

	if v := r.URL.Query().Get("archived"); v != "" {
		archived := v == "true" || v == "1"
		filter.Archived = &archived
	}

	// Client timezone offset in minutes (e.g. 480 for UTC+8). Defaults to 0 (UTC).
	tzOffset := 0
	if v := r.URL.Query().Get("tz_offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			tzOffset = n
		}
	}

	summary, err := h.db.DailyRecordingSummary(ctx, filter, tzOffset)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to get daily summary")
		return
	}

	if summary == nil {
		summary = []model.RecordingDaySummary{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"days": summary,
	})
}

// handleTimelineSeekEvent records a timeline seek for observability (0.8.0 M6).
// Body: {"camera_id":"front-door","type":"segment"}
// type is "segment" (cross-recording) or "intra" (within same recording).
// handleCreateRecording allows MiBeeVision (or other authenticated clients) to register
// a recording in the NVR database. Requires API Key authentication.
// POST /api/recordings  body: {camera_id, file_path, format, started_at, ...}
func (h *Handler) handleCreateRecording(w http.ResponseWriter, r *http.Request) {
	if !middleware.IsAPIKeyAuthenticated(r.Context()) {
		WriteError(w, http.StatusUnauthorized, "API key required")
		return
	}
	var body struct {
		ID         string  `json:"id"`
		CameraID   string  `json:"camera_id"`
		FilePath   string  `json:"file_path"`
		Format     string  `json:"format"`
		StartedAt  string  `json:"started_at"`
		EndedAt    string  `json:"ended_at"`
		Duration   float64 `json:"duration"`
		FileSize   int64   `json:"file_size"`
		FrameCount int     `json:"frame_count"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.CameraID == "" || body.FilePath == "" || body.Format == "" {
		WriteError(w, http.StatusBadRequest, "camera_id, file_path, and format are required")
		return
	}

	rec := &model.Recording{
		ID:          body.ID,
		CameraID:    body.CameraID,
		FilePath:    body.FilePath,
		Format:      model.Format(body.Format),
		Duration:    body.Duration,
		FileSize:    body.FileSize,
		FrameCount:  body.FrameCount,
		MergeStatus: "pending",
	}
	if rec.ID == "" {
		rec.ID = strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	if body.StartedAt != "" {
		rec.StartedAt, _ = time.Parse(time.RFC3339, body.StartedAt)
	} else {
		rec.StartedAt = time.Now().UTC()
	}
	if body.EndedAt != "" {
		rec.EndedAt, _ = time.Parse(time.RFC3339, body.EndedAt)
	}

	if err := h.db.InsertRecording(r.Context(), rec); err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to create recording")
		return
	}

	logger.Info("recording created via API", "id", rec.ID, "camera_id", rec.CameraID, "source", middleware.APIKeyNameFromContext(r.Context()))
	writeJSON(w, http.StatusCreated, map[string]string{"id": rec.ID, "status": "created"})
}

// handleUpdateRecording allows MiBeeVision to update recording metadata.
// Requires API Key authentication.
// PATCH /api/recordings/{id}  body: {file_path?, format?, duration?, ...}
func (h *Handler) handleUpdateRecording(w http.ResponseWriter, r *http.Request) {
	if !middleware.IsAPIKeyAuthenticated(r.Context()) {
		WriteError(w, http.StatusUnauthorized, "API key required")
		return
	}
	id := chi.URLParam(r, "id")
	if id == "" {
		WriteError(w, http.StatusBadRequest, "id is required")
		return
	}

	// Fetch existing recording
	existing, err := h.db.GetRecording(r.Context(), id)
	if err != nil || existing == nil {
		WriteError(w, http.StatusNotFound, "recording not found")
		return
	}

	var body struct {
		FilePath   *string  `json:"file_path"`
		Format     *string  `json:"format"`
		EndedAt    *string  `json:"ended_at"`
		Duration   *float64 `json:"duration"`
		FileSize   *int64   `json:"file_size"`
		FrameCount *int     `json:"frame_count"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Apply partial updates
	if body.FilePath != nil {
		existing.FilePath = *body.FilePath
	}
	if body.Format != nil {
		existing.Format = model.Format(*body.Format)
	}
	if body.EndedAt != nil {
		existing.EndedAt, _ = time.Parse(time.RFC3339, *body.EndedAt)
	}
	if body.Duration != nil {
		existing.Duration = *body.Duration
	}
	if body.FileSize != nil {
		existing.FileSize = *body.FileSize
	}
	if body.FrameCount != nil {
		existing.FrameCount = *body.FrameCount
	}

	if err := h.db.UpdateRecording(r.Context(), existing); err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to update recording")
		return
	}

	logger.Info("recording updated via API", "id", id, "source", middleware.APIKeyNameFromContext(r.Context()))
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "status": "updated"})
}

// handleUpdateRecordingAIStatus allows MiBeeVision to update the AI processing
// status of a recording. Requires API Key authentication.
// PATCH /api/recordings/{id}/ai-status  body: {"ai_status":"completed", "ai_error":""}
// Valid ai_status values: pending, processing, completed, failed.
func (h *Handler) handleUpdateRecordingAIStatus(w http.ResponseWriter, r *http.Request) {
	if !middleware.IsAPIKeyAuthenticated(r.Context()) {
		WriteError(w, http.StatusUnauthorized, "API key required")
		return
	}
	id := chi.URLParam(r, "id")
	if id == "" {
		WriteError(w, http.StatusBadRequest, "id is required")
		return
	}

	var body struct {
		AIStatus string `json:"ai_status"`
		AIError  string `json:"ai_error"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Validate ai_status value.
	switch body.AIStatus {
	case "pending", "processing", "completed", "failed":
		// ok
	default:
		WriteError(w, http.StatusBadRequest, "invalid ai_status; must be one of: pending, processing, completed, failed")
		return
	}

	// Verify recording exists.
	existing, err := h.db.GetRecording(r.Context(), id)
	if err != nil || existing == nil {
		WriteError(w, http.StatusNotFound, "recording not found")
		return
	}

	if err := h.db.UpdateRecordingAIStatus(r.Context(), id, body.AIStatus, body.AIError); err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to update AI status")
		return
	}

	logger.Info("recording AI status updated", "id", id, "ai_status", body.AIStatus,
		"source", middleware.APIKeyNameFromContext(r.Context()))
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "ai_status": body.AIStatus})
}

func (h *Handler) handleTimelineSeekEvent(w http.ResponseWriter, r *http.Request) {
	var body struct {
		CameraID string `json:"camera_id"`
		Type     string `json:"type"` // "segment" | "intra"
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	seekType := body.Type
	if seekType != "segment" && seekType != "intra" {
		seekType = "segment"
	}
	cameraID := body.CameraID
	if cameraID == "" {
		cameraID = "unknown"
	}
	if apiMetrics != nil {
		apiMetrics.TimelineSeeksTotal.WithLabelValues(cameraID, seekType).Inc()
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "recorded"})
}

func (h *Handler) handleGetRecording(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	rec, err := h.db.GetRecording(r.Context(), id)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to get recording")
		return
	}
	if rec == nil {
		WriteError(w, http.StatusNotFound, "recording not found")
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

func (h *Handler) handleDeleteRecording(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx := r.Context()

	rec, err := h.db.GetRecording(ctx, id)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to get recording")
		return
	}
	if rec == nil {
		WriteError(w, http.StatusNotFound, "recording not found")
		return
	}

	// Delete from DB first (authoritative source)
	if err := h.db.DeleteRecording(ctx, id); err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to delete recording")
		return
	}

	// Then delete on-disk files (non-fatal if they fail).
	// The merged MP4 (merge_path) is the largest artifact and the one playback
	// actually loads; without this it leaked permanently because the orphan
	// scanner never reaches the nested YYYYMM/DD/HH/ tree. Mirrors
	// handleTimelapseDelete. os.RemoveAll tolerates a missing path.
	if rec.MergePath != "" {
		if err := os.RemoveAll(rec.MergePath); err != nil {
			logger.Warn("failed to delete merged file", "merge_path", rec.MergePath, "error", err)
		}
	}
	if rec.FilePath != "" {
		if err := h.store.DeleteFile(rec.FilePath); err != nil {
			logger.Warn("failed to delete file", "file_path", rec.FilePath, "error", err)
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (h *Handler) handleBatchDeleteRecordings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var body struct {
		IDs []string `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(body.IDs) == 0 {
		WriteError(w, http.StatusBadRequest, "ids must not be empty")
		return
	}
	if len(body.IDs) > 100 {
		WriteError(w, http.StatusBadRequest, "ids must not exceed 100")
		return
	}
	// Fetch on-disk paths before batch delete (need both source file_path and
	// the merged MP4 merge_path — see handleDeleteRecording for why merge_path
	// must be reclaimed here too).
	type recPaths struct {
		filePath  string
		mergePath string
	}
	paths := map[string]recPaths{}
	recordings, err := h.db.GetRecordingsByIDBatch(ctx, body.IDs)
	if err != nil {
		logger.Warn("batch delete: failed to fetch recordings", "error", err)
	} else {
		for _, rec := range recordings {
			paths[rec.ID] = recPaths{filePath: rec.FilePath, mergePath: rec.MergePath}
		}
	}

	// Delete DB records (transaction)
	deleted, err := h.db.DeleteRecordingsBatch(ctx, body.IDs)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to delete recordings")
		return
	}

	// Attempt file deletion for successfully deleted records (non-fatal)
	failed := []string{}
	deletedSet := make(map[string]bool, len(deleted))
	for _, id := range deleted {
		deletedSet[id] = true
		p, ok := paths[id]
		if !ok {
			continue
		}
		if p.mergePath != "" {
			if err := os.RemoveAll(p.mergePath); err != nil {
				logger.Warn("batch delete: failed to delete merged file", "merge_path", p.mergePath, "error", err)
			}
		}
		if p.filePath != "" {
			if err := h.store.DeleteFile(p.filePath); err != nil {
				logger.Warn("batch delete: failed to delete file", "file_path", p.filePath, "error", err)
			}
		}
	}
	for _, id := range body.IDs {
		if !deletedSet[id] {
			failed = append(failed, id)
		}
	}

	result := map[string]any{"deleted": deleted}
	if len(failed) > 0 {
		result["failed"] = failed
	} else {
		result["failed"] = []string{}
	}
	writeJSON(w, http.StatusOK, result)
}

// sortedImageFiles returns the sorted list of image filenames in dir, using a short-TTL
// cache to avoid os.ReadDir + sort on every request. The cache is invalidated when the
// directory's mtime changes (new frames written) or after frameListCacheTTL. This matters
// for MJPEG/timelapse frame dirs which can hold thousands of JPEGs; without it each
// ?frame=N / list-frames request re-scanned and re-sorted the whole directory.
func (h *Handler) sortedImageFiles(dir string) ([]string, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	mtime := info.ModTime().Unix()

	h.frameListMu.Lock()
	if h.frameListCache == nil {
		h.frameListCache = make(map[string]*frameListEntry)
	}
	cached, ok := h.frameListCache[dir]
	if ok && cached.dirMtime == mtime && time.Since(cached.scannedAt) < frameListCacheTTL {
		names := cached.names
		h.frameListMu.Unlock()
		return names, nil
	}
	h.frameListMu.Unlock()

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if isImageFile(e.Name()) {
			names = append(names, e.Name())
		}
	}
	sort.Slice(names, func(i, j int) bool { return names[i] < names[j] })

	h.frameListMu.Lock()
	h.frameListCache[dir] = &frameListEntry{names: names, dirMtime: mtime, scannedAt: time.Now()}
	h.frameListMu.Unlock()
	return names, nil
}

func (h *Handler) handleDownloadRecording(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	rec, err := h.db.GetRecording(r.Context(), id)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to get recording")
		return
	}
	if rec == nil {
		WriteError(w, http.StatusNotFound, "recording not found")
		return
	}

	// Timelapse recordings are JPEG sequences — cannot be downloaded as a single file.
	if rec.Format == model.Format("timelapse") {
		WriteError(w, http.StatusBadRequest, fmt.Sprintf("Timelapse recordings (JPEG sequences) cannot be downloaded as a single file. Use /api/recordings/%s/timelapse-frames to access individual frames.", id))
		return
	}
	if rec.FilePath == "" {
		WriteError(w, http.StatusNotFound, "file not available")
		return
	}

	// Validate that the recording file path is within the storage root to prevent
	// path traversal. This ensures rec.FilePath (which may come from external
	// sources like WebDAV uploads) is confined to the storage directory.
	validPath, err := storage.ValidatePath(h.store.RootDir(), rec.FilePath)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, &model.PathTraversalError{Path: rec.FilePath})
		return
	}

	// Check for frame parameter (MJPEG frame download)
	frameStr := r.URL.Query().Get("frame")
	if frameStr != "" && rec.Format == model.FormatMJPEG {
		frameIndex, err := strconv.Atoi(frameStr)
		if err == nil {
			jpgFiles, err := h.sortedImageFiles(validPath)
			if err == nil {
				if frameIndex >= 0 && frameIndex < len(jpgFiles) {
					framePath := filepath.Join(validPath, jpgFiles[frameIndex])
					http.ServeFile(w, r, framePath)
					return
				}
			}
		}
		WriteError(w, http.StatusNotFound, "frame not found")
		return
	}

	filePath := validPath
	info, err := os.Stat(filePath)
	if err != nil {
		WriteError(w, http.StatusNotFound, "file not found")
		return
	}
	if info.IsDir() {
		entries, err := os.ReadDir(filePath)
		if err != nil || len(entries) == 0 {
			WriteError(w, http.StatusNotFound, "no files in recording directory")
			return
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if strings.HasSuffix(name, ".jpg") || strings.HasSuffix(name, ".jpeg") || strings.HasSuffix(name, ".mp4") {
				filePath = filepath.Join(filePath, name)
				break
			}
		}
	}

	w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=\"%s\"", filepath.Base(filePath)))
	http.ServeFile(w, r, filePath)
}

func (h *Handler) handleListFrames(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	rec, err := h.db.GetRecording(r.Context(), id)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to get recording")
		return
	}
	if rec == nil {
		WriteError(w, http.StatusNotFound, "recording not found")
		return
	}

	if rec.Format != "mjpeg" {
		WriteError(w, http.StatusBadRequest, "not a JPEG recording")
		return
	}

	filePath := rec.FilePath
	info, err := os.Stat(filePath)
	if err != nil {
		WriteError(w, http.StatusNotFound, "recording files not found")
		return
	}
	if !info.IsDir() {
		WriteError(w, http.StatusNotFound, "recording is not a directory")
		return
	}

	entries, err := os.ReadDir(filePath)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to read recording directory")
		return
	}

	type FrameInfo struct {
		Index    int    `json:"index"`
		Filename string `json:"filename"`
		Size     int64  `json:"size"`
	}

	var frames []FrameInfo
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(strings.ToLower(name), ".jpg") && !strings.HasSuffix(strings.ToLower(name), ".jpeg") {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		frames = append(frames, FrameInfo{
			Filename: name,
			Size:     fi.Size(),
		})
	}

	// Sort by filename (natural order - timestamp-based names sort correctly)
	sort.Slice(frames, func(i, j int) bool {
		return frames[i].Filename < frames[j].Filename
	})

	// Assign sequential indices
	for i := range frames {
		frames[i].Index = i
	}

	if frames == nil {
		frames = []FrameInfo{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"frames": frames,
	})
}

// --- Timelapse endpoints ---

// isTimelapseFrame checks if a filename has a supported timelapse frame extension.
func isTimelapseFrame(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".jpg") || strings.HasSuffix(lower, ".jpeg") ||
		strings.HasSuffix(lower, ".h264") || strings.HasSuffix(lower, ".h265")
}

// handleTimelapseFrames handles GET /api/recordings/{id}/timelapse-frames.
// Returns JSON array of frame metadata for timelapse recordings.
func (h *Handler) handleTimelapseFrames(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	rec, err := h.db.GetRecording(r.Context(), id)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to get recording")
		return
	}
	if rec == nil {
		WriteError(w, http.StatusNotFound, "recording not found")
		return
	}
	if rec.Format == model.FormatAVI {
		h.handleAviFrames(w, r, rec)
		return
	}
	if rec.Format != model.FormatTimelapse && rec.Format != model.FormatMJPEG {
		WriteError(w, http.StatusNotFound, "not a timelapse or MJPEG recording")
		return
	}

	info, err := os.Stat(rec.FilePath)
	if err != nil {
		WriteError(w, http.StatusNotFound, "timelapse directory not found")
		return
	}
	if !info.IsDir() {
		WriteError(w, http.StatusNotFound, "timelapse recording is not a directory")
		return
	}

	entries, err := os.ReadDir(rec.FilePath)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to read timelapse directory")
		return
	}

	type TimelapseFrameInfo struct {
		Filename  string `json:"filename"`
		URL       string `json:"url"`
		Size      int64  `json:"size"`
		Timestamp string `json:"timestamp"`
	}

	var frames []TimelapseFrameInfo
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !isTimelapseFrame(name) {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		frames = append(frames, TimelapseFrameInfo{
			Filename:  name,
			URL:       fmt.Sprintf("/api/recordings/%s/timelapse-frames/%s", id, name),
			Size:      fi.Size(),
			Timestamp: extractFrameTimestamp(name, filepath.Join(rec.FilePath, name), fi.ModTime()).UTC().Format(time.RFC3339),
		})
	}

	sort.Slice(frames, func(i, j int) bool {
		return frames[i].Filename < frames[j].Filename
	})

	if frames == nil {
		frames = []TimelapseFrameInfo{}
	}

	writeJSON(w, http.StatusOK, frames)
}

// handleAviFrames serves the timelapse-frames listing contract for AVI
// recordings (#321): video frames (complete JPEGs) indexed by walking the
// movi chunk headers. The frontend JPEG cycler — which already has seamless
// cross-segment chaining and seeking — consumes this exactly like timelapse
// frames, giving AVI recordings seekable playback instead of the realtime-only
// WebSocket player.
func (h *Handler) handleAviFrames(w http.ResponseWriter, r *http.Request, rec *model.Recording) {
	f, err := os.Open(rec.FilePath)
	if err != nil {
		WriteError(w, http.StatusNotFound, "AVI file not found")
		return
	}
	defer f.Close()

	dmx, err := avi.NewDemuxer(f)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "parse AVI: "+err.Error())
		return
	}
	entries, err := dmx.VideoFrameIndex()
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "index AVI frames: "+err.Error())
		return
	}

	type TimelapseFrameInfo struct {
		Filename  string `json:"filename"`
		URL       string `json:"url"`
		Size      int64  `json:"size"`
		Timestamp string `json:"timestamp"`
	}
	started := rec.StartedAt
	frames := make([]TimelapseFrameInfo, 0, len(entries))
	for _, e := range entries {
		name := fmt.Sprintf("f%06d.jpg", e.Index)
		frames = append(frames, TimelapseFrameInfo{
			Filename:  name,
			URL:       fmt.Sprintf("/api/recordings/%s/timelapse-frames/%s", rec.ID, name),
			Size:      int64(e.Size),
			Timestamp: started.Add(time.Duration(e.PTSUs) * time.Microsecond).UTC().Format(time.RFC3339),
		})
	}
	writeJSON(w, http.StatusOK, frames)
}

// handleAviFrame serves one JPEG frame out of an AVI recording by synthetic
// filename (f%06d.jpg — see handleAviFrames).
func (h *Handler) handleAviFrame(w http.ResponseWriter, r *http.Request, rec *model.Recording, filename string) {
	var idx int
	if _, err := fmt.Sscanf(filename, "f%d.jpg", &idx); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid AVI frame filename")
		return
	}
	f, err := os.Open(rec.FilePath)
	if err != nil {
		WriteError(w, http.StatusNotFound, "AVI file not found")
		return
	}
	defer f.Close()
	dmx, err := avi.NewDemuxer(f)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "parse AVI: "+err.Error())
		return
	}
	entries, err := dmx.VideoFrameIndex()
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "index AVI frames: "+err.Error())
		return
	}
	if idx < 0 || idx >= len(entries) {
		WriteError(w, http.StatusNotFound, "AVI frame out of range")
		return
	}
	e := entries[idx]
	if _, err := f.Seek(e.Offset, io.SeekStart); err != nil {
		WriteError(w, http.StatusInternalServerError, "seek AVI frame: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Content-Length", strconv.FormatInt(int64(e.Size), 10))
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.WriteHeader(http.StatusOK)
	if _, err := io.CopyN(w, f, int64(e.Size)); err != nil {
		return // client went away
	}
}

// handleTimelapseFrame handles GET /api/recordings/{id}/timelapse-frames/{filename}.
// Serves an individual JPEG frame from a timelapse recording.
func (h *Handler) handleTimelapseFrame(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	filename := chi.URLParam(r, "filename")

	// Validate filename — only allow alphanumeric, underscore, dash, dot
	for _, c := range filename {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-' || c == '.') {
			WriteError(w, http.StatusBadRequest, "invalid filename")
			return
		}
	}
	if filename == "" || filename == "." || filename == ".." {
		WriteError(w, http.StatusBadRequest, "invalid filename")
		return
	}

	rec, err := h.db.GetRecording(r.Context(), id)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to get recording")
		return
	}
	if rec == nil {
		WriteError(w, http.StatusNotFound, "recording not found")
		return
	}
	if rec.Format == model.FormatAVI {
		h.handleAviFrame(w, r, rec, filename)
		return
	}
	if rec.Format != model.FormatTimelapse && rec.Format != model.FormatMJPEG {
		WriteError(w, http.StatusNotFound, "not a timelapse or MJPEG recording")
		return
	}

	filePath := filepath.Join(rec.FilePath, filename)
	http.ServeFile(w, r, filePath)
}

// handleMergedRecording handles GET /api/recordings/{id}/merged.
// Serves the merged MP4 file for a timelapse recording if it has been merged.
// Returns 404 if the merged MP4 is not available — the frontend falls back to
// the JPEG frame viewer on this 404 (via MEDIA_ERR_NETWORK in handleVideoError).
//
// Sets the X-Timelapse-Codec response header (h264/h265/mjpeg) so the frontend
// can decide whether to use <video> (H.264/H.265 are browser-playable) or fall
// back to the JPEG frame cycler (MJPEG-in-MP4 / mjpa is not). Probed via the
// pure-Go mediaprobe package — no ffprobe.
func (h *Handler) handleMergedRecording(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	rec, err := h.db.GetRecording(r.Context(), id)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to get recording")
		return
	}
	if rec == nil {
		WriteError(w, http.StatusNotFound, "recording not found")
		return
	}
	if rec.MergePath == "" {
		WriteError(w, http.StatusNotFound, "merged recording not available")
		return
	}
	// Verify the merged MP4 actually exists on disk
	if _, err := os.Stat(rec.MergePath); err != nil {
		logger.Warn("merged recording file missing on disk",
			"recording_id", id, "merge_path", rec.MergePath, "error", err)
		WriteError(w, http.StatusNotFound, "merged recording file not available")
		return
	}
	// Surface the codec so the frontend player can pick the right path.
	// HEAD requests also get the header (caller sets it before ServeFile).
	if codec := probeTimelapseCodecCached(rec.MergePath); codec != "" {
		w.Header().Set("X-Timelapse-Codec", codec)
	}
	http.ServeFile(w, r, rec.MergePath)
}

// timelapseCodecCache caches probe results by file path. Merge output files are
// immutable (never modified after creation), so the codec never changes — caching
// avoids re-parsing the MP4 box header on every playback request.
var timelapseCodecCache sync.Map // path → string (codec)

// probeTimelapseCodecCached returns the codec for a timelapse merge MP4, using
// an in-memory cache keyed by file path. Since merge outputs are write-once,
// the cache is permanent per path (no invalidation needed).
func probeTimelapseCodecCached(path string) string {
	if v, ok := timelapseCodecCache.Load(path); ok {
		return v.(string)
	}
	codec := probeTimelapseCodec(path)
	timelapseCodecCache.Store(path, codec)
	return codec
}

// probeTimelapseCodec returns "h264" / "h265" / "mjpeg" for the MP4 at the
// given path, or "" if the codec could not be determined. Used to populate the
// X-Timelapse-Codec response header that the frontend player consults.
func probeTimelapseCodec(path string) string {
	info, err := mediaprobe.ProbeMP4(path)
	if err != nil {
		return ""
	}
	switch info.Codec {
	case model.TimelapseMergeCodecH264, model.TimelapseMergeCodecH265:
		return info.Codec
	default:
		// mediaprobe returns raw codec string for mjpa; normalize to "mjpeg".
		return model.TimelapseMergeCodecMJPEG
	}
}

// extractFrameTimestamp extracts the capture timestamp from a frame file.
// Priority order:
// 1. Filename pattern: frame_YYYYMMDD_HHMMSS.jpg
// 2. JPEG EXIF DateTimeOriginal (or DateTime as fallback)
// 3. File ModTime (fallback)
func extractFrameTimestamp(name, filePath string, modTime time.Time) time.Time {
	// Try filename pattern first (no file I/O needed)
	if t, ok := parseFrameFilename(name); ok {
		return t
	}

	// Fall back to EXIF DateTimeOriginal
	if t, ok := extractEXIFDateTime(filePath); ok {
		return t
	}

	// Last resort: file ModTime
	return modTime
}

// parseFrameFilename attempts to parse a timestamp from a frame filename.
// Supported formats:
//   - frame_20240101_120000.jpg
//   - frame_20240101_120000_001.jpg
//   - frame_000001.jpg (no timestamp — returns false)
func parseFrameFilename(name string) (time.Time, bool) {
	// Must start with "frame_" prefix
	if !strings.HasPrefix(name, "frame_") {
		return time.Time{}, false
	}

	// Remove prefix
	rest := name[6:]

	// Expected pattern: 8 digits (date) + "_" + 6 digits (time) [+ optional suffix]
	if len(rest) < 15 {
		return time.Time{}, false
	}
	if rest[8] != '_' {
		return time.Time{}, false
	}

	dateStr := rest[:8]
	timeStr := rest[9:15]

	// Validate both are digits
	for _, c := range dateStr {
		if c < '0' || c > '9' {
			return time.Time{}, false
		}
	}
	for _, c := range timeStr {
		if c < '0' || c > '9' {
			return time.Time{}, false
		}
	}

	// Parse as YYYYMMDD_HHMMSS
	t, err := time.Parse("20060102_150405", dateStr+"_"+timeStr)
	if err != nil {
		return time.Time{}, false
	}

	return t, true
}

// extractEXIFDateTime extracts DateTimeOriginal (or DateTime) from a JPEG file's EXIF data.
// Returns the parsed time and true if successful.
func extractEXIFDateTime(filePath string) (time.Time, bool) {
	f, err := os.Open(filePath)
	if err != nil {
		return time.Time{}, false
	}
	defer f.Close()

	// Read enough for JPEG SOI + APP1 marker + EXIF header + TIFF structure
	// EXIF data is typically in the first few KB; read 64KB to be safe
	buf := make([]byte, 65536)
	n, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return time.Time{}, false
	}
	if n < 4 {
		return time.Time{}, false
	}
	data := buf[:n]

	// Check JPEG SOI marker (0xFF 0xD8)
	if data[0] != 0xFF || data[1] != 0xD8 {
		return time.Time{}, false
	}

	// Find APP1 segment (0xFF 0xE1)
	i := 2
	found := false
	for i < n-1 {
		if data[i] != 0xFF {
			return time.Time{}, false
		}
		marker := data[i+1]
		if i+3 >= n {
			return time.Time{}, false
		}
		segLen := int(binary.BigEndian.Uint16(data[i+2:i+4])) + 2
		if segLen < 2 || i+segLen > n {
			return time.Time{}, false
		}

		if marker == 0xE1 && segLen >= 10 {
			// Check for "Exif\0\0" header (6 bytes after the length field)
			if string(data[i+4:i+10]) == "Exif\x00\x00" {
				found = true
				// Parse EXIF starting after the "Exif\0\0" header (i+10)
				exifData := data[i+10 : i+segLen]
				if t, ok := parseEXIFTIFF(exifData); ok {
					return t, true
				}
			}
		}
		i += segLen
	}

	if !found {
		return time.Time{}, false
	}

	return time.Time{}, false
}

// parseEXIFTIFF parses a TIFF structure within EXIF data to find DateTimeOriginal or DateTime.
func parseEXIFTIFF(data []byte) (time.Time, bool) {
	if len(data) < 8 {
		return time.Time{}, false
	}

	// Determine byte order
	var bo binary.ByteOrder
	switch string(data[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return time.Time{}, false
	}

	// Verify TIFF magic (0x002A)
	if bo.Uint16(data[2:4]) != 0x002A {
		return time.Time{}, false
	}

	// Offset to IFD0 (from start of TIFF header)
	ifdOffset := int(bo.Uint32(data[4:8]))
	if ifdOffset < 8 || ifdOffset+2 > len(data) {
		return time.Time{}, false
	}

	// Number of IFD0 entries
	numEntries := int(bo.Uint16(data[ifdOffset : ifdOffset+2]))
	ifdEnd := ifdOffset + 2 + numEntries*12
	if ifdEnd > len(data) {
		return time.Time{}, false
	}

	var dateTimeStr string
	var exifIFDOffset int

	// Scan IFD0 entries
	for j := range numEntries {
		entryOff := ifdOffset + 2 + j*12
		if entryOff+12 > len(data) {
			break
		}
		tag := bo.Uint16(data[entryOff : entryOff+2])
		type_ := bo.Uint16(data[entryOff+2 : entryOff+4])
		_ = type_

		switch tag {
		case 0x0132: // DateTime in IFD0 (ASCII)
			// Value is an ASCII string stored in the value field (4 bytes) or as an offset
			if s := readEXIFString(data, entryOff, bo); s != "" {
				dateTimeStr = s
			}
		case 0x8769: // ExifIFD pointer
			if entryOff+12 > len(data) {
				break
			}
			exifIFDOffset = int(bo.Uint32(data[entryOff+8 : entryOff+12]))
			if exifIFDOffset < 0 || exifIFDOffset >= len(data) {
				exifIFDOffset = 0
			}
		}
	}

	// If we found DateTime in IFD0, use it (but prefer DateTimeOriginal from EXIF IFD)
	// Try EXIF IFD first for DateTimeOriginal
	if exifIFDOffset > 0 && exifIFDOffset+2 <= len(data) {
		numExifEntries := int(bo.Uint16(data[exifIFDOffset : exifIFDOffset+2]))
		for j := range numExifEntries {
			entryOff := exifIFDOffset + 2 + j*12
			if entryOff+12 > len(data) {
				break
			}
			tag := bo.Uint16(data[entryOff : entryOff+2])
			if tag == 0x9003 { // DateTimeOriginal
				if s := readEXIFString(data, entryOff, bo); s != "" {
					if t, err := time.Parse("2006:01:02 15:04:05", s); err == nil {
						return t, true
					}
				}
			}
		}
	}

	// Fall back to DateTime from IFD0
	if dateTimeStr != "" {
		if t, err := time.Parse("2006:01:02 15:04:05", dateTimeStr); err == nil {
			return t, true
		}
	}

	return time.Time{}, false
}

// readEXIFString reads an ASCII string value from an EXIF entry.
// For short strings (≤4 bytes), the value is stored inline in the 4-byte value field.
// For longer strings, the value field contains an offset to the string data.
func readEXIFString(data []byte, entryOff int, bo binary.ByteOrder) string {
	if entryOff+12 > len(data) {
		return ""
	}

	type_ := bo.Uint16(data[entryOff+2 : entryOff+4])
	count := bo.Uint32(data[entryOff+4 : entryOff+8])

	// Type 2 = ASCII string
	if type_ != 2 {
		return ""
	}

	// Calculate total byte count (ASCII strings have 1 byte per character)
	totalBytes := int(count)
	if totalBytes <= 0 || totalBytes > 256 {
		return ""
	}

	var strBytes []byte
	if totalBytes <= 4 {
		// Inline value
		strBytes = data[entryOff+8 : entryOff+8+totalBytes]
	} else {
		// Offset to string data
		offset := int(bo.Uint32(data[entryOff+8 : entryOff+12]))
		if offset < 0 || offset+totalBytes > len(data) {
			return ""
		}
		strBytes = data[offset : offset+totalBytes]
	}

	// Strip null terminator(s)
	s := string(bytes.TrimRight(strBytes, "\x00"))
	return strings.TrimSpace(s)
}

// handleTimelineGaps returns recording gaps (time periods with no recording)
// for a camera on a specific date. Used by the frontend timeline to render
// "断帧" (frame drop) markers.
//
// Query params:
//
//	date=YYYY-MM-DD  — the day to analyze (required)
//	min_gap=30s      — minimum gap duration to report (default 30s)
func (h *Handler) handleTimelineGaps(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		WriteError(w, http.StatusInternalServerError, "database not available")
		return
	}

	cameraID := chi.URLParam(r, "id")
	if cameraID == "" {
		WriteError(w, http.StatusBadRequest, "camera id is required")
		return
	}

	dateStr := r.URL.Query().Get("date")
	if dateStr == "" {
		WriteError(w, http.StatusBadRequest, "date query param is required (YYYY-MM-DD)")
		return
	}

	minGapStr := r.URL.Query().Get("min_gap")
	if minGapStr == "" {
		minGapStr = "30s"
	}
	minGap, err := time.ParseDuration(minGapStr)
	if err != nil || minGap <= 0 {
		minGap = 30 * time.Second
	}

	// Parse date to UTC start/end.
	y, m, d, err := parseDateParts(dateStr)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "invalid date format, use YYYY-MM-DD")
		return
	}
	dayStart := time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC)
	dayEnd := dayStart.Add(24 * time.Hour)

	// Fetch all recordings for this camera on this day.
	recs, err := h.db.ListRecordings(r.Context(), model.RecordingFilter{
		CameraID:  cameraID,
		StartTime: dayStart,
		EndTime:   dayEnd,
		SortBy:    "started_at",
		SortOrder: "asc",
		Limit:     1000,
	})
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to list recordings: "+err.Error())
		return
	}

	// Compute gaps between consecutive recordings.
	type Gap struct {
		Start    string  `json:"start"`
		End      string  `json:"end"`
		Duration float64 `json:"duration"`
	}
	var gaps []Gap
	for i := 1; i < len(recs); i++ {
		prevEnd := recs[i-1].EndedAt
		currStart := recs[i].StartedAt
		if prevEnd.IsZero() || currStart.IsZero() {
			continue
		}
		gapDur := currStart.Sub(prevEnd).Seconds()
		if gapDur >= minGap.Seconds() {
			gaps = append(gaps, Gap{
				Start:    prevEnd.Format(time.RFC3339Nano),
				End:      currStart.Format(time.RFC3339Nano),
				Duration: math.Round(gapDur*10) / 10,
			})
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"camera_id":  cameraID,
		"date":       dateStr,
		"gaps":       gaps,
		"total_gaps": len(gaps),
	})
}

// parseDateParts parses a "YYYY-MM-DD" string into year, month, day integers.
func parseDateParts(s string) (year, month, day int, err error) {
	parts := strings.Split(s, "-")
	if len(parts) != 3 {
		return 0, 0, 0, fmt.Errorf("expected YYYY-MM-DD")
	}
	year, err1 := strconv.Atoi(parts[0])
	month, err2 := strconv.Atoi(parts[1])
	day, err3 := strconv.Atoi(parts[2])
	if err1 != nil || err2 != nil || err3 != nil {
		return 0, 0, 0, fmt.Errorf("invalid date components")
	}
	return year, month, day, nil
}

// registerRecordingRoutes registers all /api/recordings* routes on the given
// (already auth-protected) router.
func (h *Handler) registerRecordingRoutes(r chi.Router) {
	r.Route("/api/recordings", func(r chi.Router) {
		r.Get("/", h.handleListRecordings)
		r.Get("/daily-summary", h.handleDailyRecordingSummary)
		r.Get("/timeline", h.handleTimelineSegments)
		r.Post("/", h.handleCreateRecording)
		r.Post("/timeline/seek-event", h.handleTimelineSeekEvent)
		r.Post("/batch-delete", h.handleBatchDeleteRecordings)
		r.Route("/{id}", func(r chi.Router) {
			r.Get("/", h.handleGetRecording)
			r.Delete("/", h.handleDeleteRecording)
			r.Patch("/", h.handleUpdateRecording)
			r.Patch("/ai-status", h.handleUpdateRecordingAIStatus)
			r.Get("/frames", h.handleListFrames)
			r.Get("/playback", h.handlePlayback)
			r.Get("/timelapse-frames", h.handleTimelapseFrames)
			r.Get("/timelapse-frames/{filename}", h.handleTimelapseFrame)
			r.Post("/retry-merge", h.handleRetryTimelapseMerge)
		})
	})
	// Recording gaps for timeline (per-camera, registered here to keep recording-
	// related routes together)
	r.Get("/api/cameras/{id}/timeline/gaps", h.handleTimelineGaps)
}
