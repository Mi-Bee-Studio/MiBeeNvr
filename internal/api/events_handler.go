package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/go-chi/chi/v5"
)

// handleEvents handles GET /api/events.
// Generic SSE endpoint that streams events from the EventBus.
// Supports ?filter=onvif. to filter by topic prefix.
func (h *Handler) handleEvents(w http.ResponseWriter, r *http.Request) {
	if h.eventBus == nil {
		WriteError(w, http.StatusServiceUnavailable, "event bus not available")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		WriteError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	filter := r.URL.Query().Get("filter") // e.g., "onvif."

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ctx := r.Context()
	eventCh := make(chan event.Event, 64)

	// Use empty prefix to match all topics when no filter is specified.
	prefix := filter
	h.eventBus.SubscribeByPrefix(prefix, eventCh, 64)

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	defer func() {
		h.eventBus.UnsubscribeByPrefix(prefix, eventCh)
		close(eventCh)
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case evt := <-eventCh:
			data, err := json.Marshal(evt)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", evt.Topic, data)
			flusher.Flush()
		case <-heartbeat.C:
			fmt.Fprintf(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

// handleCameraEvents handles GET /api/cameras/{id}/events.
// SSE endpoint that streams camera-specific events from the EventBus.
// Filters events by camera ID extracted from the event data.
func (h *Handler) handleCameraEvents(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "id")

	if h.eventBus == nil {
		WriteError(w, http.StatusServiceUnavailable, "event bus not available")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		WriteError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ctx := r.Context()
	eventCh := make(chan event.Event, 64)

	// Subscribe to all events, filter by camera ID later.
	h.eventBus.SubscribeByPrefix("", eventCh, 64)

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	defer func() {
		h.eventBus.UnsubscribeByPrefix("", eventCh)
		close(eventCh)
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case evt := <-eventCh:
			// Filter events by camera ID.
			if cameraIDFromEventData(evt.Data) != cameraID {
				continue
			}
			data, err := json.Marshal(evt)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", evt.Topic, data)
			flusher.Flush()
		case <-heartbeat.C:
			fmt.Fprintf(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

// cameraIDFromEventData attempts to extract a camera ID from event data.
// Uses fast type assertion for known event types (avoids JSON round-trip on
// the hot SSE path). Falls back to JSON reflection for unknown types.
func cameraIDFromEventData(data interface{}) string {
	// Fast path: type-assert known event structs (zero allocation).
	switch d := data.(type) {
	case event.SegmentCompleted:
		return d.CameraID
	case event.SegmentDeleted:
		return d.CameraID
	case event.StorageHealthChanged:
		return d.CameraID
	case event.AIDetectionEvent:
		return d.CameraID
	case map[string]interface{}:
		for _, key := range []string{"camera_id", "CameraID", "camera", "Camera"} {
			if id, ok := d[key].(string); ok && id != "" {
				return id
			}
		}
		return ""
	}
	// Fallback: JSON round-trip for ad-hoc types.
	b, err := json.Marshal(data)
	if err != nil {
		return ""
	}
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		return ""
	}
	for _, key := range []string{"camera_id", "CameraID", "camera", "Camera"} {
		if id, ok := m[key].(string); ok && id != "" {
			return id
		}
	}
	return ""
}
