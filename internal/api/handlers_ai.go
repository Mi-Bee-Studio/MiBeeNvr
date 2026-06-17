package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/middleware"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/go-chi/chi/v5"
)

// apiMetrics holds the optional metrics instance for AI event tracking.
var apiMetrics *metrics.Metrics

// SetAPIMetrics injects the Prometheus metrics instance into the API package.
func SetAPIMetrics(m *metrics.Metrics) {
	apiMetrics = m
}

// handleCreateAIEvent accepts AI detection events from MiBeeVision (POST /api/ai/events).
// Requires API Key authentication (Authorization: Bearer mbv_*).
func (h *Handler) handleCreateAIEvent(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusInternalServerError, "database not available")
		return
	}

	// Verify API Key authentication
	if !middleware.IsAPIKeyAuthenticated(r.Context()) {
		writeError(w, http.StatusUnauthorized, "API key required for AI event submission")
		return
	}

	var body struct {
		CameraID       string  `json:"camera_id"`
		RecordingID    string  `json:"recording_id"`
		EventType      string  `json:"event_type"`
		Severity       string  `json:"severity"`
		ZoneName       string  `json:"zone_name"`
		ClassName      string  `json:"class_name"`
		Confidence     float64 `json:"confidence"`
		FrameIdx       int     `json:"frame_idx"`
		FrameTimestamp string  `json:"frame_timestamp"`
		BBox           []float64 `json:"bbox"`
		SnapshotPath   string  `json:"snapshot_path"`
		Metadata       json.RawMessage `json:"metadata"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if body.CameraID == "" || body.EventType == "" {
		writeError(w, http.StatusBadRequest, "camera_id and event_type are required")
		return
	}

	severity := body.Severity
	if severity == "" {
		severity = "info"
	}

	// Convert bbox to JSON string for storage
	var bboxStr string
	if len(body.BBox) == 4 {
		bboxStr = storage.MarshalBBox([4]float64{body.BBox[0], body.BBox[1], body.BBox[2], body.BBox[3]})
	}

	var metadataStr string
	if len(body.Metadata) > 0 && string(body.Metadata) != "null" {
		metadataStr = string(body.Metadata)
	}

	aiEvent := &storage.AIEvent{
		CameraID:       body.CameraID,
		RecordingID:    body.RecordingID,
		EventType:      body.EventType,
		Severity:       severity,
		ZoneName:       body.ZoneName,
		ClassName:      body.ClassName,
		Confidence:     body.Confidence,
		FrameIdx:       body.FrameIdx,
		FrameTimestamp: body.FrameTimestamp,
		BBox:           bboxStr,
		SnapshotPath:   body.SnapshotPath,
		Metadata:       metadataStr,
	}

	id, err := h.db.InsertAIEvent(r.Context(), aiEvent)
	if err != nil {
		if apiMetrics != nil {
			apiMetrics.AIEventsErrorsTotal.Inc()
		}
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to store AI event: %v", err))
		return
	}

	// Record metrics
	if apiMetrics != nil {
		apiMetrics.AIEventsReceivedTotal.WithLabelValues(body.CameraID, body.EventType).Inc()
	}

	// Publish ai.event.created event for SSE subscribers
	if h.eventBus != nil {
		h.eventBus.Publish(r.Context(), event.TopicAIEventCreated, map[string]interface{}{
			"event_id":  id,
			"camera_id": body.CameraID,
			"event_type": body.EventType,
			"severity":   severity,
		})
	}

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"id":     id,
		"status": "stored",
	})
}

// handleListAIEvents returns AI events with optional filtering (GET /api/ai/events).
func (h *Handler) handleListAIEvents(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusInternalServerError, "database not available")
		return
	}

	f := storage.AIEventFilter{
		CameraID:  r.URL.Query().Get("camera_id"),
		EventType: r.URL.Query().Get("event_type"),
	}
	if lim := r.URL.Query().Get("limit"); lim != "" {
		if v, err := strconv.Atoi(lim); err == nil && v > 0 {
			f.Limit = v
		}
	}
	if off := r.URL.Query().Get("offset"); off != "" {
		if v, err := strconv.Atoi(off); err == nil && v >= 0 {
			f.Offset = v
		}
	}

	events, total, err := h.db.ListAIEvents(r.Context(), f)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to list AI events: %v", err))
		return
	}
	if events == nil {
		events = []storage.AIEvent{}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"events": events,
		"total":  total,
		"limit":  f.Limit,
		"offset": f.Offset,
	})
}

// handleGetAIEvent returns a single AI event by ID (GET /api/ai/events/{id}).
func (h *Handler) handleGetAIEvent(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusInternalServerError, "database not available")
		return
	}

	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid event ID")
		return
	}

	evt, err := h.db.GetAIEvent(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to get AI event: %v", err))
		return
	}
	if evt == nil {
		writeError(w, http.StatusNotFound, "AI event not found")
		return
	}

	writeJSON(w, http.StatusOK, evt)
}

// handleGetAIEventStats returns aggregated statistics (GET /api/ai/stats).
func (h *Handler) handleGetAIEventStats(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusInternalServerError, "database not available")
		return
	}

	cameraID := r.URL.Query().Get("camera_id")
	if cameraID == "" {
		writeError(w, http.StatusBadRequest, "camera_id is required")
		return
	}

	period := r.URL.Query().Get("period")
	since := getDefaultStatsSince(period)

	stats, err := h.db.GetAIEventStats(r.Context(), cameraID, since)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to get AI stats: %v", err))
		return
	}
	if stats == nil {
		stats = []storage.AIEventStats{}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"camera_id": cameraID,
		"period":    period,
		"stats":     stats,
	})
}

// getDefaultStatsSince returns a time.Time for common period strings.
func getDefaultStatsSince(period string) (t time.Time) {
	now := time.Now()
	switch period {
	case "1h":
		return now.Add(-1 * time.Hour)
	case "24h", "":
		return now.Add(-24 * time.Hour)
	case "7d":
		return now.Add(-7 * 24 * time.Hour)
	case "30d":
		return now.Add(-30 * 24 * time.Hour)
	default:
		return now.Add(-24 * time.Hour)
	}
}

// handleUpdateRecordingAIStatus updates the AI processing status of a recording
// (PATCH /api/recordings/{id}/ai-status). Used by MiBeeVision to report
// processing progress and prevent duplicate processing.
func (h *Handler) handleUpdateRecordingAIStatus(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusInternalServerError, "database not available")
		return
	}

	// API Key required
	if !middleware.IsAPIKeyAuthenticated(r.Context()) {
		writeError(w, http.StatusUnauthorized, "API key required")
		return
	}

	recID := chi.URLParam(r, "id")
	if recID == "" {
		writeError(w, http.StatusBadRequest, "recording id is required")
		return
	}

	var body struct {
		Status string `json:"status"`
		Error  string `json:"error"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	validStatuses := map[string]bool{
		"pending": true, "processing": true, "done": true, "failed": true, "skipped": true,
	}
	if !validStatuses[body.Status] {
		writeError(w, http.StatusBadRequest, "status must be one of: pending, processing, done, failed, skipped")
		return
	}

	if err := h.db.UpdateRecordingAIStatus(r.Context(), recID, body.Status, body.Error); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to update AI status: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"recording_id": recID,
		"ai_status":    body.Status,
	})
}
