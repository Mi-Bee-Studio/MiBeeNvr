package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/ai"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/ai/engine"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// --- AI interfaces for testability ---

// AIEngine abstracts the AI engine lifecycle for the handler.
type AIEngine interface {
	IsAvailable() bool
	Name() string
	ModelPath() string
}

// AIDetector abstracts per-camera AI detection management for the handler.
type AIDetector interface {
	EnableCamera(camID string, hub *model.StreamHub) error
	DisableCamera(camID string)
	IsEnabled(camID string) bool
	EnabledCameras() []string
	OnDetection(cb engine.OnDetectionFunc)
	StopAll()
}

// --- AI status types ---

// aiStatusResponse is the JSON response for GET /api/ai/status.
type aiStatusResponse struct {
	Available    bool          `json:"available"`
	EngineStatus string        `json:"engine_status"` // "running", "stopped", "not_installed"
	Model        string        `json:"model"`
	Probe        engine.ProbeInfo `json:"probe"`
}

// aiEnableRequest is the JSON body for POST /api/ai/enable.
type aiEnableRequest struct {
	CameraID string `json:"camera_id"`
}

// aiDisableRequest is the JSON body for POST /api/ai/disable.
type aiDisableRequest struct {
	CameraID string `json:"camera_id"`
}

// --- AI setter ---

// SetAIComponents sets the AI engine and detector on the handler.
func (h *Handler) SetAIComponents(eng AIEngine, det AIDetector) {
	h.aiEngine = eng
	h.aiDetector = det
}

// --- AI handlers ---

// handleGetAIStatus handles GET /api/ai/status.
// Returns AI engine availability, model info, and probe details.
func (h *Handler) handleGetAIStatus(w http.ResponseWriter, r *http.Request) {
	if h.aiEngine == nil {
		writeJSON(w, http.StatusOK, aiStatusResponse{
			Available:    false,
			EngineStatus: "not_installed",
			Model:        "",
			Probe:        engine.ProbeInfo{},
		})
		return
	}

	status := "stopped"
	if h.aiEngine.IsAvailable() {
		status = "running"
	}

	writeJSON(w, http.StatusOK, aiStatusResponse{
		Available:    h.aiEngine.IsAvailable(),
		EngineStatus: status,
		Model:        h.aiEngine.ModelPath(),
		Probe:        engine.ProbeInfo{},
	})
}

// handleEnableAI handles POST /api/ai/enable.
// Enables AI detection for a camera by subscribing to its StreamHub.
func (h *Handler) handleEnableAI(w http.ResponseWriter, r *http.Request) {
	if h.aiDetector == nil {
		writeError(w, http.StatusServiceUnavailable, "AI detection not available")
		return
	}

	var req aiEnableRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.CameraID == "" {
		writeError(w, http.StatusBadRequest, "missing camera_id")
		return
	}

	// Validate camera exists.
	if h.db != nil {
		cam, err := h.db.GetCamera(r.Context(), req.CameraID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to get camera")
			return
		}
		if cam == nil {
			writeError(w, http.StatusNotFound, "camera not found")
			return
		}
	}

	// Get the camera's StreamHub.
	if h.camMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "camera manager not available")
		return
	}
	rec := h.camMgr.GetRecorder(req.CameraID)
	if rec == nil {
		writeError(w, http.StatusBadRequest, "camera recorder not running")
		return
	}
	hub := getStreamHub(rec)
	if hub == nil {
		writeError(w, http.StatusServiceUnavailable, "camera stream hub not available")
		return
	}

	if err := h.aiDetector.EnableCamera(req.CameraID, hub); err != nil {
		writeError(w, http.StatusConflict, fmt.Sprintf("failed to enable AI: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"status":    "enabled",
		"camera_id": req.CameraID,
	})
}

// handleDisableAI handles POST /api/ai/disable.
// Disables AI detection for a camera.
func (h *Handler) handleDisableAI(w http.ResponseWriter, r *http.Request) {
	if h.aiDetector == nil {
		writeError(w, http.StatusServiceUnavailable, "AI detection not available")
		return
	}

	var req aiDisableRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.CameraID == "" {
		writeError(w, http.StatusBadRequest, "missing camera_id")
		return
	}

	// Check if camera is enabled (idempotent).
	if !h.aiDetector.IsEnabled(req.CameraID) {
		writeJSON(w, http.StatusOK, map[string]string{
			"status":    "disabled",
			"camera_id": req.CameraID,
		})
		return
	}

	h.aiDetector.DisableCamera(req.CameraID)

	writeJSON(w, http.StatusOK, map[string]string{
		"status":    "disabled",
		"camera_id": req.CameraID,
	})
}

// handleAIEvents handles GET /api/ai/events.
// SSE stream that pushes detection events to the frontend.
func (h *Handler) handleAIEvents(w http.ResponseWriter, r *http.Request) {
	if h.aiDetector == nil {
		writeError(w, http.StatusServiceUnavailable, "AI detection not available")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ctx := r.Context()
	eventCh := make(chan engine.DetectionResult, 16)

	// Register detection callback.
	h.aiDetector.OnDetection(func(result engine.DetectionResult) {
		select {
		case eventCh <- result:
		default:
			// Drop event if client is too slow.
			logger.Warn("AI SSE: dropping detection event, client too slow")
		}
	})

	// Heartbeat ticker.
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	defer close(eventCh)

	for {
		select {
		case <-ctx.Done():
			// Client disconnected.
			return
		case result := <-eventCh:
			data, err := json.Marshal(result)
			if err != nil {
				logger.Warn("AI SSE: failed to marshal detection", "error", err)
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		case <-heartbeat.C:
			fmt.Fprintf(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

// --- ensure imports are used ---

var _ = ai.Detection{}
