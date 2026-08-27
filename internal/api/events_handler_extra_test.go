package api

// Tests for events_handler.go camera-specific SSE + cameraIDFromEventData
// (#578). Uses a real httptest.Server + cancellable request context: the
// SSE loop only ends on ctx cancellation, which the test drives explicitly
// (never a sleep-then-assert).

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/stretchr/testify/require"
)

func TestCameraEvents_NilBusIs503(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)
	rr := doRequest(t, h.Routes(), http.MethodGet, "/api/cameras/cam-1/events", nil, "", "")
	require.Equal(t, http.StatusServiceUnavailable, rr.Code)
}

func TestCameraEvents_SSEFiltersByCamera(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)
	bus := event.NewEventBus(16)
	h.SetEventBus(bus)

	srv := httptest.NewServer(h.Routes())
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/cameras/cam-1/events", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))

	// Events for other cameras are filtered out; ours arrive as SSE frames.
	lines := make(chan string, 64)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()

	// Alternate foreign-camera and our-camera events: only ours may be
	// written to the stream. Success = a data frame carrying our recording.
	var sawForeign bool
	require.Eventually(t, func() bool {
		bus.Publish(context.Background(), event.TopicSegmentCompleted,
			event.SegmentCompleted{CameraID: "cam-2", RecordingID: "foreign"})
		bus.Publish(context.Background(), event.TopicSegmentCompleted,
			event.SegmentCompleted{CameraID: "cam-1", RecordingID: "rec-9"})
		for {
			select {
			case line, ok := <-lines:
				if !ok {
					return false
				}
				if strings.Contains(line, "foreign") {
					sawForeign = true
				}
				if strings.Contains(line, "rec-9") {
					return true
				}
			default:
				return false
			}
		}
	}, 10*time.Second, 100*time.Millisecond, "camera-1 event never streamed")
	require.False(t, sawForeign, "foreign-camera event leaked through the filter")

	cancel()
}

func TestCameraIDFromEventData(t *testing.T) {
	t.Parallel()
	// Fast type-assertion paths.
	require.Equal(t, "c1", cameraIDFromEventData(event.SegmentCompleted{CameraID: "c1"}))
	require.Equal(t, "c2", cameraIDFromEventData(event.SegmentDeleted{CameraID: "c2"}))
	require.Equal(t, "c3", cameraIDFromEventData(event.StorageHealthChanged{CameraID: "c3"}))
	require.Equal(t, "c4", cameraIDFromEventData(event.AIDetectionEvent{CameraID: "c4"}))

	// Map fast path with several key spellings.
	require.Equal(t, "m1", cameraIDFromEventData(map[string]interface{}{"camera_id": "m1"}))
	require.Equal(t, "m2", cameraIDFromEventData(map[string]interface{}{"CameraID": "m2"}))
	require.Equal(t, "m3", cameraIDFromEventData(map[string]interface{}{"camera": "m3"}))
	require.Equal(t, "", cameraIDFromEventData(map[string]interface{}{"camera_id": 42}))
	require.Equal(t, "", cameraIDFromEventData(map[string]interface{}{}))

	// JSON fallback for ad-hoc struct types.
	type adhoc struct {
		Camera string `json:"camera"`
	}
	require.Equal(t, "j1", cameraIDFromEventData(adhoc{Camera: "j1"}))
	require.Equal(t, "", cameraIDFromEventData(42)) // not marshalable to a map with a key
}
