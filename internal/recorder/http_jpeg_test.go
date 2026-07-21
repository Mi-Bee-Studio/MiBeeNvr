package recorder

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/avi"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// generateSmallJPEG creates a valid 32x24 JPEG image for testing.
func generateSmallJPEG() []byte {
	img := image.NewYCbCr(image.Rect(0, 0, 32, 24), image.YCbCrSubsampleRatio420)
	for y := range 24 {
		for x := range 32 {
			c := color.YCbCr{Y: 128, Cb: 128, Cr: 128}
			img.Y[img.YOffset(x, y)] = c.Y
			img.Cb[img.COffset(x, y)] = c.Cb
			img.Cr[img.COffset(x, y)] = c.Cr
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 50}); err != nil {
		panic("generateSmallJPEG: " + err.Error())
	}
	return buf.Bytes()
}

// mjpegStreamHandler serves a multipart/x-mixed-replace MJPEG stream.
type mjpegStreamHandler struct {
	boundary string
	frameCh  chan []byte
	done     atomic.Bool
}

func (h *mjpegStreamHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.done.Store(false)
	defer h.done.Store(true)

	w.Header().Set("Content-Type", "multipart/x-mixed-replace;boundary="+h.boundary)
	w.WriteHeader(http.StatusOK)

	flusher, ok := w.(http.Flusher)
	if !ok {
		return
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case frame, ok := <-h.frameCh:
			if !ok {
				return
			}
			// Write MIME part: boundary + headers + JPEG data
			part := fmt.Sprintf("\r\n--%s\r\nContent-Type: image/jpeg\r\nContent-Length: %d\r\n\r\n", h.boundary, len(frame))
			if _, err := io.WriteString(w, part); err != nil {
				return
			}
			if _, err := w.Write(frame); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (h *mjpegStreamHandler) sendFrame(frame []byte) {
	if !h.done.Load() {
		h.frameCh <- frame
	}
}

func (h *mjpegStreamHandler) sendFrames(count int, interval time.Duration) {
	for range count {
		h.sendFrame(generateSmallJPEG())
		if interval > 0 {
			time.Sleep(interval)
		}
	}
}

func newMJPEGStreamServer() (*httptest.Server, *mjpegStreamHandler) {
	handler := &mjpegStreamHandler{
		boundary: "testboundary123",
		frameCh:  make(chan []byte, 100),
	}
	server := httptest.NewServer(handler)
	return server, handler
}

// --- Tests ---

func TestHTTPJPEGAVIRecording(t *testing.T) {
	srv, handler := newMJPEGStreamServer()
	defer srv.Close()

	mgr := newTestManager(t)
	rec := NewHTTPJPEGRecorder(HTTPJPEGConfig{
		CameraID:   "cam-http-jpeg-avi",
		URL:        srv.URL,
		SegmentDur: 30 * time.Second,
		AVI:        true,
		Width:      32,
		Height:     24,
	}, mgr)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	require.NoError(t, rec.Start(ctx))
	require.Equal(t, model.StatusRecording, rec.Status())

	// Wait for stream to connect, then send frames
	time.Sleep(200 * time.Millisecond)
	handler.sendFrames(10, 20*time.Millisecond)
	time.Sleep(200 * time.Millisecond)

	// Stop recorder
	require.NoError(t, rec.Stop())
	require.Equal(t, model.StatusStopped, rec.Status())

	// Verify AVI segment was created
	files, err := mgr.ListSegments("cam-http-jpeg-avi")
	require.NoError(t, err)
	require.NotEmpty(t, files, "expected at least one recorded segment")

	for _, f := range files {
		info, err := os.Stat(f)
		require.NoError(t, err)
		require.False(t, info.IsDir(), "AVI segment should be a file, not a directory")
		require.True(t, strings.HasSuffix(f, ".avi"), "AVI segment should have .avi extension: %s", f)

		// Verify it's a valid AVI file with video-only stream
		data, err := os.ReadFile(f)
		require.NoError(t, err)
		require.Greater(t, len(data), 128, "AVI file too small")

		// Use AVI demuxer to verify structure (no VideoStreams/AudioStreams methods)
		// Count chunks: expect video chunks and no audio chunks
		r := bytes.NewReader(data)
		d, err := avi.NewDemuxer(r)
		require.NoError(t, err, "should be a valid AVI file")
		videoCount, audioCount := 0, 0
		for {
			chunk, err := d.NextChunk()
			if err != nil {
				break
			}
			if chunk.Type == avi.ChunkVideo {
				videoCount++
			} else if chunk.Type == avi.ChunkAudio {
				audioCount++
			}
		}
		require.Greater(t, videoCount, 0, "AVI should have video chunks")
		require.Equal(t, 0, audioCount, "AVI should have no audio chunks")
	}
}

func TestHTTPJPEGSegmentDurCap(t *testing.T) {
	// When AVI=true with SegmentDur above the platform cap, it should be capped.
	// The cap is RAM-dependent: 30s on ≤2GB hosts, 5m on >2GB hosts.
	srv, handler := newMJPEGStreamServer()
	defer srv.Close()

	mgr := newTestManager(t)
	cfg := HTTPJPEGConfig{
		CameraID:   "cam-http-jpeg-cap",
		URL:        srv.URL,
		SegmentDur: 120 * time.Minute, // way over any platform cap
		AVI:        true,
		Width:      32,
		Height:     24,
	}
	rec := NewHTTPJPEGRecorder(cfg, mgr)
	want := aviSegmentDurCap()
	require.Equal(t, want, rec.cfg.SegmentDur,
		"SegmentDur should be capped at aviSegmentDurCap() = %v (got %v)", want, rec.cfg.SegmentDur)
	require.Less(t, rec.cfg.SegmentDur, cfg.SegmentDur,
		"capped value should be less than configured")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	require.NoError(t, rec.Start(ctx))

	time.Sleep(200 * time.Millisecond)
	handler.sendFrames(5, 20*time.Millisecond)
	time.Sleep(200 * time.Millisecond)

	require.NoError(t, rec.Stop())
}

func TestHTTPJPEGSegmentDurNoCap(t *testing.T) {
	// When AVI=false with SegmentDur > 30s, it should NOT be capped.
	cfg := HTTPJPEGConfig{
		CameraID:   "cam-no-cap",
		URL:        "http://127.0.0.1:1/test",
		SegmentDur: 120 * time.Second,
		AVI:        false,
	}
	mgr := newTestManager(t)
	rec := NewHTTPJPEGRecorder(cfg, mgr)
	require.Equal(t, 120*time.Second, rec.cfg.SegmentDur, "SegmentDur should NOT be capped when AVI=false")
	_ = mgr
}

func TestHTTPJPEGFlagDisabled(t *testing.T) {
	// AVI=false (default): should use dir-based MJPEG path.
	srv, handler := newMJPEGStreamServer()
	defer srv.Close()

	mgr := newTestManager(t)
	rec := NewHTTPJPEGRecorder(HTTPJPEGConfig{
		CameraID:   "cam-http-jpeg-dir",
		URL:        srv.URL,
		SegmentDur: 30 * time.Second,
		AVI:        false, // explicit default
	}, mgr)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	require.NoError(t, rec.Start(ctx))
	require.Equal(t, model.StatusRecording, rec.Status())

	time.Sleep(200 * time.Millisecond)
	handler.sendFrames(10, 20*time.Millisecond)
	time.Sleep(200 * time.Millisecond)

	require.NoError(t, rec.Stop())

	// Verify MJPEG directory segment was created
	files, err := mgr.ListSegments("cam-http-jpeg-dir")
	require.NoError(t, err)
	require.NotEmpty(t, files, "expected at least one recorded segment")

	for _, f := range files {
		info, err := os.Stat(f)
		require.NoError(t, err)
		require.True(t, info.IsDir(), "MJPEG segment should be a directory when AVI=false: %s", f)

		// Check for .jpg files inside
		entries, err := os.ReadDir(f)
		require.NoError(t, err)
		jpgCount := 0
		for _, e := range entries {
			if !e.IsDir() && filepath.Ext(e.Name()) == ".jpg" {
				jpgCount++
			}
		}
		require.Greater(t, jpgCount, 0, "MJPEG directory should contain .jpg files")
	}
}

// TestAviSegmentDurCap verifies the RAM-dependent cap returns one of the two
// documented values and that it's a positive duration.
func TestAviSegmentDurCap(t *testing.T) {
	durCap := aviSegmentDurCap()
	require.Greater(t, durCap, time.Duration(0), "cap must be positive")
	// Must be one of the two documented values
	if durCap != 30*time.Second && durCap != 5*time.Minute {
		t.Fatalf("aviSegmentDurCap() returned %v, expected 30s (≤2GB) or 5m (>2GB)", durCap)
	}
	t.Logf("aviSegmentDurCap() = %v on this host (memAvailableMB=%d)", durCap, memAvailableMB())
}

// TestMemAvailableMB returns a sane value on Linux.
func TestMemAvailableMB(t *testing.T) {
	mb := memAvailableMB()
	require.Greater(t, mb, 0, "memAvailableMB must be positive")
	// Any real Linux host has at least 128MB available; the fallback is 1024.
	require.Less(t, mb, 1024*1024, "memAvailableMB should be <1TB (sanity check)")
}
