package recorder

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"io/fs"
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

func TestHTTPJPEGSegmentDurNoRamCap(t *testing.T) {
	// #761: the RAM-based AVI duration cap is gone — the incremental muxer
	// streams to disk, so a long SegmentDur passes through unchanged. The RIFF
	// uint32 ceiling is guarded by the byte-based rotation (aviRotateBytes),
	// not by clamping duration.
	srv, handler := newMJPEGStreamServer()
	defer srv.Close()

	mgr := newTestManager(t)
	cfg := HTTPJPEGConfig{
		CameraID:   "cam-http-jpeg-cap",
		URL:        srv.URL,
		SegmentDur: 120 * time.Minute, // would have been capped to 30s/5m before #761
		AVI:        true,
		Width:      32,
		Height:     24,
	}
	rec := NewHTTPJPEGRecorder(cfg, mgr)
	require.Equal(t, cfg.SegmentDur, rec.cfg.SegmentDur,
		"SegmentDur must not be RAM-capped anymore (#761 incremental muxer)")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	require.NoError(t, rec.Start(ctx))

	time.Sleep(200 * time.Millisecond)
	handler.sendFrames(5, 20*time.Millisecond)
	time.Sleep(200 * time.Millisecond)

	require.NoError(t, rec.Stop())
}

func TestHTTPJPEGSegmentDurNoCap(t *testing.T) {
	// AVI=false with SegmentDur > 30s passes through (dir path never had a cap).
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

// TestAviRotateBytesDefault verifies the RIFF uint32 safety ceiling is set at
// 3.5 GiB (replaces the removed RAM-based aviSegmentDurCap — #761).
func TestAviRotateBytesDefault(t *testing.T) {
	require.Equal(t, int64(7)<<29, aviRotateBytes, "AVI rotation ceiling must stay under the 4GiB RIFF limit")
}

// vanishedTmpStore simulates a segment tmp that disappears mid-stream
// (rotation/cleanup race): WriteFrame fails with fs.ErrNotExist until told
// otherwise. Everything else succeeds.
type vanishedTmpStore struct {
	createCount int
	failFirst   int
	writes      []string
}

func (s *vanishedTmpStore) CreateSegment(cameraID, format string) (string, string, error) {
	s.createCount++
	return fmt.Sprintf("seg-%d.tmp", s.createCount), fmt.Sprintf("seg-%d.final", s.createCount), nil
}

func (s *vanishedTmpStore) WriteFrame(tempPath string, data []byte) (int, error) {
	if s.failFirst > 0 {
		s.failFirst--
		return 0, fmt.Errorf("storage: temp path not accessible: %w", fs.ErrNotExist)
	}
	s.writes = append(s.writes, tempPath)
	return len(data), nil
}

func (s *vanishedTmpStore) CloseSegment(tempPath, finalPath string) error { return nil }

// A vanished segment tmp must restart the segment in-loop: keep the HTTP
// stream, open a fresh segment on the next frame — NOT tear down and
// reconnect into the same dead path (#413; the reconnect loop previously
// hammered the missing tmp until storage health escalated to Failed).
func TestHTTPJPEGVanishedTempRestartsSegment(t *testing.T) {
	srv, handler := newMJPEGStreamServer()
	defer srv.Close()

	store := &vanishedTmpStore{failFirst: 1}
	rec := NewHTTPJPEGRecorder(HTTPJPEGConfig{
		CameraID:   "cam-vanished-tmp",
		URL:        srv.URL,
		SegmentDur: time.Hour,
	}, store)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	require.NoError(t, rec.Start(ctx))
	require.Equal(t, model.StatusRecording, rec.Status())

	time.Sleep(200 * time.Millisecond)
	handler.sendFrames(5, 20*time.Millisecond)
	time.Sleep(300 * time.Millisecond)

	require.NoError(t, rec.Stop())
	require.Equal(t, model.StatusStopped, rec.Status())

	require.GreaterOrEqual(t, store.createCount, 2, "recorder must open a fresh segment after the tmp vanished")
	require.NotEmpty(t, store.writes, "frames must land in the replacement segment")
	for _, w := range store.writes {
		require.Equal(t, "seg-2.tmp", w, "frames must go to the replacement segment only")
	}
}
