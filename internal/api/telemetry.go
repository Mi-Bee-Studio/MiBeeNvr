package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/middleware"
)

// telemetryRequest is the JSON payload for POST /api/telemetry.
type telemetryRequest struct {
	Event      string `json:"event"`
	CameraID   string `json:"camera_id"`
	DurationMs int    `json:"duration_ms"`
	Details    any    `json:"details,omitempty"`
}

// HandleTelemetry receives playback telemetry events and logs them via slog.
// It requires BasicAuth and is rate-limited to 10 requests/second per IP.
func (h *Handler) HandleTelemetry(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var req telemetryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	if req.Event == "" {
		WriteError(w, http.StatusBadRequest, "event is required")
		return
	}

	slog.Info(
		"telemetry",
		"event", req.Event,
		"camera_id", req.CameraID,
		"duration_ms", req.DurationMs,
		"details", req.Details,
		"remote_addr", r.RemoteAddr,
	)

	w.WriteHeader(http.StatusNoContent)
}

// telemetryRateLimiter returns a 10 req/s per-IP rate limiter.
func telemetryRateLimiter() func(http.Handler) http.Handler {
	rl := middleware.NewRateLimiter(context.Background(), middleware.RateLimiterConfig{
		MaxRequests: 10,
		Window:      time.Second,
	})
	return rl.Handler
}

// registerTelemetryRoute registers the telemetry ingestion endpoint with its
// own rate limiter.
func (h *Handler) registerTelemetryRoute(r chi.Router) {
	r.With(telemetryRateLimiter()).Post("/api/telemetry", h.HandleTelemetry)
}
