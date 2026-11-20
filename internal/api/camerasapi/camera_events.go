package camerasapi

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/httpx"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/go-chi/chi/v5"
)

func (s *server) handleCameraEvents(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "id")

	if s.deps.EventBus == nil {
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
	s.deps.EventBus.SubscribeByPrefix("", eventCh, 64)

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	defer func() {
		s.deps.EventBus.UnsubscribeByPrefix("", eventCh)
		close(eventCh)
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.deps.ShutdownSignaled():
			return
		case evt := <-eventCh:
			// Filter events by camera ID. pre is non-nil when the fallback
			// path already serialized Data — the frame is then composed from
			// those bytes instead of reflecting over the struct a second
			// time (#875 M4).
			id, pre := event.CameraIDFromEventData(evt.Data)
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
			httpx.WriteSSEEvent(w, evt.Topic, data)
			flusher.Flush()
		case <-heartbeat.C:
			_, _ = w.Write([]byte(": ping\n\n"))
			flusher.Flush()
		}
	}
}
