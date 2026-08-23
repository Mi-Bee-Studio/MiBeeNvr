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

// telemetryDetails extracts the protocol string from the optional details
// object ("ws" | "flv" | "hls" | "webrtc" | "mjpeg"; "" when absent).
func telemetryProtocol(details any) string {
	if m, ok := details.(map[string]any); ok {
		if p, ok := m["protocol"].(string); ok {
			return p
		}
	}
	return ""
}

// HandleTelemetry receives playback telemetry events, logs them via slog, and
// aggregates flow-observability events into Prometheus (#469 Phase 3):
//
//   - event "live_latency":  duration_ms = player-measured end-to-end live
//     latency → nvr_playback_live_latency_ms{camera,protocol} gauge
//   - event "playback_stall": playback freeze/buffering
//     → nvr_playback_stalls_total{camera,protocol} counter
//
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

	// Aggregate before the raw log line — metrics survive log rotation.
	if apiMetrics != nil && req.CameraID != "" {
		protocol := telemetryProtocol(req.Details)
		switch req.Event {
		case "live_latency":
			apiMetrics.SetPlaybackLiveLatency(req.CameraID, protocol, float64(req.DurationMs))
		case "playback_stall":
			apiMetrics.IncPlaybackStall(req.CameraID, protocol)
		}
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
