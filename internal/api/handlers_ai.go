package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/middleware"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/go-chi/chi/v5"
)

// apiMetricsPtr holds the optional metrics instance for AI event tracking.
// Atomic: tests swap it via SetAPIMetrics from a parallel test's cleanup while
// other parallel tests are still serving requests that read it — a plain
// pointer write raced those readers under -race.
var apiMetricsPtr atomic.Pointer[metrics.Metrics]

// SetAPIMetrics injects the Prometheus metrics instance into the API package.
func SetAPIMetrics(m *metrics.Metrics) {
	apiMetricsPtr.Store(m)
}

// currentAPIMetrics snapshots the metrics instance for a single request's
// instrumentation — readers must not touch the global repeatedly (each Load
// may observe a different instance mid-test).
func currentAPIMetrics() *metrics.Metrics {
	return apiMetricsPtr.Load()
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

	// Sub-layer pushes (#514) identify as "<mainRecordingID>#<subStartNano>"
	// so the consumer can dedup every sub segment; events reported against
	// them map back onto the main recording row.
	if i := strings.IndexByte(body.RecordingID, '#'); i >= 0 {
		body.RecordingID = body.RecordingID[:i]
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
		// 写入方实例归因(多 Vision 接入):API Key 名落库,列表按 source 过滤。
		Source: middleware.APIKeyNameFromContext(r.Context()),
	}

	id, err := h.db.InsertAIEvent(r.Context(), aiEvent)
	if err != nil {
		if m := currentAPIMetrics(); m != nil {
			m.AIEventsErrorsTotal.Inc()
		}
		logger.Error("failed to store AI event", "error", err, "path", r.URL.Path)
		WriteError(w, http.StatusInternalServerError, "failed to store AI event")
		return
	}

	if m := currentAPIMetrics(); m != nil {
		m.AIEventsReceivedTotal.WithLabelValues(body.CameraID, body.EventType).Inc()
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
		Source:    r.URL.Query().Get("source"),
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

// handleGetAIEventSnapshot serves the event's snapshot image
// (GET /api/ai/events/{id}/snapshot). Sidecar MiBeeVision deployments write
// JPEGs under <storage root>/ai-snapshots/ and report the storage-root-
// relative path in snapshot_path; remote deployments leave it empty. Every
// "no image available" case (empty path, missing file, invalid/escaping
// path, unknown event) is a plain 404 so clients can fall back to a
// placeholder without parsing the error body.
func (h *Handler) handleGetAIEventSnapshot(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		WriteError(w, http.StatusInternalServerError, "database not available")
		return
	}

	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
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
	if evt == nil || evt.SnapshotPath == "" {
		WriteError(w, http.StatusNotFound, "snapshot not available")
		return
	}
	if h.config == nil {
		WriteError(w, http.StatusInternalServerError, "storage not configured")
		return
	}

	path, ok := resolveWithinStorageRoot(h.config.Storage.RootDir, evt.SnapshotPath)
	if !ok {
		// Vision-supplied path resolving outside the storage root is invalid
		// — never serve files from outside the configured tree.
		WriteError(w, http.StatusNotFound, "snapshot not available")
		return
	}
	if _, err := os.Stat(path); err != nil {
		WriteError(w, http.StatusNotFound, "snapshot not available")
		return
	}

	// Snapshots are immutable once written; list views fetch them in bursts,
	// so let clients cache for a day (ServeFile still honors range requests
	// and conditional GETs via Last-Modified).
	w.Header().Set("Cache-Control", "private, max-age=86400")
	h.serveFileBudgeted(w, r, path)
}

// maxAIEventSnapshotBytes caps a single snapshot upload (sanity guard
// against misbehaving clients; a 1080p event JPEG is well under 1MB).
const maxAIEventSnapshotBytes = 4 << 20

// handleUploadAIEventSnapshot accepts the event's snapshot JPEG bytes from
// an external AI backend (POST /api/ai/events/{id}/snapshot, API-key auth).
// Remote deployments cannot write the NVR storage tree directly, so they
// upload bytes here; the handler persists the file under
// <storage root>/ai-snapshots/ and backfills the event's snapshot_path.
func (h *Handler) handleUploadAIEventSnapshot(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		WriteError(w, http.StatusInternalServerError, "database not available")
		return
	}
	if !middleware.IsAPIKeyAuthenticated(r.Context()) {
		WriteError(w, http.StatusUnauthorized, "API key required for snapshot upload")
		return
	}

	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
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
	if h.config == nil {
		WriteError(w, http.StatusInternalServerError, "storage not configured")
		return
	}

	data, err := io.ReadAll(io.LimitReader(r.Body, maxAIEventSnapshotBytes+1))
	if err != nil {
		WriteError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	if len(data) > maxAIEventSnapshotBytes {
		WriteError(w, http.StatusRequestEntityTooLarge, "snapshot too large")
		return
	}
	// JPEG 魔数嗅探（FF D8）：挡住误发的 JSON/文本体。
	if len(data) < 2 || data[0] != 0xFF || data[1] != 0xD8 {
		WriteError(w, http.StatusBadRequest, "invalid JPEG data")
		return
	}

	rel := filepath.ToSlash(filepath.Join("ai-snapshots",
		fmt.Sprintf("%s_%d.jpg", sanitizeFileToken(evt.CameraID), id)))
	root := h.config.Storage.RootDir
	dir := filepath.Join(root, "ai-snapshots")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		logger.Error("failed to create snapshot dir", "error", err)
		WriteError(w, http.StatusInternalServerError, "failed to store snapshot")
		return
	}
	target := filepath.Join(root, filepath.FromSlash(rel))
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		logger.Error("failed to write snapshot", "error", err)
		WriteError(w, http.StatusInternalServerError, "failed to store snapshot")
		return
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		logger.Error("failed to finalize snapshot", "error", err)
		WriteError(w, http.StatusInternalServerError, "failed to store snapshot")
		return
	}

	if err := h.db.UpdateAIEventSnapshotPath(r.Context(), id, rel); err != nil {
		logger.Error("failed to backfill snapshot path", "error", err, "id", id)
		WriteError(w, http.StatusInternalServerError, "failed to record snapshot path")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"id":            id,
		"snapshot_path": rel,
		"bytes":         len(data),
	})
}

// sanitizeFileToken keeps a free-form camera id usable as a filename chunk.
func sanitizeFileToken(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	out := b.String()
	if out == "" {
		return "camera"
	}
	return out
}

// resolveWithinStorageRoot resolves relOrAbs under root, accepting both
// storage-root-relative paths (the documented contract) and absolute paths
// that already point inside the root (lenient for same-host integrators).
// It returns false when the resolved path leaves the root.
func resolveWithinStorageRoot(root, relOrAbs string) (string, bool) {
	cleanRoot := filepath.Clean(root)
	var p string
	if filepath.IsAbs(relOrAbs) {
		p = filepath.Clean(relOrAbs)
	} else {
		p = filepath.Clean(filepath.Join(cleanRoot, relOrAbs))
	}
	if p != cleanRoot && !strings.HasPrefix(p, cleanRoot+string(filepath.Separator)) {
		return "", false
	}
	return p, true
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
	r.Get("/api/ai/events/{id}/snapshot", h.handleGetAIEventSnapshot)
	// 上传与读取同一资源路径（POST 写、GET 读）：远程部署的 Vision 推字节。
	r.Post("/api/ai/events/{id}/snapshot", h.handleUploadAIEventSnapshot)
	r.Get("/api/ai/stats", h.handleGetAIEventStats)
}
