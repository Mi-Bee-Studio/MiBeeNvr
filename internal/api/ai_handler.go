package api

import (
	"encoding/json"
	"net/http"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/ai"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/go-chi/chi/v5"
)

// AIHandler holds dependencies for AI REST API endpoints.
// Only config management and ROI zone CRUD — no backend inference.
type AIHandler struct {
	manager    *ai.Manager
	config     *config.Config
	configPath string
}

// NewAIHandler creates a new AIHandler.
func NewAIHandler(mgr *ai.Manager, cfg *config.Config, configPath string) *AIHandler {
	return &AIHandler{
		manager:    mgr,
		config:     cfg,
		configPath: configPath,
	}
}

// syncAndSaveConfig maps the AI manager's in-memory config back to the shared
// config.Config and persists it to disk. Matches the pattern used by system handlers.
func (h *AIHandler) syncAndSaveConfig() {
	aiCfg := h.manager.GetConfig()
	h.config.AI = config.AIConfig{
		Enabled:             aiCfg.Enabled,
		EnabledCameras:      aiCfg.EnabledCameras,
		ModelURL:            aiCfg.ModelURL,
		Zones:               aiCfg.Zones,
		FrameSkipRate:       aiCfg.FrameSkipRate,
		ConfidenceThreshold: aiCfg.ConfidenceThreshold,
	}
	if err := config.Save(h.configPath, h.config); err != nil {
		logger.Warn("failed to save AI config", "error", err)
	}
}

// --- Status endpoint ---

// handleAIStatus handles GET /api/ai/status.
// Returns global AI config state — no per-camera inference status.
func (h *AIHandler) handleAIStatus(w http.ResponseWriter, r *http.Request) {
	cfg := h.manager.GetConfig()

	resp := map[string]any{
		"enabled":              cfg.Enabled,
		"model_url":            cfg.ModelURL,
		"confidence_threshold": cfg.ConfidenceThreshold,
		"frame_skip_rate":      cfg.FrameSkipRate,
	}
	writeJSON(w, http.StatusOK, resp)
}

// --- Config update ---

// handleAIUpdateConfig handles PUT /api/ai/config.
// Updates global AI config at runtime (enabled, threshold, frame skip, etc.).
func (h *AIHandler) handleAIUpdateConfig(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled             *bool    `json:"enabled,omitempty"`
		ConfidenceThreshold *float64 `json:"confidence_threshold,omitempty"`
		FrameSkipRate       *int     `json:"frame_skip_rate,omitempty"`
		ModelURL            *string  `json:"model_url,omitempty"`
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
	if body.ModelURL != nil {
		cfg.ModelURL = *body.ModelURL
	}

	h.manager.UpdateConfig(cfg)
	h.syncAndSaveConfig()

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// --- Zone CRUD endpoints ---

type zoneRequestBody struct {
	CameraID string       `json:"camera_id"`
	Zone     zoneBodyZone `json:"zone"`
	Enabled  bool         `json:"enabled"`
}

type zoneBodyZone struct {
	Name   string       `json:"name"`
	Points [][2]float64 `json:"points"`
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
	h.syncAndSaveConfig()

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
	h.syncAndSaveConfig()

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
	h.syncAndSaveConfig()

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
