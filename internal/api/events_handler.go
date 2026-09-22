package api

import (
	"encoding/json"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/go-chi/chi/v5"
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
	b := make([]byte, 0, len("event: \ndata: \n\n")+len(topic)+len(data))
	b = append(b, "event: "...)
	b = append(b, topic...)
	b = append(b, "\ndata: "...)
	b = append(b, data...)
	b = append(b, "\n\n"...)
	_, _ = w.Write(b)
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

	// Subscribe to all events, filter by camera ID later. The bus never
	// blocks on a slow subscriber (ring-overflow drops oldest, non-blocking
	// send), so the broad subscription is safe; enumerating "camera topics"
	// instead would silently miss future topics this endpoint promises to
	// carry.
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
		case <-streamShutdown:
			return
		case evt := <-eventCh:
			// Filter events by camera ID. pre is non-nil when the fallback
			// path already serialized Data — the frame is then composed from
			// those bytes instead of reflecting over the struct a second
			// time (#875 M4).
			id, pre := cameraIDFromEventData(evt.Data)
			if id != cameraID {
				continue
			}
			var data []byte
			if pre != nil {
				topicB, err := json.Marshal(evt.Topic)
				if err != nil {
					continue
				}
				data = []byte(`{"Topic":` + string(topicB) + `,"Data":` + string(pre) + `}`)
			} else {
				var err error
				data, err = json.Marshal(evt)
				if err != nil {
					continue
				}
			}
			writeSSEEvent(w, evt.Topic, data)
			flusher.Flush()
		case <-heartbeat.C:
			_, _ = w.Write([]byte(": ping\n\n"))
			flusher.Flush()
		}
	}
}

// cameraIDFromEventData attempts to extract a camera ID from event data.
// Uses fast type assertion for known event types (zero allocation). Falls
// back to one JSON serialize+parse pass for unknown types, whose serialized
// bytes are returned so the caller can compose the SSE frame without a
// second reflection round-trip (#875 M4).
func cameraIDFromEventData(data interface{}) (id string, marshaled []byte) {
	// Fast path: type-assert known event structs (zero allocation).
	switch d := data.(type) {
	case event.SegmentCompleted:
		return d.CameraID, nil
	case event.SegmentDeleted:
		return d.CameraID, nil
	case event.StorageHealthChanged:
		return d.CameraID, nil
	case event.AIDetectionEvent:
		return d.CameraID, nil
	case event.CameraSnapshotEvent:
		return d.CameraID, nil
	case event.GB28181AlarmEvent:
		return d.CameraID, nil
	case map[string]interface{}:
		for _, key := range []string{"camera_id", "CameraID", "camera", "Camera"} {
			if id, ok := d[key].(string); ok && id != "" {
				return id, nil
			}
		}
		return "", nil
	}
	// Fallback: one JSON round-trip for ad-hoc types.
	b, err := json.Marshal(data)
	if err != nil {
		return "", nil
	}
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		return "", nil
	}
	for _, key := range []string{"camera_id", "CameraID", "camera", "Camera"} {
		if id, ok := m[key].(string); ok && id != "" {
			return id, b
		}
	}
	return "", nil
}
