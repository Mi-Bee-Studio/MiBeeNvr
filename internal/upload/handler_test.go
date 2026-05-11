package upload

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)

// setupTestEnv creates temp storage dir, DB, and Handler for tests.
// Returns handler, cleanup function, and temp dir path.
func setupTestEnv(t *testing.T) (*Handler, func()) {
	t.Helper()

	tmpDir := t.TempDir()
	storageDir := filepath.Join(tmpDir, "storage")
	dbPath := filepath.Join(tmpDir, "test.db")

	mgr, err := storage.NewManager(storageDir)
	require.NoError(t, err)

	db, err := storage.New(dbPath)
	require.NoError(t, err)
	require.NoError(t, db.Init(context.Background()))

	// Insert test cameras via DB
	require.NoError(t, db.UpsertCamera(context.Background(), "cam1", "Test Camera 1", "http_jpeg", "http://example.com/cam1.jpg", "", "", true, "", ""))
	require.NoError(t, db.UpsertCamera(context.Background(), "cam2", "Test Camera 2", "rtsp_h264", "rtsp://example.com/cam2", "", "", true, "", ""))

	h := NewHandler(mgr, db, 10*1024*1024) // 10MB max

	cleanup := func() {
		db.Close()
		os.RemoveAll(tmpDir)
	}

	return h, cleanup
}

// newRouter creates a chi router with the handler's routes registered.
func newRouter(h *Handler) *chi.Mux {
	r := chi.NewRouter()
	h.RegisterRoutes(r)
	return r
}

// minimal valid JPEG bytes (SOI + EOI markers).
var testJPEG = []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 0x4A, 0x46, 0x49, 0x46, 0x00, 0x01, 0x01, 0x00, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00, 0xFF, 0xD9}

func TestUploadJPEG(t *testing.T) {
	h, cleanup := setupTestEnv(t)
	defer cleanup()

	r := newRouter(h)

	body := bytes.NewReader(testJPEG)
	req := httptest.NewRequest(http.MethodPost, "/api/upload/cam1", body)
	req.Header.Set("Content-Type", "image/jpeg")

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusCreated, rec.Code)

	var resp uploadResponse
	err := json.Unmarshal(rec.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.Equal(t, "cam1", resp.CameraID)
	assert.Equal(t, "mjpeg", resp.Format)
	assert.Equal(t, 1, resp.FrameCount)
	assert.Equal(t, int64(len(testJPEG)), resp.FileSize)
	assert.NotEmpty(t, resp.ID)
	assert.NotEmpty(t, resp.FilePath)
}

func TestUploadUnknownCamera(t *testing.T) {
	h, cleanup := setupTestEnv(t)
	defer cleanup()

	r := newRouter(h)

	body := bytes.NewReader(testJPEG)
	req := httptest.NewRequest(http.MethodPost, "/api/upload/nonexistent", body)
	req.Header.Set("Content-Type", "image/jpeg")

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)

	var resp errorResponse
	err := json.Unmarshal(rec.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.Contains(t, resp.Error, "nonexistent")
}

func TestUploadOversized(t *testing.T) {
	tmpDir := t.TempDir()
	storageDir := filepath.Join(tmpDir, "storage")
	dbPath := filepath.Join(tmpDir, "test.db")

	mgr, err := storage.NewManager(storageDir)
	require.NoError(t, err)

	db, err := storage.New(dbPath)
	require.NoError(t, err)
	require.NoError(t, db.Init(context.Background()))

	require.NoError(t, db.UpsertCamera(context.Background(), "cam1", "Cam", "http_jpeg", "http://x", "", "", true, "", ""))

	// 16 bytes max upload size
	h := NewHandler(mgr, db, 16)

	r := newRouter(h)

	// 20 bytes of data exceeds 16 byte limit
	largeData := bytes.Repeat([]byte{0xAA}, 20)
	body := bytes.NewReader(largeData)
	req := httptest.NewRequest(http.MethodPost, "/api/upload/cam1", body)
	req.Header.Set("Content-Type", "image/jpeg")

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)

	var resp errorResponse
	err = json.Unmarshal(rec.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.Contains(t, resp.Error, "maximum size")
}

func TestUploadBadContentType(t *testing.T) {
	h, cleanup := setupTestEnv(t)
	defer cleanup()

	r := newRouter(h)

	body := bytes.NewReader([]byte("not a jpeg"))
	req := httptest.NewRequest(http.MethodPost, "/api/upload/cam1", body)
	req.Header.Set("Content-Type", "text/plain")

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)

	var resp errorResponse
	err := json.Unmarshal(rec.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.Contains(t, resp.Error, "text/plain")
}

func TestUploadVideo(t *testing.T) {
	h, cleanup := setupTestEnv(t)
	defer cleanup()

	r := newRouter(h)

	videoData := []byte("fake mp4 data for testing")
	body := bytes.NewReader(videoData)
	req := httptest.NewRequest(http.MethodPost, "/api/upload/cam2/video", body)
	req.Header.Set("Content-Type", "video/mp4")

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusCreated, rec.Code)

	var resp uploadResponse
	err := json.Unmarshal(rec.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.Equal(t, "cam2", resp.CameraID)
	assert.Equal(t, "h264", resp.Format)
	assert.Equal(t, 1, resp.FrameCount)
	assert.Equal(t, int64(len(videoData)), resp.FileSize)
	assert.NotEmpty(t, resp.ID)
	assert.NotEmpty(t, resp.FilePath)
}

func TestUploadBatch(t *testing.T) {
	h, cleanup := setupTestEnv(t)
	defer cleanup()

	r := newRouter(h)

	// Build multipart form with 2 JPEG frames
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	part1, err := writer.CreateFormFile("frames", "frame1.jpg")
	require.NoError(t, err)
	_, err = part1.Write(testJPEG)
	require.NoError(t, err)

	part2, err := writer.CreateFormFile("frames", "frame2.jpg")
	require.NoError(t, err)
	_, err = part2.Write(testJPEG)
	require.NoError(t, err)

	require.NoError(t, writer.Close())

	req := httptest.NewRequest(http.MethodPost, "/api/upload/cam1/batch", &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusCreated, rec.Code)

	var resp uploadResponse
	err = json.Unmarshal(rec.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.Equal(t, "cam1", resp.CameraID)
	assert.Equal(t, "mjpeg", resp.Format)
	assert.Equal(t, 2, resp.FrameCount)
	assert.Equal(t, int64(len(testJPEG)*2), resp.FileSize)
	assert.NotEmpty(t, resp.ID)
}

func TestUploadVideoBadContentType(t *testing.T) {
	h, cleanup := setupTestEnv(t)
	defer cleanup()

	r := newRouter(h)

	body := bytes.NewReader([]byte("not a video"))
	req := httptest.NewRequest(http.MethodPost, "/api/upload/cam2/video", body)
	req.Header.Set("Content-Type", "text/html")

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)

	var resp errorResponse
	err := json.Unmarshal(rec.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.Contains(t, resp.Error, "text/html")
}

// TestUploadJPEGWritesFile verifies that a JPEG upload actually creates a file on disk.
func TestUploadJPEGWritesFile(t *testing.T) {
	h, cleanup := setupTestEnv(t)
	defer cleanup()

	r := newRouter(h)

	body := bytes.NewReader(testJPEG)
	req := httptest.NewRequest(http.MethodPost, "/api/upload/cam1", body)
	req.Header.Set("Content-Type", "image/jpeg")

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code)

	var resp uploadResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

	// Verify the file was created
	info, err := os.Stat(resp.FilePath)
	require.NoError(t, err)
	assert.True(t, info.IsDir(), "MJPEG segment should be a directory")
}

// TestUploadVideoWritesFile verifies that a video upload creates a file on disk.
func TestUploadVideoWritesFile(t *testing.T) {
	h, cleanup := setupTestEnv(t)
	defer cleanup()

	r := newRouter(h)

	videoData := []byte("fake video content")
	body := bytes.NewReader(videoData)
	req := httptest.NewRequest(http.MethodPost, "/api/upload/cam2/video", body)
	req.Header.Set("Content-Type", "video/mp4")

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code)

	var resp uploadResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

	// Verify the file was created
	info, err := os.Stat(resp.FilePath)
	require.NoError(t, err)
	assert.False(t, info.IsDir(), "H264 segment should be a file")
	assert.Equal(t, int64(len(videoData)), info.Size())
}
