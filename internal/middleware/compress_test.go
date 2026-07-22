package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStreamingGzip_JSONResponse(t *testing.T) {
	handler := StreamingGzip(5)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Use a larger payload so gzip actually compresses (tiny payloads
		// may use stored mode and contain the original text).
		w.Write([]byte(`{"cameras":[{"id":"cam1","name":"Front Door","protocol":"rtsp","encoding":"h264","url":"rtsp://192.168.1.10/stream"},{"id":"cam2","name":"Back Yard","protocol":"rtsp","encoding":"h265","url":"rtsp://192.168.1.11/stream"},{"id":"cam3","name":"Garage","protocol":"http","encoding":"jpeg","url":"http://192.168.1.12/capture"}]}`))
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/cameras", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, "gzip", rec.Header().Get("Content-Encoding"))
	assert.Equal(t, "Accept-Encoding", rec.Header().Get("Vary"))
	// Body should start with gzip magic bytes (0x1f 0x8b).
	body := rec.Body.Bytes()
	assert.Greater(t, len(body), 0)
	assert.Equal(t, byte(0x1f), body[0], "body should start with gzip magic")
	assert.Equal(t, byte(0x8b), body[1])
}

func TestStreamingGzip_NoAcceptEncoding(t *testing.T) {
	handler := StreamingGzip(5)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	// No Accept-Encoding header
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Empty(t, rec.Header().Get("Content-Encoding"))
	assert.Contains(t, rec.Body.String(), "ok")
}

func TestStreamingGzip_SkipsVideo(t *testing.T) {
	handler := StreamingGzip(5)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("fake-video-data-that-should-not-be-compressed"))
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/recordings/abc/download", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// Video should NOT be compressed
	assert.Empty(t, rec.Header().Get("Content-Encoding"))
	assert.Contains(t, rec.Body.String(), "fake-video-data")
}

func TestStreamingGzip_SSEFlush(t *testing.T) {
	handler := StreamingGzip(5)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		require.True(t, ok, "ResponseWriter should implement Flusher")

		// Write an SSE event and flush immediately.
		w.Write([]byte("event: camera.started\ndata: {\"id\":\"cam1\"}\n\n"))
		flusher.Flush()
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/events", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// SSE should be compressed (text/event-stream is text).
	assert.Equal(t, "gzip", rec.Header().Get("Content-Encoding"))
}

func TestStreamingGzip_WebSocketSkipped(t *testing.T) {
	handler := StreamingGzip(5)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	}))

	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("Upgrade", "websocket")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// WebSocket upgrades should NOT be compressed
	assert.Empty(t, rec.Header().Get("Content-Encoding"))
}

func TestShouldSkipCompression(t *testing.T) {
	tests := []struct {
		ct   string
		want bool
	}{
		{"video/mp4", true},
		{"image/jpeg", true},
		{"audio/aac", true},
		{"application/json", false},
		{"text/html; charset=utf-8", false},
		{"text/event-stream", false},
		{"application/octet-stream", true},
		{"", false}, // unknown — compress by default
	}
	for _, tt := range tests {
		t.Run(tt.ct, func(t *testing.T) {
			assert.Equal(t, tt.want, shouldSkipCompression(tt.ct))
		})
	}
}
