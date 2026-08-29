package api

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/middleware"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/go-chi/chi/v5"
)

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

	// Archived toggle: the frontend archive view sends ?archived=true to list
	// recordings of archived cameras (the DB layer and count-cache key already
	// support it — handleDailyRecordingSummary parses it too). Without this
	// the param was silently dropped and archived recordings were unreachable.
	if v := r.URL.Query().Get("archived"); v != "" {
		archived := v == "true" || v == "1"
		filter.Archived = &archived
	}

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
	if m := currentAPIMetrics(); m != nil {
		m.TimelineSeeksTotal.WithLabelValues(cameraID, seekType).Inc()
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
