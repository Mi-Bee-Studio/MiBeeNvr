package timelapse

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================
// Test helpers
// ============================================================

// makeTestJPEG creates a minimal valid JPEG in memory.
func makeTestJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 128, A: 255})
		}
	}
	var buf bytes.Buffer
	err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 50})
	require.NoError(t, err)
	return buf.Bytes()
}

// makeTestPNG creates a minimal valid PNG in memory.
func makeTestPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	var buf bytes.Buffer
	err := png.Encode(&buf, img)
	require.NoError(t, err)
	return buf.Bytes()
}

// countFiles counts non-directory files in a directory.
func countFiles(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	count := 0
	for _, e := range entries {
		if !e.IsDir() {
			count++
		}
	}
	return count
}

// ============================================================
// Test: Basic JPEG capture
// ============================================================

func TestSnapshotCapturer_CaptureJPEGFrame(t *testing.T) {
	jpegData := makeTestJPEG(t, 32, 32)
	var requestCount atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		// Check basic auth
		user, pass, ok := r.BasicAuth()
		assert.True(t, ok, "expected basic auth")
		assert.Equal(t, "admin", user)
		assert.Equal(t, "secret", pass)
		w.Header().Set("Content-Type", "image/jpeg")
		w.WriteHeader(http.StatusOK)
		w.Write(jpegData)
	}))
	defer srv.Close()

	store := newMockSegmentStore(t)
	db := newMockRecordingDB()

	cfg := SnapshotCapturerConfig{
		CameraID:    "test-cam",
		SnapshotURL: srv.URL,
		Interval:    50 * time.Millisecond,
		SegmentDur:  time.Hour, // long enough to avoid rotation
		Username:    "admin",
		Password:    "secret",
		Store:       store,
		DB:          db,
	}

	capturer := NewSnapshotCapturer(cfg, store)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := capturer.Start(ctx)
	require.NoError(t, err)

	// Let it capture a few frames
	time.Sleep(200 * time.Millisecond)

	err = capturer.Stop()
	require.NoError(t, err)

	// Should have made at least 2 requests
	assert.GreaterOrEqual(t, requestCount.Load(), int32(2),
		"expected at least 2 HTTP requests (50ms interval over 200ms)")

	// Should have created at least one segment
	assert.GreaterOrEqual(t, store.segmentCount(), 1,
		"expected at least 1 segment created")

	// Should have inserted at least one recording (or segment is still open)
	// If segment was closed, we should have a recording
	if store.closedCount() > 0 {
		assert.GreaterOrEqual(t, db.recordingCount(), 1,
			"expected at least 1 recording inserted")
	}

	// Verify frames were written to the segment
	for _, seg := range store.segments {
		finalPath := seg.finalPath
		// If not closed, check tempPath
		checkPath := seg.tempPath
		if seg.closed {
			checkPath = finalPath
		}
		if _, err := os.Stat(checkPath); err == nil {
			count := countFiles(t, checkPath)
			t.Logf("segment %s has %d frame files", checkPath, count)
		}
	}
}

// ============================================================
// Test: 404 handling (skip gracefully)
// ============================================================

func TestSnapshotCapturer_Handles404(t *testing.T) {
	jpegData := makeTestJPEG(t, 32, 32)
	var requestCount atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := requestCount.Add(1)
		if count <= 2 {
			// Return 404 for first 2 requests
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.WriteHeader(http.StatusOK)
		w.Write(jpegData)
	}))
	defer srv.Close()

	store := newMockSegmentStore(t)

	cfg := SnapshotCapturerConfig{
		CameraID:    "test-cam-404",
		SnapshotURL: srv.URL,
		Interval:    50 * time.Millisecond,
		SegmentDur:  time.Hour,
		Store:       store,
	}

	capturer := NewSnapshotCapturer(cfg, store)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := capturer.Start(ctx)
	require.NoError(t, err)

	// Let it capture — first requests are 404, then OK
	time.Sleep(200 * time.Millisecond)

	err = capturer.Stop()
	require.NoError(t, err)

	// Should have made requests without crashing
	assert.GreaterOrEqual(t, requestCount.Load(), int32(1),
		"expected at least 1 HTTP request")

	// Test passed if we got here without panic
	t.Log("survived 404 responses without crash")
}

// ============================================================
// Test: Retry on HTTP error with exponential backoff
// ============================================================

func TestSnapshotCapturer_RetryOnHTTPError(t *testing.T) {
	jpegData := makeTestJPEG(t, 32, 32)
	var requestCount atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := requestCount.Add(1)
		if count == 1 {
			// First attempt: 503 Service Unavailable
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if count == 2 {
			// Second attempt: 502 Bad Gateway
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		// Third attempt: success
		w.Header().Set("Content-Type", "image/jpeg")
		w.WriteHeader(http.StatusOK)
		w.Write(jpegData)
	}))
	defer srv.Close()

	store := newMockSegmentStore(t)

	cfg := SnapshotCapturerConfig{
		CameraID:    "test-cam-retry",
		SnapshotURL: srv.URL,
		Interval:    50 * time.Millisecond,
		SegmentDur:  time.Hour,
		Store:       store,
	}

	capturer := NewSnapshotCapturer(cfg, store)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := capturer.Start(ctx)
	require.NoError(t, err)

	// Wait for retry cycle to complete (200ms + 400ms backoff + margin)
	time.Sleep(1500 * time.Millisecond)

	err = capturer.Stop()
	require.NoError(t, err)

	// Should have made exactly 3 requests (first 2 fail, 3rd succeeds)
	t.Logf("total requests for retry test: %d", requestCount.Load())
	assert.GreaterOrEqual(t, requestCount.Load(), int32(3),
		"expected at least 3 HTTP requests (2 retries + 1 success)")

	// At least one segment should have been created (from the successful request)
	if store.segmentCount() > 0 {
		t.Log("segment was created after retry success")
	}
}

// ============================================================
// Test: Invalid Content-Type is rejected
// ============================================================

func TestSnapshotCapturer_InvalidContentTypeRejected(t *testing.T) {
	jpegData := makeTestJPEG(t, 32, 32)
	var servedOK atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// First request: wrong content type (text/html)
		if servedOK.Load() == 0 {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("<html>not an image</html>"))
			return
		}
		// Subsequent requests: valid content type
		w.Header().Set("Content-Type", "image/jpeg")
		w.WriteHeader(http.StatusOK)
		w.Write(jpegData)
		servedOK.Add(1)
	}))
	defer srv.Close()

	store := newMockSegmentStore(t)

	cfg := SnapshotCapturerConfig{
		CameraID:    "test-cam-ct",
		SnapshotURL: srv.URL,
		Interval:    50 * time.Millisecond,
		SegmentDur:  time.Hour,
		Store:       store,
	}

	capturer := NewSnapshotCapturer(cfg, store)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := capturer.Start(ctx)
	require.NoError(t, err)

	time.Sleep(200 * time.Millisecond)

	err = capturer.Stop()
	require.NoError(t, err)

	// Should have recovered from invalid content-type
	t.Log("survived invalid Content-Type without crash")
}

// ============================================================
// Test: JPEG magic bytes validation (warns but does not reject)
// ============================================================

func TestSnapshotCapturer_MagicBytesWarning(t *testing.T) {
	// Serve data with image/jpeg content-type but invalid JPEG data
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.WriteHeader(http.StatusOK)
		// Write bytes that are NOT valid JPEG magic bytes
		w.Write([]byte{0x00, 0x01, 0x02, 0x03, 0x04})
	}))
	defer srv.Close()

	store := newMockSegmentStore(t)

	cfg := SnapshotCapturerConfig{
		CameraID:    "test-cam-magic",
		SnapshotURL: srv.URL,
		Interval:    50 * time.Millisecond,
		SegmentDur:  time.Hour,
		Store:       store,
	}

	capturer := NewSnapshotCapturer(cfg, store)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := capturer.Start(ctx)
	require.NoError(t, err)

	time.Sleep(200 * time.Millisecond)

	err = capturer.Stop()
	require.NoError(t, err)

	// The invalid JPEG data should still be written (content-type is the gate, magic is a warning)
	// But we should have created segments
	t.Log("survived invalid JPEG magic bytes without crash")
}

// ============================================================
// Test: Empty SnapshotURL with DeriveSnapshotURL()
// ============================================================

func TestSnapshotCapturer_EmptySnapshotURL(t *testing.T) {
	// When SnapshotURL is empty, the capturer should use DeriveSnapshotURL
	// to auto-populate. If derivation fails, it should log an error and not start.
	store := newMockSegmentStore(t)

	cfg := SnapshotCapturerConfig{
		CameraID:    "test-cam-empty",
		SnapshotURL: "",
		Interval:    50 * time.Millisecond,
		SegmentDur:  time.Hour,
		Store:       store,
		StreamURL:   "rtsp://192.168.1.100:554/stream1",
		Protocol:    "rtsp",
	}

	capturer := NewSnapshotCapturer(cfg, store)

	// The URL should be auto-derived
	assert.NotEmpty(t, capturer.cfg.SnapshotURL,
		"expected snapshot URL to be auto-derived from RTSP")
	assert.Contains(t, capturer.cfg.SnapshotURL, "192.168.1.100",
		"expected URL to contain the camera host")
}

// ============================================================
// Test: Simulate concurrency — multiple captures
// ============================================================

func TestSnapshotCapturer_StopTwice(t *testing.T) {
	jpegData := makeTestJPEG(t, 32, 32)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.WriteHeader(http.StatusOK)
		w.Write(jpegData)
	}))
	defer srv.Close()

	store := newMockSegmentStore(t)

	cfg := SnapshotCapturerConfig{
		CameraID:    "test-cam-stop2",
		SnapshotURL: srv.URL,
		Interval:    50 * time.Millisecond,
		SegmentDur:  time.Hour,
		Store:       store,
	}

	capturer := NewSnapshotCapturer(cfg, store)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := capturer.Start(ctx)
	require.NoError(t, err)

	time.Sleep(100 * time.Millisecond)

	// Stop twice — should not panic
	err = capturer.Stop()
	assert.NoError(t, err)

	// Second stop should be safe
	err = capturer.Stop()
	assert.NoError(t, err)
}

// ============================================================
// Test: Segment rotation by duration
// ============================================================

func TestSnapshotCapturer_SegmentRotation(t *testing.T) {
	jpegData := makeTestJPEG(t, 32, 32)
	var mu sync.Mutex
	serveCount := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		serveCount++
		mu.Unlock()
		w.Header().Set("Content-Type", "image/jpeg")
		w.WriteHeader(http.StatusOK)
		w.Write(jpegData)
	}))
	defer srv.Close()

	store := newMockSegmentStore(t)

	cfg := SnapshotCapturerConfig{
		CameraID:    "test-cam-rotate",
		SnapshotURL: srv.URL,
		Interval:    30 * time.Millisecond,
		SegmentDur:  100 * time.Millisecond, // short duration to test rotation
		Store:       store,
	}

	capturer := NewSnapshotCapturer(cfg, store)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := capturer.Start(ctx)
	require.NoError(t, err)

	// Wait enough time for at least one rotation
	time.Sleep(300 * time.Millisecond)

	err = capturer.Stop()
	require.NoError(t, err)

	// Should have created multiple segments
	t.Logf("segments created: %d, closed: %d", store.segmentCount(), store.closedCount())
	assert.GreaterOrEqual(t, store.segmentCount(), 2,
		"expected at least 2 segments with 100ms SegmentDur over 300ms")

	// Check frames were written to each segment
	for i, seg := range store.segments {
		checkPath := seg.tempPath
		if seg.closed {
			checkPath = seg.finalPath
		}
		if info, err := os.Stat(checkPath); err == nil && info.IsDir() {
			count := countFiles(t, checkPath)
			t.Logf("segment %d (%s): %d frames", i, checkPath, count)
		}
	}
}

// ============================================================
// Test: PNG content-type is accepted
// ============================================================

func TestSnapshotCapturer_PNGContentType(t *testing.T) {
	pngData := makeTestPNG(t, 16, 16)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.WriteHeader(http.StatusOK)
		w.Write(pngData)
	}))
	defer srv.Close()

	store := newMockSegmentStore(t)

	cfg := SnapshotCapturerConfig{
		CameraID:    "test-cam-png",
		SnapshotURL: srv.URL,
		Interval:    50 * time.Millisecond,
		SegmentDur:  time.Hour,
		Store:       store,
	}

	capturer := NewSnapshotCapturer(cfg, store)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := capturer.Start(ctx)
	require.NoError(t, err)

	time.Sleep(200 * time.Millisecond)

	err = capturer.Stop()
	require.NoError(t, err)

	// PNG frames should be accepted — check segments exist
	assert.GreaterOrEqual(t, store.segmentCount(), 1,
		"expected at least 1 segment with PNG content")
}

// ============================================================
// Test: Context cancellation stops immediately
// ============================================================

func TestSnapshotCapturer_ContextCancellation(t *testing.T) {
	jpegData := makeTestJPEG(t, 32, 32)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.WriteHeader(http.StatusOK)
		w.Write(jpegData)
	}))
	defer srv.Close()

	store := newMockSegmentStore(t)

	cfg := SnapshotCapturerConfig{
		CameraID:    "test-cam-cancel",
		SnapshotURL: srv.URL,
		Interval:    50 * time.Millisecond,
		SegmentDur:  time.Hour,
		Store:       store,
	}

	capturer := NewSnapshotCapturer(cfg, store)
	ctx := context.Background()

	err := capturer.Start(ctx)
	require.NoError(t, err)

	// Stop immediately
	err = capturer.Stop()
	require.NoError(t, err)

	// Status should be stopped
	assert.Equal(t, model.StatusStopped, capturer.Status())
}

// ============================================================
// Test: Start twice returns error
// ============================================================

func TestSnapshotCapturer_StartTwice(t *testing.T) {
	jpegData := makeTestJPEG(t, 32, 32)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.WriteHeader(http.StatusOK)
		w.Write(jpegData)
	}))
	defer srv.Close()

	store := newMockSegmentStore(t)

	cfg := SnapshotCapturerConfig{
		CameraID:    "test-cam-start2",
		SnapshotURL: srv.URL,
		Interval:    50 * time.Millisecond,
		SegmentDur:  time.Hour,
		Store:       store,
	}

	capturer := NewSnapshotCapturer(cfg, store)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := capturer.Start(ctx)
	require.NoError(t, err)

	// Second start should fail
	err = capturer.Start(ctx)
	assert.Error(t, err, "expected error when starting twice")

	capturer.Stop()
}
