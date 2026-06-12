package api

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/ai"
	"github.com/go-chi/chi/v5"
)

const (
	maxDetectionHistory   = 100
	defaultDetectionsLimit = 50
)

// AIHandler holds dependencies for AI REST API endpoints.
type AIHandler struct {
	manager   *ai.Manager
	historyMu sync.Mutex
	history   map[string][]ai.DetectionEvent // cameraID → recent events (max 100)
}

// NewAIHandler creates a new AIHandler.
func NewAIHandler(mgr *ai.Manager) *AIHandler {
	return &AIHandler{
		manager: mgr,
		history: make(map[string][]ai.DetectionEvent),
	}
}

// AddDetection appends a detection event to the in-memory ring buffer,
// trimming to at most maxDetectionHistory (100) per camera.
// This is called by the detector or an EventBus subscriber.
func (h *AIHandler) AddDetection(cameraID string, evt ai.DetectionEvent) {
	h.historyMu.Lock()
	defer h.historyMu.Unlock()

	h.history[cameraID] = append(h.history[cameraID], evt)
	if len(h.history[cameraID]) > maxDetectionHistory {
		h.history[cameraID] = h.history[cameraID][len(h.history[cameraID])-maxDetectionHistory:]
	}
}

// getDetections returns the most recent detections for a camera,
// sorted by timestamp descending (newest first).
func (h *AIHandler) getDetections(cameraID string, limit int) []ai.DetectionEvent {
	h.historyMu.Lock()
	defer h.historyMu.Unlock()

	events := h.history[cameraID]
	if len(events) == 0 {
		return nil
	}

	sorted := make([]ai.DetectionEvent, len(events))
	copy(sorted, events)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Timestamp.After(sorted[j].Timestamp)
	})

	if limit <= 0 || limit > len(sorted) {
		limit = len(sorted)
	}
	return sorted[:limit]
}

// getAllDetections returns the most recent detections across all cameras,
// sorted by timestamp descending (newest first).
func (h *AIHandler) getAllDetections(limit int) []ai.DetectionEvent {
	h.historyMu.Lock()
	defer h.historyMu.Unlock()

	all := make([]ai.DetectionEvent, 0)
	for _, events := range h.history {
		all = append(all, events...)
	}
	if len(all) == 0 {
		return nil
	}

	sort.Slice(all, func(i, j int) bool {
		return all[i].Timestamp.After(all[j].Timestamp)
	})

	if limit <= 0 || limit > len(all) {
		limit = len(all)
	}
	return all[:limit]
}

// --- Status endpoints ---

// handleAIStatus handles GET /api/ai/status.
// Returns global AI status: enabled, model info, active camera count, per-camera status.
func (h *AIHandler) handleAIStatus(w http.ResponseWriter, r *http.Request) {
	statuses := h.manager.Status()
	cfg := h.manager.GetConfig()

	var activeCameras int
	cameraStatuses := make(map[string]ai.CameraAIStatus, len(statuses))
	for id, s := range statuses {
		cameraStatuses[id] = s
		if s.Running {
			activeCameras++
		}
	}

	resp := map[string]any{
		"enabled":              cfg.Enabled,
		"ncnn_available":       h.manager.IsNCNNAvailable(),
		"model_name":           cfg.ModelPath,
		"active_cameras":       activeCameras,
		"cameras":              cameraStatuses,
		"confidence_threshold": cfg.ConfidenceThreshold,
		"frame_skip_rate":      cfg.FrameSkipRate,
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleAICameraAIStatus handles GET /api/ai/status/{cameraID}.
// Returns per-camera AI status.
func (h *AIHandler) handleAICameraAIStatus(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "cameraID")

	statuses := h.manager.Status()
	s, ok := statuses[cameraID]
	if !ok {
		writeError(w, http.StatusNotFound, "camera not found in AI status")
		return
	}
	writeJSON(w, http.StatusOK, s)
}

// --- Detection endpoints ---

// handleAIDetections handles GET /api/ai/detections.
// Query params: cameraID (optional filter), class (optional filter),
// since (RFC3339 timestamp), limit (default 50).
// Returns paginated detection events with total count.
func (h *AIHandler) handleAIDetections(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	cameraID := q.Get("cameraID")
	classFilter := q.Get("class")
	sinceStr := q.Get("since")
	limitStr := q.Get("limit")

	limit := defaultDetectionsLimit
	if limitStr != "" {
		if n, err := strconv.Atoi(limitStr); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > maxDetectionHistory {
		limit = maxDetectionHistory
	}

	var events []ai.DetectionEvent
	if cameraID != "" {
		events = h.getDetections(cameraID, maxDetectionHistory)
	} else {
		events = h.getAllDetections(maxDetectionHistory)
	}

	// Parse since filter
	var since time.Time
	if sinceStr != "" {
		if t, err := time.Parse(time.RFC3339, sinceStr); err == nil {
			since = t
		}
	}

	// Apply filters
	filtered := make([]ai.DetectionEvent, 0, len(events))
	for _, evt := range events {
		if !since.IsZero() && evt.Timestamp.Before(since) {
			continue
		}
		if classFilter != "" && !detectionHasClass(evt, classFilter) {
			continue
		}
		filtered = append(filtered, evt)
	}

	// Re-sort after filtering (should already be sorted, but safe)
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].Timestamp.After(filtered[j].Timestamp)
	})

	if len(filtered) > limit {
		filtered = filtered[:limit]
	}

	if filtered == nil {
		filtered = []ai.DetectionEvent{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"detections": filtered,
		"total":      len(filtered),
	})
}

// handleAICameraDetections handles GET /api/ai/detections/{cameraID}.
// Returns detections for a specific camera with optional filters.
func (h *AIHandler) handleAICameraDetections(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "cameraID")

	q := r.URL.Query()
	classFilter := q.Get("class")
	sinceStr := q.Get("since")
	limitStr := q.Get("limit")

	limit := defaultDetectionsLimit
	if limitStr != "" {
		if n, err := strconv.Atoi(limitStr); err == nil && n > 0 {
			limit = n
		}
	}

	events := h.getDetections(cameraID, maxDetectionHistory)

	var since time.Time
	if sinceStr != "" {
		if t, err := time.Parse(time.RFC3339, sinceStr); err == nil {
			since = t
		}
	}

	filtered := make([]ai.DetectionEvent, 0, len(events))
	for _, evt := range events {
		if !since.IsZero() && evt.Timestamp.Before(since) {
			continue
		}
		if classFilter != "" && !detectionHasClass(evt, classFilter) {
			continue
		}
		filtered = append(filtered, evt)
	}

	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].Timestamp.After(filtered[j].Timestamp)
	})

	if len(filtered) > limit {
		filtered = filtered[:limit]
	}

	if filtered == nil {
		filtered = []ai.DetectionEvent{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"detections": filtered,
		"total":      len(filtered),
	})
}

// --- Restart endpoint ---

// handleAIRestartCamera handles POST /api/ai/restart/{cameraID}.
// Restarts AI inference for a camera.
func (h *AIHandler) handleAIRestartCamera(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "cameraID")

	if err := h.manager.RestartCamera(r.Context(), cameraID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"status":    "restarted",
		"camera_id": cameraID,
	})
}

// --- Config update ---

// handleAIUpdateConfig handles PUT /api/ai/config.
// Updates global AI config at runtime (enabled, threshold, frame skip, etc.).
func (h *AIHandler) handleAIUpdateConfig(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled             *bool    `json:"enabled,omitempty"`
		ConfidenceThreshold *float64 `json:"confidence_threshold,omitempty"`
		FrameSkipRate       *int     `json:"frame_skip_rate,omitempty"`
		ModelPath           *string  `json:"model_path,omitempty"`
		ModelURL            *string  `json:"model_url,omitempty"`
		MaxGoroutines       *int     `json:"max_goroutines,omitempty"`
		InferenceTimeoutMs  *int     `json:"inference_timeout_ms,omitempty"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	cfg := h.manager.GetConfig()

	if body.Enabled != nil {
		cfg.Enabled = *body.Enabled
	}
	if body.ConfidenceThreshold != nil {
		cfg.ConfidenceThreshold = *body.ConfidenceThreshold
	}
	if body.FrameSkipRate != nil {
		cfg.FrameSkipRate = *body.FrameSkipRate
	}
	if body.ModelPath != nil {
		cfg.ModelPath = *body.ModelPath
	}
	if body.ModelURL != nil {
		cfg.ModelURL = *body.ModelURL
	}
	if body.MaxGoroutines != nil {
		cfg.MaxGoroutines = *body.MaxGoroutines
	}
	if body.InferenceTimeoutMs != nil {
		cfg.InferenceTimeoutMs = *body.InferenceTimeoutMs
	}

	h.manager.UpdateConfig(cfg)

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// --- Zone CRUD endpoints ---

type zoneRequestBody struct {
	CameraID string           `json:"camera_id"`
	Zone     zoneBodyZone     `json:"zone"`
	Enabled  bool             `json:"enabled"`
}

type zoneBodyZone struct {
	Name   string          `json:"name"`
	Points [][2]float64    `json:"points"`
}

// handleAIZones handles GET /api/ai/zones.
// Lists all ROI zones across all cameras.
func (h *AIHandler) handleAIZones(w http.ResponseWriter, r *http.Request) {
	cfg := h.manager.GetConfig()

	zones := make([]ai.ROIZone, 0)
	for cameraID, rois := range cfg.Zones {
		for _, roi := range rois {
			zones = append(zones, ai.ROIZone{
				CameraID: cameraID,
				Zone:     roi,
				Enabled:  true,
			})
		}
	}

	if zones == nil {
		zones = []ai.ROIZone{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"zones": zones,
	})
}

// handleAICreateZone handles POST /api/ai/zones.
// Creates a new ROI zone.
func (h *AIHandler) handleAICreateZone(w http.ResponseWriter, r *http.Request) {
	var body zoneRequestBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if body.CameraID == "" || body.Zone.Name == "" {
		writeError(w, http.StatusBadRequest, "camera_id and zone.name are required")
		return
	}

	cfg := h.manager.GetConfig()

	if cfg.Zones == nil {
		cfg.Zones = make(map[string][]ai.ROI)
	}

	// Check for duplicate zone name across all cameras
	for _, rois := range cfg.Zones {
		for _, roi := range rois {
			if roi.Name == body.Zone.Name {
				writeError(w, http.StatusConflict, "zone with this name already exists")
				return
			}
		}
	}

	roi := ai.ROI{
		Name:   body.Zone.Name,
		Points: body.Zone.Points,
	}

	cfg.Zones[body.CameraID] = append(cfg.Zones[body.CameraID], roi)
	h.manager.UpdateConfig(cfg)

	created := ai.ROIZone{
		CameraID: body.CameraID,
		Zone:     roi,
		Enabled:  body.Enabled,
	}

	writeJSON(w, http.StatusCreated, created)
}

// handleAIUpdateZone handles PUT /api/ai/zones/{id}.
// Updates an existing ROI zone identified by its name.
func (h *AIHandler) handleAIUpdateZone(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "zone id is required")
		return
	}

	var body zoneRequestBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	cfg := h.manager.GetConfig()

	found := false
	for cameraID, rois := range cfg.Zones {
		for i, roi := range rois {
			if roi.Name == id {
				if body.Zone.Name != "" && body.Zone.Name != id {
					// Check that the new name doesn't conflict
					for _, otherROIs := range cfg.Zones {
						for _, other := range otherROIs {
							if other.Name == body.Zone.Name {
								writeError(w, http.StatusConflict, "zone with new name already exists")
								return
							}
						}
					}
					cfg.Zones[cameraID][i].Name = body.Zone.Name
				}
				if len(body.Zone.Points) > 0 {
					cfg.Zones[cameraID][i].Points = body.Zone.Points
				}
				found = true
				break
			}
		}
		if found {
			break
		}
	}

	if !found {
		writeError(w, http.StatusNotFound, "zone not found")
		return
	}

	h.manager.UpdateConfig(cfg)

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// handleAIDeleteZone handles DELETE /api/ai/zones/{id}.
// Deletes an ROI zone identified by its name.
func (h *AIHandler) handleAIDeleteZone(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "zone id is required")
		return
	}

	cfg := h.manager.GetConfig()

	found := false
	for cameraID, rois := range cfg.Zones {
		for i, roi := range rois {
			if roi.Name == id {
				cfg.Zones[cameraID] = append(rois[:i], rois[i+1:]...)
				if len(cfg.Zones[cameraID]) == 0 {
					delete(cfg.Zones, cameraID)
				}
				found = true
				break
			}
		}
		if found {
			break
		}
	}

	if !found {
		writeError(w, http.StatusNotFound, "zone not found")
		return
	}

	h.manager.UpdateConfig(cfg)

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// --- Helpers ---

// detectionHasClass checks if any detection in the event matches the given class label.
func detectionHasClass(evt ai.DetectionEvent, class string) bool {
	for _, det := range evt.Detections {
		if det.ClassLabel == class {
			return true
		}
	}
	return false
}
