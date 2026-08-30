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
	require.NoError(t, db.UpsertCamera(context.Background(), "cam1", "Test Camera 1", "http", "jpeg", "http://example.com/cam1.jpg", "", "", "", "", "", ""))
	require.NoError(t, db.UpsertCamera(context.Background(), "cam2", "Test Camera 2", "rtsp", "h264", "rtsp://example.com/cam2", "", "", "", "", "", ""))

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
	t.Parallel()
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
	t.Parallel()
	h, cleanup := setupTestEnv(t)
	defer cleanup()

	r := newRouter(h)

	body := bytes.NewReader(testJPEG)
	req := httptest.NewRequest(http.MethodPost, "/api/upload/nonexistent", body)
	req.Header.Set("Content-Type", "image/jpeg")

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)

	var resp map[string]string
	err := json.Unmarshal(rec.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.Contains(t, resp["error"], "nonexistent")
}

func TestUploadOversized(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	storageDir := filepath.Join(tmpDir, "storage")
	dbPath := filepath.Join(tmpDir, "test.db")

	mgr, err := storage.NewManager(storageDir)
	require.NoError(t, err)

	db, err := storage.New(dbPath)
	require.NoError(t, err)
	require.NoError(t, db.Init(context.Background()))

	require.NoError(t, db.UpsertCamera(context.Background(), "cam1", "Cam", "http", "jpeg", "http://x", "", "", "", "", "", ""))

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

	var resp map[string]string
	err = json.Unmarshal(rec.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.Contains(t, resp["error"], "maximum size")
}

func TestUploadBadContentType(t *testing.T) {
	t.Parallel()
	h, cleanup := setupTestEnv(t)
	defer cleanup()

	r := newRouter(h)

	body := bytes.NewReader([]byte("not a jpeg"))
	req := httptest.NewRequest(http.MethodPost, "/api/upload/cam1", body)
	req.Header.Set("Content-Type", "text/plain")

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)

	var resp map[string]string
	err := json.Unmarshal(rec.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.Contains(t, resp["error"], "text/plain")
}

func TestUploadVideo(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	h, cleanup := setupTestEnv(t)
	defer cleanup()

	r := newRouter(h)

	body := bytes.NewReader([]byte("not a video"))
	req := httptest.NewRequest(http.MethodPost, "/api/upload/cam2/video", body)
	req.Header.Set("Content-Type", "text/html")

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)

	var resp map[string]string
	err := json.Unmarshal(rec.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.Contains(t, resp["error"], "text/html")
}

// TestUploadJPEGWritesFile verifies that a JPEG upload actually creates a file on disk.
func TestUploadJPEGWritesFile(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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

// errReader fails every Read — exercises the readBody error path.
type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, assert.AnError }

// TestUploadBatch_BadContentType: non-multipart content type is rejected.
func TestUploadBatch_BadContentType(t *testing.T) {
	t.Parallel()
	h, cleanup := setupTestEnv(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/api/upload/cam1/batch", bytes.NewReader(testJPEG))
	req.Header.Set("Content-Type", "image/jpeg")
	rec := httptest.NewRecorder()
	newRouter(h).ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "multipart/form-data")
}

// TestUploadBatch_GarbageMultipart: multipart content type with a body that
// does not parse as multipart fails ParseMultipartForm.
func TestUploadBatch_GarbageMultipart(t *testing.T) {
	t.Parallel()
	h, cleanup := setupTestEnv(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/api/upload/cam1/batch", bytes.NewReader([]byte("definitely not multipart")))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=x")
	rec := httptest.NewRecorder()
	newRouter(h).ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "failed to parse multipart form")
}

// TestUploadBatch_NoFramesField: a valid multipart form without any "frames"
// parts is rejected.
func TestUploadBatch_NoFramesField(t *testing.T) {
	t.Parallel()
	h, cleanup := setupTestEnv(t)
	defer cleanup()

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile("other", "note.txt")
	require.NoError(t, err)
	_, err = fw.Write([]byte("hi"))
	require.NoError(t, err)
	require.NoError(t, w.Close())

	req := httptest.NewRequest(http.MethodPost, "/api/upload/cam1/batch", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rec := httptest.NewRecorder()
	newRouter(h).ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "no frames found")
}

// TestUploadVideo_Oversized: a video body past the limit is rejected 413.
func TestUploadVideo_Oversized(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	mgr, err := storage.NewManager(filepath.Join(tmpDir, "storage"))
	require.NoError(t, err)
	db, err := storage.New(filepath.Join(tmpDir, "test.db"))
	require.NoError(t, err)
	require.NoError(t, db.Init(context.Background()))
	require.NoError(t, db.UpsertCamera(context.Background(), "cam2", "Cam", "rtsp", "h264", "rtsp://x", "", "", "", "", "", ""))
	t.Cleanup(func() { _ = db.Close() })

	h := NewHandler(mgr, db, 8)

	req := httptest.NewRequest(http.MethodPost, "/api/upload/cam2/video", bytes.NewReader([]byte("0123456789ABCDEF")))
	req.Header.Set("Content-Type", "video/mp4")
	rec := httptest.NewRecorder()
	newRouter(h).ServeHTTP(rec, req)

	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	assert.Contains(t, rec.Body.String(), "maximum size")
}

// TestUploadBodyReadError: a failing request body surfaces as 500 on both
// single-frame and video endpoints.
func TestUploadBodyReadError(t *testing.T) {
	t.Parallel()
	h, cleanup := setupTestEnv(t)
	defer cleanup()
	r := newRouter(h)

	req := httptest.NewRequest(http.MethodPost, "/api/upload/cam1", errReader{})
	req.Header.Set("Content-Type", "image/jpeg")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)

	req2 := httptest.NewRequest(http.MethodPost, "/api/upload/cam2/video", errReader{})
	req2.Header.Set("Content-Type", "video/mp4")
	rec2 := httptest.NewRecorder()
	r.ServeHTTP(rec2, req2)
	assert.Equal(t, http.StatusInternalServerError, rec2.Code)
}

// TestUploadStorageFailure: a storage manager whose root is a regular file
// cannot create segments — every upload endpoint answers 500.
func TestUploadStorageFailure(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	// A regular file planted at <root>/cam1 makes CreateSegment's camera-dir
	// mkdir fail — every endpoint must answer 500 without touching the DB row.
	mgr, err := storage.NewManager(filepath.Join(tmpDir, "storage"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "storage", "cam1"), []byte("x"), 0o644))

	db, err := storage.New(filepath.Join(tmpDir, "test.db"))
	require.NoError(t, err)
	require.NoError(t, db.Init(context.Background()))
	require.NoError(t, db.UpsertCamera(context.Background(), "cam1", "Cam", "http", "jpeg", "http://x", "", "", "", "", "", ""))
	t.Cleanup(func() { _ = db.Close() })

	h := NewHandler(mgr, db, 1024*1024)
	r := newRouter(h)

	req := httptest.NewRequest(http.MethodPost, "/api/upload/cam1", bytes.NewReader(testJPEG))
	req.Header.Set("Content-Type", "image/jpeg")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile("frames", "f.jpg")
	require.NoError(t, err)
	_, err = fw.Write(testJPEG)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	req2 := httptest.NewRequest(http.MethodPost, "/api/upload/cam1/batch", &buf)
	req2.Header.Set("Content-Type", w.FormDataContentType())
	rec2 := httptest.NewRecorder()
	r.ServeHTTP(rec2, req2)
	assert.Equal(t, http.StatusInternalServerError, rec2.Code)

	req3 := httptest.NewRequest(http.MethodPost, "/api/upload/cam1/video", bytes.NewReader([]byte("mp4")))
	req3.Header.Set("Content-Type", "video/mp4")
	rec3 := httptest.NewRecorder()
	r.ServeHTTP(rec3, req3)
	assert.Equal(t, http.StatusInternalServerError, rec3.Code)
}
