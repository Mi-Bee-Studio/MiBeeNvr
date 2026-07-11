package recorder

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Mock DB for timelapse tests ---

type mockTimelapseDB struct {
	mu       sync.Mutex
	inserted []*model.Recording
}

func (m *mockTimelapseDB) InsertRecording(_ context.Context, r *model.Recording) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.inserted = append(m.inserted, r)
	return nil
}

func (m *mockTimelapseDB) InsertRecordingWithRetry(_ context.Context, r *model.Recording, _ int, _ time.Duration) error {
	return m.InsertRecording(context.Background(), r)
}

func (m *mockTimelapseDB) SetMergeStatus(_ context.Context, _ []string, _ string) error {
	return nil
}

func (m *mockTimelapseDB) recordings() []*model.Recording {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*model.Recording, len(m.inserted))
	copy(out, m.inserted)
	return out
}

// --- Mock segment store for timelapse tests ---

type mockTimelapseStore struct {
	dataDir string
	seq     int
}

func newMockTimelapseStore(dataDir string) *mockTimelapseStore {
	return &mockTimelapseStore{dataDir: dataDir}
}

func (s *mockTimelapseStore) CreateSegment(cameraID, _ string) (string, string, error) {
	s.seq++
	name := fmt.Sprintf("%s_%d_tmp", cameraID, s.seq)
	tempPath := filepath.Join(s.dataDir, name)
	finalPath := filepath.Join(s.dataDir, fmt.Sprintf("%s_%d", cameraID, s.seq))
	if err := os.MkdirAll(tempPath, 0o755); err != nil {
		return "", "", err
	}
	return tempPath, finalPath, nil
}

func (s *mockTimelapseStore) WriteFrame(_ string, _ []byte) (int, error) {
	return 0, nil // no-op — timelapse recorder writes frames directly via os.WriteFile
}

func (s *mockTimelapseStore) CloseSegment(tempPath, finalPath string) error {
	return os.Rename(tempPath, finalPath)
}

// --- MJPEG test server helper ---

func newTestMJPEGServer(t *testing.T, frames [][]byte) *httptest.Server {
	t.Helper()
	boundary := "frame"

	handler := func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "multipart/x-mixed-replace;boundary="+boundary)
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		// Fast-flush loop: continuously send frames with short sleeps
		// to keep the connection alive but deliver frames promptly.
		for {
			for _, frame := range frames {
				select {
				case <-r.Context().Done():
					return
				default:
				}
				fmt.Fprintf(w, "--%s\r\n", boundary)
				fmt.Fprintf(w, "Content-Type: image/jpeg\r\n")
				fmt.Fprintf(w, "Content-Length: %d\r\n", len(frame))
				fmt.Fprintf(w, "\r\n")
				w.Write(frame)
				fmt.Fprintf(w, "\r\n")
				flusher.Flush()
				time.Sleep(5 * time.Millisecond)
			}
		}
	}

	return httptest.NewServer(http.HandlerFunc(handler))
}

// makeTestJPEG creates a minimal valid JPEG in memory.
func makeTestJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	// Fill with non-zero color so it's a real image
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 128, A: 255})
		}
	}
	var buf bytes.Buffer
	err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 50})
	if err != nil {
		t.Fatalf("failed to encode test JPEG: %v", err)
	}
	return buf.Bytes()
}

// waitUntilFinished polls recorder status until stopped, with timeout.
func waitUntilFinished(t *testing.T, rec *TimelapseRecorder, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if rec.Status() == model.StatusStopped {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("timed out waiting for recorder to reach StatusStopped")
}

// --- Tests ---

func TestTimelapseStartStop(t *testing.T) {
	jpg := makeTestJPEG(t, 64, 64)
	srv := newTestMJPEGServer(t, [][]byte{jpg})
	defer srv.Close()

	store := newMockTimelapseStore(t.TempDir())
	rec := NewTimelapseRecorder(TimelapseRecorderConfig{
		CameraID:   "cam-startstop",
		URL:        srv.URL,
		Interval:   200 * time.Millisecond,
		SegmentDur: 5 * time.Minute,
	}, store)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	require.NoError(t, rec.Start(ctx))
	require.Equal(t, model.StatusRecording, rec.Status())

	// Double start should fail
	require.Error(t, rec.Start(ctx))

	// Let it run briefly
	time.Sleep(200 * time.Millisecond)

	require.NoError(t, rec.Stop())
	assert.Equal(t, model.StatusStopped, rec.Status())

	// Double stop should be safe
	require.NoError(t, rec.Stop())
}

func TestTimelapseFrameSampling(t *testing.T) {
	jpg := makeTestJPEG(t, 64, 64)
	srv := newTestMJPEGServer(t, [][]byte{jpg})
	defer srv.Close()

	dataDir := t.TempDir()
	store := newMockTimelapseStore(dataDir)
	db := &mockTimelapseDB{}

	rec := NewTimelapseRecorder(TimelapseRecorderConfig{
		CameraID:   "cam-sampling",
		URL:        srv.URL,
		Interval:   100 * time.Millisecond,
		SegmentDur: 10 * time.Second, // long enough to keep one segment
		DB:         db,
	}, store)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	require.NoError(t, rec.Start(ctx))

	// Server sends frames every ~5ms, but interval is 100ms.
	// Run for 800ms. Expected frames: ~8 (800/100), NOT ~26 (800/30).
	time.Sleep(800 * time.Millisecond)

	require.NoError(t, rec.Stop())
	waitUntilFinished(t, rec, 2*time.Second)

	// Count total JPEG files across all segment dirs
	var frameCount int
	filepath.Walk(dataDir, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasSuffix(path, ".jpg") {
			frameCount++
		}
		return nil
	})

	// With 100ms interval over 800ms, expect ~8 frames (allow 4–12 range).
	assert.Greater(t, frameCount, 3, "expected at least 4 frames with 100ms interval")
	assert.Less(t, frameCount, 14, "expected fewer than 14 frames — interval sampling should skip most frames")
}

func TestTimelapseSegmentRotation(t *testing.T) {
	jpg := makeTestJPEG(t, 64, 64)
	srv := newTestMJPEGServer(t, [][]byte{jpg})
	defer srv.Close()

	store := newMockTimelapseStore(t.TempDir())
	db := &mockTimelapseDB{}

	rec := NewTimelapseRecorder(TimelapseRecorderConfig{
		CameraID:   "cam-segment",
		URL:        srv.URL,
		Interval:   50 * time.Millisecond,
		SegmentDur: 300 * time.Millisecond,
		DB:         db,
	}, store)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	require.NoError(t, rec.Start(ctx))

	// Run for 700ms — should trigger >= 1 segment rotation (300ms each)
	time.Sleep(700 * time.Millisecond)

	require.NoError(t, rec.Stop())
	waitUntilFinished(t, rec, 3*time.Second)

	recs := db.recordings()
	timelapseCount := 0
	for _, r := range recs {
		if r.Format == model.FormatTimelapse {
			timelapseCount++
		}
	}
	assert.GreaterOrEqual(t, timelapseCount, 1, "expected at least 1 DB recording with timelapse format")
}

func TestTimelapseDBRegistration(t *testing.T) {
	jpg := makeTestJPEG(t, 64, 64)
	srv := newTestMJPEGServer(t, [][]byte{jpg})
	defer srv.Close()

	store := newMockTimelapseStore(t.TempDir())
	db := &mockTimelapseDB{}

	rec := NewTimelapseRecorder(TimelapseRecorderConfig{
		CameraID:   "cam-dbreg",
		URL:        srv.URL,
		Interval:   50 * time.Millisecond,
		SegmentDur: 300 * time.Millisecond,
		DB:         db,
	}, store)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	require.NoError(t, rec.Start(ctx))
	time.Sleep(700 * time.Millisecond)
	require.NoError(t, rec.Stop())
	waitUntilFinished(t, rec, 3*time.Second)

	recs := db.recordings()
	require.NotEmpty(t, recs, "expected at least one recording in DB")

	tlRec := recs[len(recs)-1] // take the last one
	assert.Equal(t, "cam-dbreg", tlRec.CameraID)
	assert.Equal(t, model.FormatTimelapse, tlRec.Format)
	assert.Greater(t, tlRec.FrameCount, 0, "FrameCount should be > 0")
	assert.True(t, tlRec.Duration > 0, "Duration should be > 0")

	// FilePath should be a directory that exists
	info, err := os.Stat(tlRec.FilePath)
	require.NoError(t, err, "recording FilePath should exist")
	assert.True(t, info.IsDir(), "recording FilePath should be a directory")
}

func TestTimelapseReconnect(t *testing.T) {
	jpg := makeTestJPEG(t, 64, 64)
	// Server that closes after first read (no looping)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "multipart/x-mixed-replace;boundary=frame")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		flusher.Flush()

		// Send exactly one frame then close
		fmt.Fprintf(w, "--frame\r\nContent-Type: image/jpeg\r\nContent-Length: %d\r\n\r\n", len(jpg))
		w.Write(jpg)
		fmt.Fprintf(w, "\r\n")
		flusher.Flush()
		// Connection closes when handler returns
	}))
	defer srv.Close()

	store := newMockTimelapseStore(t.TempDir())
	db := &mockTimelapseDB{}

	rec := NewTimelapseRecorder(TimelapseRecorderConfig{
		CameraID:   "cam-reconnect",
		URL:        srv.URL,
		Interval:   50 * time.Millisecond,
		SegmentDur: 1 * time.Second,
		DB:         db,
	}, store)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	require.NoError(t, rec.Start(ctx))

	// Let it run — server closes after first frame, recorder should reconnect (or stop on retry)
	// The key test: no panic, no hang
	done := make(chan struct{})
	go func() {
		defer close(done)
		time.Sleep(500 * time.Millisecond)
		rec.Stop()
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("recorder hung after server closed connection")
	}

	// Status should be stopped
	assert.Equal(t, model.StatusStopped, rec.Status())
}

func TestTimelapseInvalidFramesDropped(t *testing.T) {
	invalidFrames := [][]byte{
		[]byte("this-is-not-a-jpeg"),
		{0xFF, 0x01, 0x02}, // wrong magic after FF
		[]byte("also-not-jpeg-data"),
	}
	srv := newTestMJPEGServer(t, invalidFrames)
	defer srv.Close()

	store := newMockTimelapseStore(t.TempDir())
	db := &mockTimelapseDB{}

	rec := NewTimelapseRecorder(TimelapseRecorderConfig{
		CameraID:   "cam-invalid",
		URL:        srv.URL,
		Interval:   50 * time.Millisecond,
		SegmentDur: 1 * time.Second,
		DB:         db,
	}, store)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	require.NoError(t, rec.Start(ctx))
	time.Sleep(500 * time.Millisecond)
	require.NoError(t, rec.Stop())
	waitUntilFinished(t, rec, 2*time.Second)

	// No JPEG files should be written (all frames had invalid magic bytes)
	var frameCount int
	dataDir := store.dataDir
	filepath.Walk(dataDir, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasSuffix(path, ".jpg") {
			frameCount++
		}
		return nil
	})
	assert.Equal(t, 0, frameCount, "no JPEG files should be written when all frames are invalid")

	// No DB recordings either (segment was never closed with frames)
	recs := db.recordings()
	timelapseCount := 0
	for _, r := range recs {
		if r.Format == model.FormatTimelapse {
			timelapseCount++
		}
	}
	assert.Equal(t, 0, timelapseCount, "no timelapse recordings when all frames are invalid")
}

func TestTimelapseJPEGNaming(t *testing.T) {
	jpg := makeTestJPEG(t, 64, 64)
	srv := newTestMJPEGServer(t, [][]byte{jpg})
	defer srv.Close()

	store := newMockTimelapseStore(t.TempDir())
	db := &mockTimelapseDB{}

	rec := NewTimelapseRecorder(TimelapseRecorderConfig{
		CameraID:   "cam-naming",
		URL:        srv.URL,
		Interval:   50 * time.Millisecond,
		SegmentDur: 10 * time.Second, // long enough to keep one segment
		DB:         db,
	}, store)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	require.NoError(t, rec.Start(ctx))
	time.Sleep(500 * time.Millisecond)
	require.NoError(t, rec.Stop())
	waitUntilFinished(t, rec, 2*time.Second)

	// List all .jpg files in segment directories
	var jpgFiles []string
	dataDir := store.dataDir
	filepath.Walk(dataDir, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasSuffix(path, ".jpg") {
			jpgFiles = append(jpgFiles, filepath.Base(path))
		}
		return nil
	})

	require.NotEmpty(t, jpgFiles, "expected at least one JPEG file in segment dir")

	// Verify naming pattern: frame_NNNNNN.jpg
	for _, name := range jpgFiles {
		assert.Regexp(t, `^frame_\d{6}\.jpg$`, name, "file name should match frame_NNNNNN.jpg pattern")
	}
}
