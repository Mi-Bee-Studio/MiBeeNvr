package api

import (
	"encoding/json"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/httpx"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
)

// maxSSEPerIP caps concurrent SSE streams per client IP so one address cannot
// hold unlimited long-lived connections.
const maxSSEPerIP = 6

// handleEvents handles GET /api/events.
// Generic SSE endpoint that streams events from the EventBus.
// Supports ?filter=onvif. to filter by topic prefix.
func (h *Handler) handleEvents(w http.ResponseWriter, r *http.Request) {
	if h.eventBus == nil {
		WriteError(w, http.StatusServiceUnavailable, "event bus not available")
		return
	}
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = r.RemoteAddr
	}
	cnt, _ := h.ssePerIP.LoadOrStore(ip, new(atomic.Int32))
	if cnt.(*atomic.Int32).Add(1) > maxSSEPerIP {
		cnt.(*atomic.Int32).Add(-1)
		WriteError(w, http.StatusTooManyRequests, "too many SSE connections from this address")
		return
	}
	defer cnt.(*atomic.Int32).Add(-1)

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
		case <-streamShutdown:
			return
		case evt := <-eventCh:
			data, err := json.Marshal(evt)
			if err != nil {
				continue
			}
			writeSSEEvent(w, evt.Topic, data)
			flusher.Flush()
		case <-heartbeat.C:
			_, _ = w.Write([]byte(": ping\n\n"))
			flusher.Flush()
		}
	}
}

// writeSSEEvent emits one server-sent event frame. Byte concatenation
// replaces fmt.Fprintf's format parsing + reflection on the per-event path
// (#875 L2); the frame layout is identical.
func writeSSEEvent(w http.ResponseWriter, topic string, data []byte) {
	httpx.WriteSSEEvent(w, topic, data)
}

// handleCameraEvents handles GET /api/cameras/{id}/events.
// SSE endpoint that streams camera-specific events from the EventBus.
// Filters events by camera ID extracted from the event data.

// cameraIDFromEventData extracts a camera ID from event data — canonical
// implementation lives in internal/event (shared with camerasapi).
func cameraIDFromEventData(data interface{}) (string, []byte) {
	return event.CameraIDFromEventData(data)
}
