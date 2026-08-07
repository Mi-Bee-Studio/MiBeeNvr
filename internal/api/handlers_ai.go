package api

import (
	"encoding/json"
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
		WriteError(w, http.StatusInternalServerError, "database not available")
		return
	}

	// Verify API Key authentication
	if !middleware.IsAPIKeyAuthenticated(r.Context()) {
		WriteError(w, http.StatusUnauthorized, "API key required for AI event submission")
		return
	}

	var body struct {
		CameraID       string          `json:"camera_id"`
		RecordingID    string          `json:"recording_id"`
		EventType      string          `json:"event_type"`
		Severity       string          `json:"severity"`
		ZoneName       string          `json:"zone_name"`
		ClassName      string          `json:"class_name"`
		Confidence     float64         `json:"confidence"`
		FrameIdx       int             `json:"frame_idx"`
		FrameTimestamp string          `json:"frame_timestamp"`
		BBox           []float64       `json:"bbox"`
		SnapshotPath   string          `json:"snapshot_path"`
		Metadata       json.RawMessage `json:"metadata"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if body.CameraID == "" || body.EventType == "" {
		WriteError(w, http.StatusBadRequest, "camera_id and event_type are required")
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
		logger.Error("failed to store AI event", "error", err, "path", r.URL.Path)
		WriteError(w, http.StatusInternalServerError, "failed to store AI event")
		return
	}

	// Record metrics
	if apiMetrics != nil {
		apiMetrics.AIEventsReceivedTotal.WithLabelValues(body.CameraID, body.EventType).Inc()
	}

	// Publish ai.event.created event for SSE subscribers
	if h.eventBus != nil {
		h.eventBus.Publish(r.Context(), event.TopicAIEventCreated, map[string]interface{}{
			"event_id":   id,
			"camera_id":  body.CameraID,
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
		WriteError(w, http.StatusInternalServerError, "database not available")
		return
	}

	f := storage.AIEventFilter{
		CameraID:  r.URL.Query().Get("camera_id"),
		EventType: r.URL.Query().Get("event_type"),
	}
	f.Limit, f.Offset = parsePagination(r, 0, 0) // no default/cap; ListAIEvents clamps to 50 internally
	// Time-range filtering for timeline overlay support.
	if startStr := r.URL.Query().Get("start"); startStr != "" {
		if t, err := time.Parse(time.RFC3339Nano, startStr); err == nil {
			f.StartTime = &t
		}
	}
	if endStr := r.URL.Query().Get("end"); endStr != "" {
		if t, err := time.Parse(time.RFC3339Nano, endStr); err == nil {
			f.EndTime = &t
		}
	}
	if r.URL.Query().Get("asc") == "true" {
		f.AscOrder = true
	}

	events, total, err := h.db.ListAIEvents(r.Context(), f)
	if err != nil {
		logger.Error("failed to list AI events", "error", err, "path", r.URL.Path)
		WriteError(w, http.StatusInternalServerError, "failed to list AI events")
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
		WriteError(w, http.StatusInternalServerError, "database not available")
		return
	}

	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "invalid event ID")
		return
	}

	evt, err := h.db.GetAIEvent(r.Context(), id)
	if err != nil {
		logger.Error("failed to get AI event", "error", err, "path", r.URL.Path)
		WriteError(w, http.StatusInternalServerError, "failed to get AI event")
		return
	}
	if evt == nil {
		WriteError(w, http.StatusNotFound, "AI event not found")
		return
	}

	writeJSON(w, http.StatusOK, evt)
}

// handleGetAIEventStats returns aggregated statistics (GET /api/ai/stats).
func (h *Handler) handleGetAIEventStats(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		WriteError(w, http.StatusInternalServerError, "database not available")
		return
	}

	// camera_id is optional: when omitted, stats aggregate across ALL cameras
	// (global view). When present, stats are scoped to that camera (#213).
	cameraID := r.URL.Query().Get("camera_id")

	period := r.URL.Query().Get("period")
	since := getDefaultStatsSince(period)

	stats, err := h.db.GetAIEventStats(r.Context(), cameraID, since)
	if err != nil {
		logger.Error("failed to get AI stats", "error", err, "path", r.URL.Path)
		WriteError(w, http.StatusInternalServerError, "failed to get AI stats")
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

// registerAIRoutes registers AI config/status/zones and MiBeeVision event routes.
func (h *Handler) registerAIRoutes(r chi.Router) {
	r.Get("/api/ai/status", h.aiHandler.handleAIStatus)
	r.Put("/api/ai/config", h.aiHandler.handleAIUpdateConfig)
	r.Get("/api/ai/models", h.aiHandler.handleAIModels)
	r.Get("/api/ai/zones", h.aiHandler.handleAIZones)
	r.Post("/api/ai/zones", h.aiHandler.handleAICreateZone)
	r.Put("/api/ai/zones/{id}", h.aiHandler.handleAIUpdateZone)
	r.Delete("/api/ai/zones/{id}", h.aiHandler.handleAIDeleteZone)
	// AI event endpoints (MiBeeVision collaboration)
	// POST /api/ai/events requires API Key auth (checked inside handler)
	r.Post("/api/ai/events", h.handleCreateAIEvent)
	// GET endpoints are user-authenticated (behind the group's authMW)
	r.Get("/api/ai/events", h.handleListAIEvents)
	r.Get("/api/ai/events/{id}", h.handleGetAIEvent)
	r.Get("/api/ai/stats", h.handleGetAIEventStats)
}
