package api

import (
    "encoding/json"
    "fmt"
    "net/http"
    "time"

    "github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
)

// handleEvents handles GET /api/events.
// Generic SSE endpoint that streams events from the EventBus.
// Supports ?filter=onvif. to filter by topic prefix.
func (h *Handler) handleEvents(w http.ResponseWriter, r *http.Request) {
	if h.eventBus == nil {
		writeError(w, http.StatusServiceUnavailable, "event bus not available")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
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

