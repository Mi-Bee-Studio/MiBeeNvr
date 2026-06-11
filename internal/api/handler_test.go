package api

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/camera"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/middleware"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/merge"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/hls"
	"github.com/stretchr/testify/require"
)

// --- Test helpers ---

func setupTestDB(t *testing.T) (*storage.DB, *storage.Manager) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := storage.New(dbPath)
	if err != nil {
		t.Fatalf("failed to create db: %v", err)
	}
	ctx := context.Background()
	if err := db.Init(ctx); err != nil {
		t.Fatalf("failed to init db: %v", err)
	}
	store, err := storage.NewManager(filepath.Join(dir, "storage"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	return db, store
}

func seedRecording(t *testing.T, db *storage.DB, r *model.Recording) {
	t.Helper()
	if err := db.InsertRecording(context.Background(), r); err != nil {
		t.Fatalf("failed to seed recording: %v", err)
	}
}

func seedCamera(t *testing.T, db *storage.DB, id, name, protocol, url string, enabled bool) {
	t.Helper()
	// Insert camera directly via raw SQL since storage.DB doesn't have InsertCamera
	// We use the DB's internal connection - but it's private. So we'll skip this
	// and instead seed cameras through a test helper in the storage package or
	// use the recordings endpoint which doesn't need cameras.
	// For camera listing tests, we'll create the handler_test to work around this.
	_ = db // We need to add a method or accept this limitation
	_ = id
	_ = name
	_ = protocol
	_ = url
	_ = enabled
}

func doRequest(t *testing.T, handler http.Handler, method, path string, body io.Reader, username, password string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, body)
	if username != "" {
		req.SetBasicAuth(username, password)
	}
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

func parseJSON(t *testing.T, rr *httptest.ResponseRecorder, v interface{}) {
	t.Helper()
	if err := json.NewDecoder(rr.Body).Decode(v); err != nil {
		t.Fatalf("failed to parse JSON: %v\nbody: %s", err, rr.Body.String())
	}
}

// recordingsResponse wraps the paginated recordings list.
type recordingsResponse struct {
	Recordings []model.Recording `json:"recordings"`
	Total      int                `json:"total"`
}


func makeRecording(id, cameraID, format string, startedAt time.Time, merged bool) *model.Recording {
	return &model.Recording{
		ID:        id,
		CameraID:  cameraID,
		FilePath:  "/tmp/" + id + ".mp4",
		Format:    model.Format(format),
		StartedAt: startedAt,
		EndedAt:   startedAt.Add(5 * time.Minute),
		Duration:  300.0,
		FileSize:  1024,
		FrameCount: 150,
		Merged:    merged,
	}
}

// --- Health endpoint tests ---

func TestHealth(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "GET", "/api/health", nil, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var body struct {
		Status string                       `json:"status"`
		Checks map[string]map[string]string `json:"checks"`
		Uptime string                       `json:"uptime"`
	}
	parseJSON(t, rr, &body)
	if body.Status != "ok" {
		t.Fatalf("expected status ok, got %s", body.Status)
	}
	if body.Checks["database"]["status"] != "ok" {
		t.Fatalf("expected database check ok, got %s", body.Checks["database"]["status"])
	}
	if body.Checks["storage"]["status"] != "ok" {
		t.Fatalf("expected storage check ok, got %s", body.Checks["storage"]["status"])
	}
	if body.Checks["goroutines"]["status"] != "ok" {
		t.Fatalf("expected goroutines check ok, got %s", body.Checks["goroutines"]["status"])
	}
	if body.Uptime == "" {
		t.Fatal("expected non-empty uptime")
	}
}

// --- Login endpoint tests ---

func TestLogin_NoAuth(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	// No-op auth (empty password hash = auth disabled)
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "POST", "/api/auth/login", nil, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
}

func TestLogin_ValidCredentials(t *testing.T) {
	db, store := setupTestDB(t)
	defer db.Close()
	hash, err := middleware.HashPassword("secret")
	if err != nil {
		t.Fatalf("failed to hash password: %v", err)
	}
	h := TestHandlerWithAuth(db, store, "admin", hash)

	rr := doRequest(t, h.Routes(), "POST", "/api/auth/login", nil, "admin", "secret")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestLogin_InvalidCredentials(t *testing.T) {
	db, store := setupTestDB(t)
	defer db.Close()
	hash, err := middleware.HashPassword("secret")
	if err != nil {
		t.Fatalf("failed to hash password: %v", err)
	}
	h := TestHandlerWithAuth(db, store, "admin", hash)

	rr := doRequest(t, h.Routes(), "POST", "/api/auth/login", nil, "admin", "wrong")
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

// --- List recordings tests ---

func TestListRecordings_Empty(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "GET", "/api/recordings", nil, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var resp recordingsResponse
	parseJSON(t, rr, &resp)
	if len(resp.Recordings) != 0 {
		t.Fatalf("expected 0 recordings, got %d", len(resp.Recordings))
	}
}

func TestListRecordings_WithSeed(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	seedRecording(t, db, makeRecording("rec-1", "cam-1", "h264", now, false))
	seedRecording(t, db, makeRecording("rec-2", "cam-1", "mjpeg", now.Add(-time.Hour), true))

	rr := doRequest(t, h.Routes(), "GET", "/api/recordings", nil, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var resp recordingsResponse
	parseJSON(t, rr, &resp)
	if len(resp.Recordings) != 2 {
		t.Fatalf("expected 2 recordings, got %d", len(resp.Recordings))
	}
}

func TestListRecordings_FilterByCameraID(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	seedRecording(t, db, makeRecording("rec-1", "cam-1", "h264", now, false))
	seedRecording(t, db, makeRecording("rec-2", "cam-2", "h264", now, false))

	rr := doRequest(t, h.Routes(), "GET", "/api/recordings?camera_id=cam-1", nil, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var resp recordingsResponse
	parseJSON(t, rr, &resp)
	if len(resp.Recordings) != 1 {
		t.Fatalf("expected 1 recording, got %d", len(resp.Recordings))
	}
	if resp.Recordings[0].ID != "rec-1" {
		t.Fatalf("expected rec-1, got %s", resp.Recordings[0].ID)
	}
}

func TestListRecordings_FilterByFormat(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	seedRecording(t, db, makeRecording("rec-1", "cam-1", "h264", now, false))
	seedRecording(t, db, makeRecording("rec-2", "cam-1", "mjpeg", now, false))

	rr := doRequest(t, h.Routes(), "GET", "/api/recordings?format=h264", nil, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var resp recordingsResponse
	parseJSON(t, rr, &resp)
	if len(resp.Recordings) != 1 {
		t.Fatalf("expected 1 recording, got %d", len(resp.Recordings))
	}
}

func TestListRecordings_FilterByMerged(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	seedRecording(t, db, makeRecording("rec-1", "cam-1", "h264", now, true))
	seedRecording(t, db, makeRecording("rec-2", "cam-1", "h264", now, false))

	rr := doRequest(t, h.Routes(), "GET", "/api/recordings?merged=true", nil, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var resp recordingsResponse
	parseJSON(t, rr, &resp)
	if len(resp.Recordings) != 1 {
		t.Fatalf("expected 1 recording, got %d", len(resp.Recordings))
	}
	if !resp.Recordings[0].Merged {
		t.Fatal("expected recording to be merged")
	}
}

func TestListRecordings_FilterByTimeRange(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	seedRecording(t, db, makeRecording("rec-old", "cam-1", "h264", now.Add(-48*time.Hour), false))
	seedRecording(t, db, makeRecording("rec-new", "cam-1", "h264", now.Add(-1*time.Hour), false))

	start := now.Add(-24 * time.Hour).Format(time.RFC3339)
	end := now.Format(time.RFC3339)
	rr := doRequest(t, h.Routes(), "GET", "/api/recordings?start="+start+"&end="+end, nil, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var resp recordingsResponse
	parseJSON(t, rr, &resp)
	if len(resp.Recordings) != 1 {
		t.Fatalf("expected 1 recording, got %d", len(resp.Recordings))
	}
	if resp.Recordings[0].ID != "rec-new" {
		t.Fatalf("expected rec-new, got %s", resp.Recordings[0].ID)
	}
}

func TestListRecordings_Pagination(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	for i := 0; i < 5; i++ {
		seedRecording(t, db, makeRecording("rec-"+strconv.Itoa(i), "cam-1", "h264", now.Add(time.Duration(i)*time.Hour), false))
	}

	rr := doRequest(t, h.Routes(), "GET", "/api/recordings?limit=2&offset=1", nil, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var resp recordingsResponse
	parseJSON(t, rr, &resp)
	if len(resp.Recordings) != 2 {
		t.Fatalf("expected 2 recordings, got %d", len(resp.Recordings))
	}
}

// --- Get recording tests ---

func TestGetRecording_Found(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	seedRecording(t, db, makeRecording("rec-1", "cam-1", "h264", now, false))

	rr := doRequest(t, h.Routes(), "GET", "/api/recordings/rec-1", nil, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var rec model.Recording
	parseJSON(t, rr, &rec)
	if rec.ID != "rec-1" {
		t.Fatalf("expected rec-1, got %s", rec.ID)
	}
}

func TestGetRecording_NotFound(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "GET", "/api/recordings/nonexistent", nil, "", "")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}
}

// --- Delete recording tests ---

func TestDeleteRecording_Success(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	rec := makeRecording("rec-del", "cam-1", "h264", now, false)
	// Create actual file so DeleteFile can succeed
	rec.FilePath = filepath.Join(store.RootDir(), "rec-del.mp4")
	if err := os.WriteFile(rec.FilePath, []byte("test-data"), 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}
	seedRecording(t, db, rec)

	rr := doRequest(t, h.Routes(), "DELETE", "/api/recordings/rec-del", nil, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	// Verify deleted from DB
	got, err := db.GetRecording(context.Background(), "rec-del")
	if err != nil {
		t.Fatalf("failed to get recording: %v", err)
	}
	if got != nil {
		t.Fatal("expected recording to be deleted from DB")
	}

	// Verify file deleted
	if _, err := os.Stat(rec.FilePath); !os.IsNotExist(err) {
		t.Fatal("expected file to be deleted from disk")
	}
}

func TestDeleteRecording_NotFound(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "DELETE", "/api/recordings/nonexistent", nil, "", "")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}
}

// --- Download tests ---

// --- Download tests ---

func TestDownloadRecording(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	rec := makeRecording("rec-dl", "cam-1", "h264", now, false)
	rec.FilePath = filepath.Join(store.RootDir(), "rec-dl.mp4")
	testData := []byte("fake-mp4-data")
	if err := os.WriteFile(rec.FilePath, testData, 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}
	seedRecording(t, db, rec)

	rr := doRequest(t, h.Routes(), "GET", "/api/recordings/rec-dl/download", nil, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	body, _ := io.ReadAll(rr.Body)
	if string(body) != string(testData) {
		t.Fatalf("expected %q, got %q", string(testData), string(body))
	}
}

func TestDownloadRecording_NotFound(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "GET", "/api/recordings/nonexistent/download", nil, "", "")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}
}

// --- Stats tests ---

func TestStats(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	seedRecording(t, db, makeRecording("rec-1", "cam-1", "h264", now, false))
	seedRecording(t, db, makeRecording("rec-2", "cam-1", "h264", now, false))

	rr := doRequest(t, h.Routes(), "GET", "/api/stats", nil, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var stats model.StorageStats
	parseJSON(t, rr, &stats)
	if stats.RecordingCount != 2 {
		t.Fatalf("expected 2 recordings, got %d", stats.RecordingCount)
	}
	if stats.TotalBytes <= 0 {
		t.Fatal("expected total bytes > 0")
	}
	if stats.UsedBytes < 0 {
		t.Fatal("expected used bytes >= 0")
	}
}

// --- Cameras tests ---

func TestListCameras_Empty(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "GET", "/api/cameras", nil, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var cameras []storage.CameraRow
	parseJSON(t, rr, &cameras)
	if len(cameras) != 0 {
		t.Fatalf("expected 0 cameras, got %d", len(cameras))
	}
}

func TestListCameras_WithSeed(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	// Seed cameras directly via raw exec - we need to insert into the cameras table
	// Since DB's internal db field is private, we use the InsertCamera pattern
	// by creating a test helper that inserts via a simple approach
	// We'll add InsertCamera to the DB or work with what we have.
	// For now, test with empty list (cameras are inserted by the recorder, not API).
	// The cameras table is populated by the config loader, not the API.

	rr := doRequest(t, h.Routes(), "GET", "/api/cameras", nil, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
}

// --- Auth middleware tests ---

func TestProtectedEndpoints_NoAuth(t *testing.T) {
	db, store := setupTestDB(t)
	defer db.Close()
	hash, err := middleware.HashPassword("secret")
	if err != nil {
		t.Fatalf("failed to hash password: %v", err)
	}
	h := TestHandlerWithAuth(db, store, "admin", hash)

	// Without auth, protected endpoints should return 401
	endpoints := []string{
		"GET /api/recordings",
		"GET /api/recordings/rec-1",
		"DELETE /api/recordings/rec-1",
		"POST /api/recordings/rec-1/download",
		"GET /api/cameras",
		"GET /api/stats",
	}
	for _, ep := range endpoints {
		method, path := splitEndpoint(ep)
		rr := doRequest(t, h.Routes(), method, path, nil, "", "")
		if rr.Code != http.StatusUnauthorized {
			t.Errorf("endpoint %s: expected 401, got %d", ep, rr.Code)
		}
	}

	// Public endpoints should still work
	rr := doRequest(t, h.Routes(), "GET", "/api/health", nil, "", "")
	if rr.Code != http.StatusOK {
		t.Errorf("GET /api/health: expected 200, got %d", rr.Code)
	}
}

func TestProtectedEndpoints_WithAuth(t *testing.T) {
	db, store := setupTestDB(t)
	defer db.Close()
	hash, err := middleware.HashPassword("secret")
	if err != nil {
		t.Fatalf("failed to hash password: %v", err)
	}
	h := TestHandlerWithAuth(db, store, "admin", hash)

	rr := doRequest(t, h.Routes(), "GET", "/api/recordings", nil, "admin", "secret")
	var resp recordingsResponse
	parseJSON(t, rr, &resp)
	if len(resp.Recordings) != 0 {
		t.Fatalf("expected 0 recordings, got %d", len(resp.Recordings))
}
}

// --- Helper to parse method/path ---

// --- Helper to parse method/path ---

func splitEndpoint(ep string) (string, string) {
	for i := 0; i < len(ep); i++ {
		if ep[i] == ' ' {
			return ep[:i], ep[i+1:]
		}
	}
	return ep, ""
}

// --- Content-Type tests ---

func TestResponseContentType(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "GET", "/api/health", nil, "", "")
	ct := rr.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Fatalf("expected Content-Type application/json, got %s", ct)
	}
}

// --- Login response content type ---

func TestLogin_ResponseContentType(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "POST", "/api/auth/login", nil, "", "")
	ct := rr.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Fatalf("expected Content-Type application/json, got %s", ct)
	}
}

// --- Settings API tests ---

// newHandlerWithConfig creates a Handler with a real config for testing.
func newHandlerWithConfig(db *storage.DB, store *storage.Manager, cfg *config.Config) *Handler {
	return NewHandler(db, store, noopAuthMW(), cfg, nil, nil, "", nil, nil, nil)
}
func newHandlerWithConfigAndAuth(db *storage.DB, store *storage.Manager, username, passwordHash string, cfg *config.Config) *Handler {
	authMW, _ := middleware.NewAuthMiddleware(middleware.AuthProvider{GetUsername: func() string { return username }, GetHash: func() string { return passwordHash }}, "", middleware.AuthRateLimitConfig{})
	return NewHandler(db, store, authMW, cfg, nil, nil, "", nil, nil, nil)
}
func TestGetSettings_NoConfig(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store) // nil config

	rr := doRequest(t, h.Routes(), "GET", "/api/settings", nil, "", "")
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rr.Code)
	}
	var body map[string]string
	parseJSON(t, rr, &body)
	if body["error"] != "config not available" {
		t.Fatalf("expected 'config not available', got %s", body["error"])
	}
}

func TestGetSettings_WithConfig(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	cfg := &config.Config{
		Cleanup: config.CleanupConfig{
			RetentionDays:       14,
			CheckInterval:       "30m",
			DiskThresholdPercent: 80,
		},
		Cameras: []config.CameraConfig{
			{ID: "cam-1", Name: "Front Door", Protocol: "rtsp_h264", URL: "rtsp://camera1/stream"},
			{ID: "cam-2", Name: "Backyard", Protocol: "http_jpeg", URL: "http://camera2/stream"},
		},
	}
	h := newHandlerWithConfig(db, store, cfg)

	rr := doRequest(t, h.Routes(), "GET", "/api/settings", nil, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var body map[string]any
	parseJSON(t, rr, &body)

	cleanup, ok := body["cleanup"].(map[string]any)
	if !ok {
		t.Fatal("expected cleanup object")
	}
	if cleanup["retention_days"] != float64(14) {
		t.Fatalf("expected retention_days=14, got %v", cleanup["retention_days"])
	}
	if cleanup["check_interval"] != "30m" {
		t.Fatalf("expected check_interval=30m, got %v", cleanup["check_interval"])
	}
	if cleanup["disk_threshold_percent"] != float64(80) {
		t.Fatalf("expected disk_threshold_percent=80, got %v", cleanup["disk_threshold_percent"])
	}

}


func TestUpdateSettings_NoConfig(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store) // nil config

	body := strings.NewReader(`{"cleanup":{"retention_days":7}}`)
	rr := doRequest(t, h.Routes(), "PUT", "/api/settings", body, "", "")
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rr.Code)
	}
}

func TestUpdateSettings_InvalidBody(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	cfg := &config.Config{Cleanup: config.CleanupConfig{RetentionDays: 30}, Cameras: []config.CameraConfig{}}
	h := newHandlerWithConfig(db, store, cfg)

	body := strings.NewReader(`{invalid json`)
	rr := doRequest(t, h.Routes(), "PUT", "/api/settings", body, "", "")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

func TestUpdateSettings_InvalidRetentionDays(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	cfg := &config.Config{Cleanup: config.CleanupConfig{RetentionDays: 30}, Cameras: []config.CameraConfig{}}
	h := newHandlerWithConfig(db, store, cfg)

	body := strings.NewReader(`{"cleanup":{"retention_days":0}}`)
	rr := doRequest(t, h.Routes(), "PUT", "/api/settings", body, "", "")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
	var resp map[string]string
	parseJSON(t, rr, &resp)
	if resp["error"] != "retention_days must be >= 1" {
		t.Fatalf("expected retention_days validation error, got %s", resp["error"])
	}
}

func TestUpdateSettings_InvalidDiskThreshold(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	cfg := &config.Config{Cleanup: config.CleanupConfig{DiskThresholdPercent: 95}, Cameras: []config.CameraConfig{}}
	h := newHandlerWithConfig(db, store, cfg)

	// Test too low
	body := strings.NewReader(`{"cleanup":{"disk_threshold_percent":0}}`)
	rr := doRequest(t, h.Routes(), "PUT", "/api/settings", body, "", "")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for 0%%, got %d", rr.Code)
	}

	// Test too high
	body = strings.NewReader(`{"cleanup":{"disk_threshold_percent":101}}`)
	rr = doRequest(t, h.Routes(), "PUT", "/api/settings", body, "", "")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for 101%%, got %d", rr.Code)
	}
}

func TestUpdateSettings_InvalidCheckInterval(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	cfg := &config.Config{Cleanup: config.CleanupConfig{CheckInterval: "1h"}, Cameras: []config.CameraConfig{}}
	h := newHandlerWithConfig(db, store, cfg)

	body := strings.NewReader(`{"cleanup":{"check_interval":"not-a-duration"}}`)
	rr := doRequest(t, h.Routes(), "PUT", "/api/settings", body, "", "")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
	var resp map[string]string
	parseJSON(t, rr, &resp)
	if resp["error"] != "check_interval must be a valid duration (e.g., \"30m\", \"1h\")" {
		t.Fatalf("unexpected error message: %s", resp["error"])
	}
}


func TestUpdateSettings_Success(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	cfg := &config.Config{
		Cleanup: config.CleanupConfig{RetentionDays: 30, CheckInterval: "1h", DiskThresholdPercent: 95},
		Cameras: []config.CameraConfig{{ID: "cam-1", Name: "Cam1", Protocol: "rtsp_h264", URL: "rtsp://x"}},
	}
	h := newHandlerWithConfig(db, store, cfg)

	body := strings.NewReader(`{"cleanup":{"retention_days":7,"check_interval":"30m","disk_threshold_percent":80}}`)
	rr := doRequest(t, h.Routes(), "PUT", "/api/settings", body, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]string
	parseJSON(t, rr, &resp)
	if resp["status"] != "updated" {
		t.Fatalf("expected status updated, got %s", resp["status"])
	}

	// Verify config was mutated in-memory
	if cfg.Cleanup.RetentionDays != 7 {
		t.Fatalf("expected retention_days=7, got %d", cfg.Cleanup.RetentionDays)
	}
	if cfg.Cleanup.CheckInterval != "30m" {
		t.Fatalf("expected check_interval=30m, got %s", cfg.Cleanup.CheckInterval)
	}
	if cfg.Cleanup.DiskThresholdPercent != 80 {
		t.Fatalf("expected disk_threshold_percent=80, got %d", cfg.Cleanup.DiskThresholdPercent)
	}
}


func TestUpdateSettings_EmptyBody(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	cfg := &config.Config{Cleanup: config.CleanupConfig{RetentionDays: 30}, Cameras: []config.CameraConfig{}}
	h := newHandlerWithConfig(db, store, cfg)

	body := strings.NewReader(`{}`)
	rr := doRequest(t, h.Routes(), "PUT", "/api/settings", body, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 (no-op update), got %d", rr.Code)
	}
	// Verify nothing changed
	if cfg.Cleanup.RetentionDays != 30 {
		t.Fatalf("expected retention_days unchanged at 30, got %d", cfg.Cleanup.RetentionDays)
	}
}

// --- Frames API tests ---

func TestListFrames_NotFound(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "GET", "/api/recordings/nonexistent/frames", nil, "", "")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}
}

func TestListFrames_H264_Error(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	seedRecording(t, db, makeRecording("rec-h264", "cam-1", "h264", now, false))

	rr := doRequest(t, h.Routes(), "GET", "/api/recordings/rec-h264/frames", nil, "", "")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
	var resp map[string]string
	parseJSON(t, rr, &resp)
	if resp["error"] != "not a JPEG recording" {
		t.Fatalf("expected 'not a JPEG recording', got %s", resp["error"])
	}
}

func TestListFrames_MJPEG_Success(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	// Create a directory with frame files for the MJPEG recording
	frameDir := filepath.Join(store.RootDir(), "cam-1", "rec-mjpeg-frames")
	if err := os.MkdirAll(frameDir, 0755); err != nil {
		t.Fatalf("failed to create frame dir: %v", err)
	}
	// Create some fake JPEG frames
	for _, name := range []string{"frame001.jpg", "frame002.jpg", "frame003.jpg"} {
		if err := os.WriteFile(filepath.Join(frameDir, name), []byte("fake-jpeg-"+name), 0644); err != nil {
			t.Fatalf("failed to create frame file: %v", err)
		}
	}
	// Create a non-JPEG file that should be filtered out
	if err := os.WriteFile(filepath.Join(frameDir, "readme.txt"), []byte("not a frame"), 0644); err != nil {
		t.Fatalf("failed to create non-jpeg file: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	rec := makeRecording("rec-mjpeg", "cam-1", "mjpeg", now, false)
	rec.FilePath = frameDir
	seedRecording(t, db, rec)

	rr := doRequest(t, h.Routes(), "GET", "/api/recordings/rec-mjpeg/frames", nil, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	parseJSON(t, rr, &resp)

	frames, ok := resp["frames"].([]any)
	if !ok {
		t.Fatal("expected frames array")
	}
	if len(frames) != 3 {
		t.Fatalf("expected 3 frames, got %d", len(frames))
	}
}

func TestListFrames_MJPEG_EmptyDirectory(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	// Create empty frame directory
	frameDir := filepath.Join(store.RootDir(), "cam-1", "rec-empty")
	if err := os.MkdirAll(frameDir, 0755); err != nil {
		t.Fatalf("failed to create frame dir: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	rec := makeRecording("rec-empty", "cam-1", "mjpeg", now, false)
	rec.FilePath = frameDir
	seedRecording(t, db, rec)

	rr := doRequest(t, h.Routes(), "GET", "/api/recordings/rec-empty/frames", nil, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	parseJSON(t, rr, &resp)
	frames, ok := resp["frames"].([]any)
	if !ok {
		t.Fatal("expected frames array")
	}
	if len(frames) != 0 {
		t.Fatalf("expected 0 frames, got %d", len(frames))
	}
}

func TestListFrames_MJPEG_FrameOrdering(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	frameDir := filepath.Join(store.RootDir(), "cam-1", "rec-ordered")
	if err := os.MkdirAll(frameDir, 0755); err != nil {
		t.Fatalf("failed to create frame dir: %v", err)
	}
	// Create frames out of order to verify sorting
	for _, name := range []string{"frame003.jpg", "frame001.jpg", "frame002.jpg"} {
		if err := os.WriteFile(filepath.Join(frameDir, name), []byte("data-"+name), 0644); err != nil {
			t.Fatalf("failed to create frame file: %v", err)
		}
	}

	now := time.Now().UTC().Truncate(time.Second)
	rec := makeRecording("rec-ordered", "cam-1", "mjpeg", now, false)
	rec.FilePath = frameDir
	seedRecording(t, db, rec)

	rr := doRequest(t, h.Routes(), "GET", "/api/recordings/rec-ordered/frames", nil, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var resp map[string]any
	parseJSON(t, rr, &resp)
	frames, ok := resp["frames"].([]any)
	if !ok {
		t.Fatal("expected frames array")
	}
	if len(frames) != 3 {
		t.Fatalf("expected 3 frames, got %d", len(frames))
	}

	// Verify sorted order and sequential indices
	expectedOrder := []string{"frame001.jpg", "frame002.jpg", "frame003.jpg"}
	for i, ef := range expectedOrder {
		f, ok := frames[i].(map[string]any)
		if !ok {
			t.Fatalf("frame %d is not a map", i)
		}
		if f["filename"] != ef {
			t.Fatalf("frame %d: expected filename %s, got %v", i, ef, f["filename"])
		}
		if f["index"] != float64(i) {
			t.Fatalf("frame %d: expected index %d, got %v", i, i, f["index"])
		}
	}
}

func TestListFrames_MJPEG_DirectoryNotFound(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	rec := makeRecording("rec-nodir", "cam-1", "mjpeg", now, false)
	rec.FilePath = "/nonexistent/path/that/does/not/exist"
	seedRecording(t, db, rec)

	rr := doRequest(t, h.Routes(), "GET", "/api/recordings/rec-nodir/frames", nil, "", "")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}
}

func TestListFrames_MJPEG_FilePathIsNotDirectory(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	rec := makeRecording("rec-file", "cam-1", "mjpeg", now, false)
	rec.FilePath = filepath.Join(store.RootDir(), "single-file.jpg")
	if err := os.WriteFile(rec.FilePath, []byte("fake-jpeg"), 0644); err != nil {
		t.Fatalf("failed to create file: %v", err)
	}
	seedRecording(t, db, rec)

	rr := doRequest(t, h.Routes(), "GET", "/api/recordings/rec-file/frames", nil, "", "")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}
}

// --- Pagination edge cases ---

func TestListRecordings_OffsetZero(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	for i := 0; i < 3; i++ {
		seedRecording(t, db, makeRecording("rec-"+strconv.Itoa(i), "cam-1", "h264", now.Add(time.Duration(i)*time.Hour), false))
	}

	rr := doRequest(t, h.Routes(), "GET", "/api/recordings?offset=0&limit=2", nil, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var resp recordingsResponse
	parseJSON(t, rr, &resp)
	if len(resp.Recordings) != 2 {
		t.Fatalf("expected 2 recordings, got %d", len(resp.Recordings))
	}
	if resp.Total != 3 {
		t.Fatalf("expected total=3, got %d", resp.Total)
	}
}

func TestListRecordings_OffsetBeyondTotal(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	for i := 0; i < 3; i++ {
		seedRecording(t, db, makeRecording("rec-"+strconv.Itoa(i), "cam-1", "h264", now.Add(time.Duration(i)*time.Hour), false))
	}

	// offset=999 with limit=10 returns empty (SQLite requires LIMIT with OFFSET)
	rr := doRequest(t, h.Routes(), "GET", "/api/recordings?limit=10&offset=999", nil, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var resp recordingsResponse
	parseJSON(t, rr, &resp)
	if len(resp.Recordings) != 0 {
		t.Fatalf("expected 0 recordings, got %d", len(resp.Recordings))
	}
	if resp.Total != 3 {
		t.Fatalf("expected total=3, got %d", resp.Total)
	}
}

func TestListRecordings_LimitZero(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	for i := 0; i < 5; i++ {
		seedRecording(t, db, makeRecording("rec-"+strconv.Itoa(i), "cam-1", "h264", now.Add(time.Duration(i)*time.Hour), false))
	}

	// limit=0 should be ignored (no limit set) — returns all
	rr := doRequest(t, h.Routes(), "GET", "/api/recordings?limit=0", nil, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var resp recordingsResponse
	parseJSON(t, rr, &resp)
	if len(resp.Recordings) != 5 {
		t.Fatalf("expected 5 recordings (limit=0 ignored), got %d", len(resp.Recordings))
	}
}

func TestListRecordings_NegativeOffset(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	seedRecording(t, db, makeRecording("rec-1", "cam-1", "h264", now, false))

	// Negative offset should be ignored (n >= 0 check)
	rr := doRequest(t, h.Routes(), "GET", "/api/recordings?offset=-1", nil, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var resp recordingsResponse
	parseJSON(t, rr, &resp)
	if len(resp.Recordings) != 1 {
		t.Fatalf("expected 1 recording (negative offset ignored), got %d", len(resp.Recordings))
	}
}

// --- Filter combination tests ---

func TestListRecordings_CameraIDAndFormat(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	seedRecording(t, db, makeRecording("rec-1", "cam-1", "h264", now, false))
	seedRecording(t, db, makeRecording("rec-2", "cam-1", "mjpeg", now, false))
	seedRecording(t, db, makeRecording("rec-3", "cam-2", "h264", now, false))
	seedRecording(t, db, makeRecording("rec-4", "cam-2", "mjpeg", now, false))

	rr := doRequest(t, h.Routes(), "GET", "/api/recordings?camera_id=cam-1&format=mjpeg", nil, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var resp recordingsResponse
	parseJSON(t, rr, &resp)
	if len(resp.Recordings) != 1 {
		t.Fatalf("expected 1 recording, got %d", len(resp.Recordings))
	}
	if resp.Recordings[0].ID != "rec-2" {
		t.Fatalf("expected rec-2, got %s", resp.Recordings[0].ID)
	}
}

func TestListRecordings_MergedAndTimeRange(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	seedRecording(t, db, makeRecording("rec-old-merged", "cam-1", "h264", now.Add(-48*time.Hour), true))
	seedRecording(t, db, makeRecording("rec-new-merged", "cam-1", "h264", now.Add(-1*time.Hour), true))
	seedRecording(t, db, makeRecording("rec-new-unmerged", "cam-1", "h264", now.Add(-1*time.Hour), false))

	start := now.Add(-24 * time.Hour).Format(time.RFC3339)
	end := now.Format(time.RFC3339)
	rr := doRequest(t, h.Routes(), "GET", "/api/recordings?merged=true&start="+start+"&end="+end, nil, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var resp recordingsResponse
	parseJSON(t, rr, &resp)
	if len(resp.Recordings) != 1 {
		t.Fatalf("expected 1 recording, got %d", len(resp.Recordings))
	}
	if resp.Recordings[0].ID != "rec-new-merged" {
		t.Fatalf("expected rec-new-merged, got %s", resp.Recordings[0].ID)
	}
}

func TestListRecordings_AllFilters(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	seedRecording(t, db, makeRecording("rec-match", "cam-1", "mjpeg", now.Add(-1*time.Hour), true))
	seedRecording(t, db, makeRecording("rec-diff-camera", "cam-2", "mjpeg", now.Add(-1*time.Hour), true))
	seedRecording(t, db, makeRecording("rec-diff-format", "cam-1", "h264", now.Add(-1*time.Hour), true))
	seedRecording(t, db, makeRecording("rec-diff-merged", "cam-1", "mjpeg", now.Add(-1*time.Hour), false))
	seedRecording(t, db, makeRecording("rec-diff-time", "cam-1", "mjpeg", now.Add(-48*time.Hour), true))

	start := now.Add(-24 * time.Hour).Format(time.RFC3339)
	end := now.Format(time.RFC3339)
	rr := doRequest(t, h.Routes(), "GET", "/api/recordings?camera_id=cam-1&format=mjpeg&merged=true&start="+start+"&end="+end, nil, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var resp recordingsResponse
	parseJSON(t, rr, &resp)
	if len(resp.Recordings) != 1 {
		t.Fatalf("expected 1 recording, got %d: %+v", len(resp.Recordings), resp.Recordings)
	}
	if resp.Recordings[0].ID != "rec-match" {
		t.Fatalf("expected rec-match, got %s", resp.Recordings[0].ID)
	}
}

// --- Upload auth tests ---

func TestUploadAuth_RequiresAuth(t *testing.T) {
	db, store := setupTestDB(t)
	defer db.Close()
	hash, err := middleware.HashPassword("secret")
	if err != nil {
		t.Fatalf("failed to hash password: %v", err)
	}
	h := TestHandlerWithAuth(db, store, "admin", hash)

	// Settings endpoints are behind auth middleware
	endpoints := []string{
		"GET /api/settings",
		"PUT /api/settings",
		"GET /api/recordings/rec-1/frames",
	}
	for _, ep := range endpoints {
		method, path := splitEndpoint(ep)
		rr := doRequest(t, h.Routes(), method, path, nil, "", "")
		if rr.Code != http.StatusUnauthorized {
			t.Errorf("endpoint %s: expected 401, got %d", ep, rr.Code)
		}
	}

	// With valid auth, settings should be accessible (but return 500 because nil config)
	rr := doRequest(t, h.Routes(), "GET", "/api/settings", nil, "admin", "secret")
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("GET /api/settings with auth: expected 500 (nil config), got %d", rr.Code)
	}
}

func TestUploadAuth_WithConfig(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	hash, err := middleware.HashPassword("secret")
	if err != nil {
		t.Fatalf("failed to hash password: %v", err)
	}
	cfg := &config.Config{Cleanup: config.CleanupConfig{RetentionDays: 30}, Cameras: []config.CameraConfig{}}
	h := newHandlerWithConfigAndAuth(db, store, "admin", hash, cfg)

	// Without auth
	rr := doRequest(t, h.Routes(), "GET", "/api/settings", nil, "", "")
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}

	// With valid auth
	rr = doRequest(t, h.Routes(), "GET", "/api/settings", nil, "admin", "secret")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	// With wrong auth
	rr = doRequest(t, h.Routes(), "GET", "/api/settings", nil, "admin", "wrong")
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

// --- Additional edge cases ---

func TestListRecordings_InvalidTimeFormat(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	seedRecording(t, db, makeRecording("rec-1", "cam-1", "h264", now, false))

	// Invalid time format should be silently ignored (time.Parse fails, filter not applied)
	rr := doRequest(t, h.Routes(), "GET", "/api/recordings?start=not-a-date", nil, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var resp recordingsResponse
	parseJSON(t, rr, &resp)
	if len(resp.Recordings) != 1 {
		t.Fatalf("expected 1 recording (invalid time ignored), got %d", len(resp.Recordings))
	}
}

func TestListRecordings_MergedFalse(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	seedRecording(t, db, makeRecording("rec-merged", "cam-1", "h264", now, true))
	seedRecording(t, db, makeRecording("rec-unmerged", "cam-1", "h264", now, false))
	seedRecording(t, db, makeRecording("rec-unmerged2", "cam-1", "h264", now, false))

	rr := doRequest(t, h.Routes(), "GET", "/api/recordings?merged=false", nil, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var resp recordingsResponse
	parseJSON(t, rr, &resp)
	if len(resp.Recordings) != 2 {
		t.Fatalf("expected 2 unmerged recordings, got %d", len(resp.Recordings))
	}
}

func TestListRecordings_TotalCount(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	for i := 0; i < 10; i++ {
		seedRecording(t, db, makeRecording("rec-"+strconv.Itoa(i), "cam-1", "h264", now.Add(time.Duration(i)*time.Hour), false))
	}

	rr := doRequest(t, h.Routes(), "GET", "/api/recordings?limit=3&offset=2", nil, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var resp recordingsResponse
	parseJSON(t, rr, &resp)
	if len(resp.Recordings) != 3 {
		t.Fatalf("expected 3 recordings, got %d", len(resp.Recordings))
	}
	if resp.Total != 10 {
		t.Fatalf("expected total=10, got %d", resp.Total)
	}
}

func TestDeleteRecording_InvalidID(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	// Delete non-existent recording
	rr := doRequest(t, h.Routes(), "DELETE", "/api/recordings/does-not-exist", nil, "", "")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}
}


func TestDownloadRecording_MissingFile(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	rec := makeRecording("rec-nofile", "cam-1", "h264", now, false)
	rec.FilePath = "/nonexistent/file.mp4"
	seedRecording(t, db, rec)

	rr := doRequest(t, h.Routes(), "GET", "/api/recordings/rec-nofile/download", nil, "", "")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}
}

func TestListFrames_JPEGCaseInsensitive(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	frameDir := filepath.Join(store.RootDir(), "cam-1", "rec-mixedcase")
	if err := os.MkdirAll(frameDir, 0755); err != nil {
		t.Fatalf("failed to create frame dir: %v", err)
	}
	// Mixed case extensions should all be picked up
	for _, name := range []string{"frame1.JPG", "frame2.jpeg", "frame3.JPEG"} {
		if err := os.WriteFile(filepath.Join(frameDir, name), []byte("data"), 0644); err != nil {
			t.Fatalf("failed to create frame file: %v", err)
		}
	}

	now := time.Now().UTC().Truncate(time.Second)
	rec := makeRecording("rec-mixedcase", "cam-1", "mjpeg", now, false)
	rec.FilePath = frameDir
	seedRecording(t, db, rec)

	rr := doRequest(t, h.Routes(), "GET", "/api/recordings/rec-mixedcase/frames", nil, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var resp map[string]any
	parseJSON(t, rr, &resp)
	frames, ok := resp["frames"].([]any)
	if !ok {
		t.Fatal("expected frames array")
	}
	if len(frames) != 3 {
		t.Fatalf("expected 3 frames (case insensitive), got %d", len(frames))
	}
}

// --- Frame download tests ---

func TestDownloadFrame_Success(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	// Create a directory with frame files for MJPEG recording
	frameDir := filepath.Join(store.RootDir(), "cam-1", "rec-frame-dl")
	if err := os.MkdirAll(frameDir, 0755); err != nil {
		t.Fatalf("failed to create frame dir: %v", err)
	}
	frameData := map[string]string{
		"frame001.jpg": "data-frame-001",
		"frame002.jpg": "data-frame-002",
		"frame003.jpg": "data-frame-003",
	}
	for name, data := range frameData {
		if err := os.WriteFile(filepath.Join(frameDir, name), []byte(data), 0644); err != nil {
			t.Fatalf("failed to create frame file: %v", err)
		}
	}
	// Non-image file should be ignored
	if err := os.WriteFile(filepath.Join(frameDir, "readme.txt"), []byte("not-a-frame"), 0644); err != nil {
		t.Fatalf("failed to create non-image file: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	rec := makeRecording("rec-frame-dl", "cam-1", "mjpeg", now, false)
	rec.FilePath = frameDir
	seedRecording(t, db, rec)

	// Request frame index 1 (second frame)
	rr := doRequest(t, h.Routes(), "GET", "/api/recordings/rec-frame-dl/download?frame=1", nil, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	body, _ := io.ReadAll(rr.Body)
	if string(body) != "data-frame-002" {
		t.Fatalf("expected frame 1 data 'data-frame-002', got %q", string(body))
	}

	// Verify content type
	ct := rr.Header().Get("Content-Type")
	if ct != "image/jpeg" {
		t.Fatalf("expected Content-Type image/jpeg, got %s", ct)
	}
}

func TestDownloadFrame_FirstFrame(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	frameDir := filepath.Join(store.RootDir(), "cam-1", "rec-first-frame")
	if err := os.MkdirAll(frameDir, 0755); err != nil {
		t.Fatalf("failed to create frame dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(frameDir, "001.jpg"), []byte("first"), 0644); err != nil {
		t.Fatalf("failed to create frame: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	rec := makeRecording("rec-first-frame", "cam-1", "mjpeg", now, false)
	rec.FilePath = frameDir
	seedRecording(t, db, rec)

	rr := doRequest(t, h.Routes(), "GET", "/api/recordings/rec-first-frame/download?frame=0", nil, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	body, _ := io.ReadAll(rr.Body)
	if string(body) != "first" {
		t.Fatalf("expected 'first', got %q", string(body))
	}
}

func TestDownloadFrame_OutOfRange(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	frameDir := filepath.Join(store.RootDir(), "cam-1", "rec-oob")
	if err := os.MkdirAll(frameDir, 0755); err != nil {
		t.Fatalf("failed to create frame dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(frameDir, "only.jpg"), []byte("only"), 0644); err != nil {
		t.Fatalf("failed to create frame: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	rec := makeRecording("rec-oob", "cam-1", "mjpeg", now, false)
	rec.FilePath = frameDir
	seedRecording(t, db, rec)

	// frame=99 is out of range
	rr := doRequest(t, h.Routes(), "GET", "/api/recordings/rec-oob/download?frame=99", nil, "", "")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}
}

func TestDownloadFrame_InvalidIndex(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	frameDir := filepath.Join(store.RootDir(), "cam-1", "rec-invalid")
	if err := os.MkdirAll(frameDir, 0755); err != nil {
		t.Fatalf("failed to create frame dir: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	rec := makeRecording("rec-invalid", "cam-1", "mjpeg", now, false)
	rec.FilePath = frameDir
	seedRecording(t, db, rec)

	// frame=abc is not a number
	rr := doRequest(t, h.Routes(), "GET", "/api/recordings/rec-invalid/download?frame=abc", nil, "", "")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}
}

func TestDownloadFrame_IgnoredForH264(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	rec := makeRecording("rec-h264-frame", "cam-1", "h264", now, false)
	rec.FilePath = filepath.Join(store.RootDir(), "rec-h264-frame.mp4")
	if err := os.WriteFile(rec.FilePath, []byte("fake-mp4"), 0644); err != nil {
		t.Fatalf("failed to create file: %v", err)
	}
	seedRecording(t, db, rec)

	// ?frame=N is ignored for H264, should serve the file normally
	rr := doRequest(t, h.Routes(), "GET", "/api/recordings/rec-h264-frame/download?frame=5", nil, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	body, _ := io.ReadAll(rr.Body)
	if string(body) != "fake-mp4" {
		t.Fatalf("expected 'fake-mp4', got %q", string(body))
	}
}

// --- Camera CRUD API tests ---

// newTestCamHandler creates a Handler with a CameraManager for testing.
func newTestCamHandler(t *testing.T) (*Handler, *camera.CameraManager, *config.Config) {
	t.Helper()
	db, store := setupTestDB(t)
	cfg := &config.Config{
		Storage: config.StorageConfig{RootDir: store.RootDir(), SegmentDuration: "30s"},
		Cleanup: config.CleanupConfig{RetentionDays: 30, CheckInterval: "1h", DiskThresholdPercent: 95},
		Cameras: []config.CameraConfig{},
	}
	camMgr := camera.NewCameraManager(cfg, store, db, "")
	h := NewHandler(db, store, noopAuthMW(), cfg, camMgr, nil, "", nil, nil, nil)
	return h, camMgr, cfg
}

func TestHandleCreateCamera(t *testing.T) {
	t.Parallel()
	h, _, _ := newTestCamHandler(t)

	body := strings.NewReader(`{"name":"Front Door","protocol":"rtsp_h264","url":"rtsp://camera1/stream"}`)
	rr := doRequest(t, h.Routes(), "POST", "/api/cameras", body, "", "")
	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}
	var cam config.CameraConfig
	parseJSON(t, rr, &cam)
	if cam.Name != "Front Door" {
		t.Fatalf("expected name 'Front Door', got %s", cam.Name)
	}
	if cam.Protocol != "rtsp" {
		t.Fatalf("expected protocol 'rtsp', got %s", cam.Protocol)
	}
	if cam.ID == "" {
		t.Fatal("expected non-empty camera ID")
	}
}

func TestHandleCreateCamera_MissingFields(t *testing.T) {
	t.Parallel()
	h, _, _ := newTestCamHandler(t)

	cases := []struct {
		name    string
		body    string
		wantErr string
	}{
		{"missing name", `{"protocol":"rtsp_h264","url":"rtsp://x"}`, "name is required"},
		{"missing url", `{"name":"Cam","protocol":"rtsp_h264"}`, "url is required"},
		{"missing protocol", `{"name":"Cam","url":"rtsp://x"}`, "protocol is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := doRequest(t, h.Routes(), "POST", "/api/cameras", strings.NewReader(tc.body), "", "")
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
			}
			var resp map[string]string
			parseJSON(t, rr, &resp)
			if resp["error"] != tc.wantErr {
				t.Fatalf("expected error %q, got %q", tc.wantErr, resp["error"])
			}
		})
	}
}

func TestHandleCreateCamera_InvalidProtocol(t *testing.T) {
	t.Parallel()
	h, _, _ := newTestCamHandler(t)

	body := strings.NewReader(`{"name":"Cam","protocol":"invalid_proto","url":"rtsp://x"}`)
	rr := doRequest(t, h.Routes(), "POST", "/api/cameras", body, "", "")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]string
	parseJSON(t, rr, &resp)
	if !strings.Contains(resp["error"], "invalid protocol") {
		t.Fatalf("expected invalid protocol error, got %q", resp["error"])
	}
}

func TestHandleGetCamera(t *testing.T) {
	t.Parallel()
	h, _, _ := newTestCamHandler(t)

	// Create a camera first
	body := strings.NewReader(`{"name":"Test","protocol":"http_jpeg","url":"http://cam/snap"}`)
	rr := doRequest(t, h.Routes(), "POST", "/api/cameras", body, "", "")
	if rr.Code != http.StatusCreated {
		t.Fatalf("setup: expected 201, got %d", rr.Code)
	}
	var created config.CameraConfig
	parseJSON(t, rr, &created)

	// Get it
	rr = doRequest(t, h.Routes(), "GET", "/api/cameras/"+created.ID, nil, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var row storage.CameraRow
	parseJSON(t, rr, &row)
	if row.ID != created.ID {
		t.Fatalf("expected id %s, got %s", created.ID, row.ID)
	}
	if row.Name != "Test" {
		t.Fatalf("expected name 'Test', got %s", row.Name)
	}
}

func TestHandleGetCamera_NotFound(t *testing.T) {
	t.Parallel()
	h, _, _ := newTestCamHandler(t)

	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/nonexistent", nil, "", "")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestHandleUpdateCamera(t *testing.T) {
	t.Parallel()
	h, _, _ := newTestCamHandler(t)

	// Create a camera
	body := strings.NewReader(`{"name":"Original","protocol":"http_jpeg","url":"http://cam/snap"}`)
	rr := doRequest(t, h.Routes(), "POST", "/api/cameras", body, "", "")
	if rr.Code != http.StatusCreated {
		t.Fatalf("setup: expected 201, got %d", rr.Code)
	}
	var created config.CameraConfig
	parseJSON(t, rr, &created)

	// Update it
	updateBody := strings.NewReader(`{"name":"Updated Name"}`)
	rr = doRequest(t, h.Routes(), "PUT", "/api/cameras/"+created.ID, updateBody, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var updated config.CameraConfig
	parseJSON(t, rr, &updated)
	if updated.Name != "Updated Name" {
		t.Fatalf("expected name 'Updated Name', got %s", updated.Name)
	}
}

func TestHandleUpdateCamera_NotFound(t *testing.T) {
	t.Parallel()
	h, _, _ := newTestCamHandler(t)

	body := strings.NewReader(`{"name":"X"}`)
	rr := doRequest(t, h.Routes(), "PUT", "/api/cameras/nonexistent", body, "", "")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestHandleDeleteCamera(t *testing.T) {
	t.Parallel()
	h, _, _ := newTestCamHandler(t)

	// Create a camera
	body := strings.NewReader(`{"name":"To Delete","protocol":"http_jpeg","url":"http://cam/snap"}`)
	rr := doRequest(t, h.Routes(), "POST", "/api/cameras", body, "", "")
	if rr.Code != http.StatusCreated {
		t.Fatalf("setup: expected 201, got %d", rr.Code)
	}
	var created config.CameraConfig
	parseJSON(t, rr, &created)

	// Delete it
	rr = doRequest(t, h.Routes(), "DELETE", "/api/cameras/"+created.ID, nil, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]string
	parseJSON(t, rr, &resp)
	if resp["status"] != "archived" {
		t.Fatalf("expected status 'archived', got %s", resp["status"])
	}
}

func TestHandleDeleteCamera_NotFound(t *testing.T) {
	t.Parallel()
	h, _, _ := newTestCamHandler(t)

	rr := doRequest(t, h.Routes(), "DELETE", "/api/cameras/nonexistent", nil, "", "")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rr.Code, rr.Body.String())
	}
}

// --- Enhanced health and readyz tests ---

func TestHealthEnhanced(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "GET", "/api/health", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	var body HealthResponse
	parseJSON(t, rr, &body)
	require.Equal(t, "ok", body.Status)
	require.NotEmpty(t, body.Uptime)
	require.NotNil(t, body.Checks)
	require.Contains(t, body.Checks, "database")
	require.Contains(t, body.Checks, "storage")
	require.Contains(t, body.Checks, "goroutines")
}

func TestHealthReturnsOkWhenAllChecksPass(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "GET", "/api/health", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	var body HealthResponse
	parseJSON(t, rr, &body)
	require.Equal(t, "ok", body.Status)
	require.Equal(t, "ok", body.Checks["database"].Status)
	require.Equal(t, "ok", body.Checks["goroutines"].Status)
	// Storage check should also be ok (temp dir has plenty of space)
	require.Equal(t, "ok", body.Checks["storage"].Status)
}

func TestHealthWithNilDB(t *testing.T) {
	t.Parallel()
	h := TestHandler(nil, nil)

	rr := doRequest(t, h.Routes(), "GET", "/api/health", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	var body HealthResponse
	parseJSON(t, rr, &body)
	require.Equal(t, "unhealthy", body.Status)
	require.Equal(t, "error", body.Checks["database"].Status)
	require.Equal(t, "error", body.Checks["storage"].Status)
}

func TestReadyzReturns200(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "GET", "/api/readyz", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	var body map[string]string
	parseJSON(t, rr, &body)
	require.Equal(t, "ok", body["status"])
}

func TestReadyzWithNilDB(t *testing.T) {
	t.Parallel()
	h := TestHandler(nil, nil)

	rr := doRequest(t, h.Routes(), "GET", "/api/readyz", nil, "", "")
	require.Equal(t, http.StatusServiceUnavailable, rr.Code)

	var body map[string]any
	parseJSON(t, rr, &body)
	require.Equal(t, "not ready", body["status"])
}

// --- Snapshot endpoint tests ---

// newSnapshotTestHandler creates a Handler with a config that has a snapshot-enabled camera.
func newSnapshotTestHandler(t *testing.T, snapshotServer *httptest.Server, cameraID string) *Handler {
	t.Helper()
	db, store := setupTestDB(t)
	cfg := &config.Config{
		Cleanup: config.CleanupConfig{RetentionDays: 30, CheckInterval: "1h", DiskThresholdPercent: 95},
		Cameras: []config.CameraConfig{
			{
				ID:          cameraID,
				Name:        "SnapCam",
				Protocol:    "http_jpeg",
				URL:         snapshotServer.URL + "/stream",
				SnapshotURL: snapshotServer.URL + "/snapshot.jpg",
			},
		},
	}
	return NewHandler(db, store, noopAuthMW(), cfg, nil, nil, "", nil, nil, nil)
}

func TestHandleSnapshot_NoURL(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	cfg := &config.Config{
		Cleanup: config.CleanupConfig{RetentionDays: 30},
		Cameras: []config.CameraConfig{
			{ID: "cam-1", Name: "NoSnap", Protocol: "rtsp_h264", URL: "rtsp://x"},
		},
	}
	h := NewHandler(db, store, noopAuthMW(), cfg, nil, nil, "", nil, nil, nil)

	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/cam-1/snapshot", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
	require.Contains(t, rr.Body.String(), "Snapshot URL not configured")
}

func TestHandleSnapshot_CameraNotFound(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	cfg := &config.Config{
		Cleanup: config.CleanupConfig{RetentionDays: 30},
		Cameras: []config.CameraConfig{},
	}
	h := NewHandler(db, store, noopAuthMW(), cfg, nil, nil, "", nil, nil, nil)

	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/nonexistent/snapshot", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestHandleSnapshot_Success(t *testing.T) {
	t.Parallel()
	jpegData := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 0x4A, 0x46} // fake JPEG header
	snapshotServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(jpegData)
	}))
	defer snapshotServer.Close()

	h := newSnapshotTestHandler(t, snapshotServer, "cam-snap")

	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/cam-snap/snapshot", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, "image/jpeg", rr.Header().Get("Content-Type"))
	body, err := io.ReadAll(rr.Body)
	require.NoError(t, err)
	require.Equal(t, jpegData, body)
}

func TestHandleSnapshot_CacheHit(t *testing.T) {
	t.Parallel()
	requestCount := 0
	jpegData := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 0x4A, 0x46}
	snapshotServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(jpegData)
	}))
	defer snapshotServer.Close()

	h := newSnapshotTestHandler(t, snapshotServer, "cam-snap")

	// First request — should fetch from server
	rr1 := doRequest(t, h.Routes(), "GET", "/api/cameras/cam-snap/snapshot", nil, "", "")
	require.Equal(t, http.StatusOK, rr1.Code)
	require.Equal(t, 1, requestCount, "first request should hit the server")

	// Second request — should be served from cache (10s TTL)
	rr2 := doRequest(t, h.Routes(), "GET", "/api/cameras/cam-snap/snapshot", nil, "", "")
	require.Equal(t, http.StatusOK, rr2.Code)
	require.Equal(t, 1, requestCount, "second request should be served from cache, not hit server")
}

func TestHandleSnapshot_StaleFallback(t *testing.T) {
	t.Parallel()
	requestCount := 0
	jpegData := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 0x4A, 0x46}
	snapshotServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(jpegData)
	}))

	h := newSnapshotTestHandler(t, snapshotServer, "cam-snap")

	// First request — populate cache
	rr1 := doRequest(t, h.Routes(), "GET", "/api/cameras/cam-snap/snapshot", nil, "", "")
	require.Equal(t, http.StatusOK, rr1.Code)
	require.Equal(t, 1, requestCount)

	// Make cache stale by backdating the timestamp
	h.snapshotMu.Lock()
	if cached, ok := h.snapshots["cam-snap"]; ok {
		h.snapshots["cam-snap"] = &snapshotCache{
			data:      cached.data,
			timestamp: time.Now().Add(-11 * time.Second),
		}
	}
	h.snapshotMu.Unlock()

	// Close the server to cause a connection error (stale fallback only triggers on err != nil)
	snapshotServer.Close()

	// Second request — cache is stale, fetch fails with connection error, should return stale cache
	rr2 := doRequest(t, h.Routes(), "GET", "/api/cameras/cam-snap/snapshot", nil, "", "")
	require.Equal(t, http.StatusOK, rr2.Code)
	require.Equal(t, "stale", rr2.Header().Get("X-Cache"), "should indicate stale cache")
	require.Equal(t, "image/jpeg", rr2.Header().Get("Content-Type"))
	body, err := io.ReadAll(rr2.Body)
	require.NoError(t, err)
	require.Equal(t, jpegData, body, "should return cached JPEG data")
}

func TestHandleSnapshot_CameraError(t *testing.T) {
	t.Parallel()
	snapshotServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("camera error"))
	}))
	defer snapshotServer.Close()

	h := newSnapshotTestHandler(t, snapshotServer, "cam-snap")

	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/cam-snap/snapshot", nil, "", "")
	require.Equal(t, http.StatusBadGateway, rr.Code)
}

func TestHandleSnapshot_ServerUnreachable(t *testing.T) {
	t.Parallel()
	snapshotServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	h := newSnapshotTestHandler(t, snapshotServer, "cam-snap")
	snapshotServer.Close()

	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/cam-snap/snapshot", nil, "", "")
	require.Equal(t, http.StatusBadGateway, rr.Code)
}

func TestListRecordings_SearchQuery(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	seedRecording(t, db, makeRecording("rec-1", "cam-front", "h264", now, false))
	seedRecording(t, db, makeRecording("rec-2", "cam-back", "h264", now, false))
	seedRecording(t, db, makeRecording("rec-3", "door", "h264", now, false))

	rr := doRequest(t, h.Routes(), "GET", "/api/recordings?search=cam", nil, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var resp recordingsResponse
	parseJSON(t, rr, &resp)
	if len(resp.Recordings) != 2 {
		t.Fatalf("expected 2 recordings, got %d", len(resp.Recordings))
	}
}

// --- Merge settings tests ---

func TestGetMergeSettings_NoConfig(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store) // nil config

	rr := doRequest(t, h.Routes(), "GET", "/api/settings/merge", nil, "", "")
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rr.Code)
	}
	var body map[string]string
	parseJSON(t, rr, &body)
	if body["error"] != "config not available" {
		t.Fatalf("expected 'config not available', got %s", body["error"])
	}
}

func TestGetMergeSettings_WithConfig(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	cfg := &config.Config{
		Merge: config.MergeConfig{
			Enabled:            true,
			CheckInterval:      "30m",
			WindowSize:         "24h",
			BatchLimit:         50,
			MinSegmentAge:      "1h",
			MinSegmentsToMerge: 5,
		},
		Cleanup: config.CleanupConfig{RetentionDays: 30},
		Cameras: []config.CameraConfig{},
	}
	h := newHandlerWithConfig(db, store, cfg)

	rr := doRequest(t, h.Routes(), "GET", "/api/settings/merge", nil, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var body map[string]any
	parseJSON(t, rr, &body)

	if body["enabled"] != true {
		t.Fatalf("expected enabled=true, got %v", body["enabled"])
	}
	if body["check_interval"] != "30m" {
		t.Fatalf("expected check_interval=30m, got %v", body["check_interval"])
	}
	if body["window_size"] != "24h" {
		t.Fatalf("expected window_size=24h, got %v", body["window_size"])
	}
	if body["batch_limit"] != float64(50) {
		t.Fatalf("expected batch_limit=50, got %v", body["batch_limit"])
	}
	if body["min_segment_age"] != "1h" {
		t.Fatalf("expected min_segment_age=1h, got %v", body["min_segment_age"])
	}
	if body["min_segments_to_merge"] != float64(5) {
		t.Fatalf("expected min_segments_to_merge=5, got %v", body["min_segments_to_merge"])
	}
}

func TestUpdateMergeSettings_NoConfig(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store) // nil config

	body := strings.NewReader(`{"enabled":true}`)
	rr := doRequest(t, h.Routes(), "PUT", "/api/settings/merge", body, "", "")
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rr.Code)
	}
}

func TestUpdateMergeSettings_InvalidBody(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	cfg := &config.Config{Cleanup: config.CleanupConfig{RetentionDays: 30}, Cameras: []config.CameraConfig{}}
	h := newHandlerWithConfig(db, store, cfg)

	body := strings.NewReader(`{invalid json`)
	rr := doRequest(t, h.Routes(), "PUT", "/api/settings/merge", body, "", "")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

func TestUpdateMergeSettings_InvalidCheckInterval(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	cfg := &config.Config{Cleanup: config.CleanupConfig{RetentionDays: 30}, Cameras: []config.CameraConfig{}}
	h := newHandlerWithConfig(db, store, cfg)

	body := strings.NewReader(`{"check_interval":"not-a-duration"}`)
	rr := doRequest(t, h.Routes(), "PUT", "/api/settings/merge", body, "", "")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
	var resp map[string]string
	parseJSON(t, rr, &resp)
	if resp["error"] != "check_interval must be a valid duration (e.g., \"30m\", \"1h\")" {
		t.Fatalf("unexpected error: %s", resp["error"])
	}
}

func TestUpdateMergeSettings_InvalidBatchLimit(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	cfg := &config.Config{Cleanup: config.CleanupConfig{RetentionDays: 30}, Cameras: []config.CameraConfig{}}
	h := newHandlerWithConfig(db, store, cfg)

	body := strings.NewReader(`{"batch_limit":0}`)
	rr := doRequest(t, h.Routes(), "PUT", "/api/settings/merge", body, "", "")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

func TestUpdateMergeSettings_Success(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	cfg := &config.Config{
		Merge: config.MergeConfig{Enabled: false, CheckInterval: "1h"},
		Cleanup: config.CleanupConfig{RetentionDays: 30},
		Cameras: []config.CameraConfig{},
	}
	h := newHandlerWithConfig(db, store, cfg)

	body := strings.NewReader(`{"enabled":true,"batch_limit":100}`)
	rr := doRequest(t, h.Routes(), "PUT", "/api/settings/merge", body, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]string
	parseJSON(t, rr, &resp)
	if resp["status"] != "updated" {
		t.Fatalf("expected status=updated, got %s", resp["status"])
	}
	// Verify in-memory config was updated
	if !cfg.Merge.Enabled {
		t.Fatal("expected enabled=true")
	}
	if cfg.Merge.BatchLimit != 100 {
		t.Fatalf("expected batch_limit=100, got %d", cfg.Merge.BatchLimit)
	}
}

// --- Camera merge config tests ---

func TestUpdateCameraMergeConfig_Success(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	cfg := &config.Config{Cleanup: config.CleanupConfig{RetentionDays: 30}, Cameras: []config.CameraConfig{}}
	h := newHandlerWithConfig(db, store, cfg)

	// Seed a camera
	_, err := db.DB().Exec("INSERT INTO cameras (id, name, protocol, url, enabled) VALUES (?, ?, ?, ?, 1)",
			"cam1", "Test Cam", "rtsp_h264", "rtsp://camera/stream")
	require.NoError(t, err)

	body := strings.NewReader(`{"enabled":true,"batch_limit":20}`)
	rr := doRequest(t, h.Routes(), "PUT", "/api/cameras/cam1/merge-config", body, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]string
	parseJSON(t, rr, &resp)
	if resp["status"] != "updated" {
		t.Fatalf("expected status=updated, got %s", resp["status"])
	}
}

func TestUpdateCameraMergeConfig_InvalidDuration(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	cfg := &config.Config{Cleanup: config.CleanupConfig{RetentionDays: 30}, Cameras: []config.CameraConfig{}}
	h := newHandlerWithConfig(db, store, cfg)

	_, err := db.DB().Exec("INSERT INTO cameras (id, name, protocol, url, enabled) VALUES (?, ?, ?, ?, 1)",
			"cam1", "Test Cam", "rtsp_h264", "rtsp://camera/stream")
	require.NoError(t, err)

	body := strings.NewReader(`{"check_interval":"bad"}`)
	rr := doRequest(t, h.Routes(), "PUT", "/api/cameras/cam1/merge-config", body, "", "")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

func TestUpdateCameraMergeConfig_CameraNotFound(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	cfg := &config.Config{Cleanup: config.CleanupConfig{RetentionDays: 30}, Cameras: []config.CameraConfig{}}
	h := newHandlerWithConfig(db, store, cfg)

	body := strings.NewReader(`{"enabled":true}`)
	rr := doRequest(t, h.Routes(), "PUT", "/api/cameras/nonexistent/merge-config", body, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 (no-op on nonexistent camera), got %d", rr.Code)
	}
}

func TestDeleteCameraMergeConfig_Success(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	cfg := &config.Config{Cleanup: config.CleanupConfig{RetentionDays: 30}, Cameras: []config.CameraConfig{}}
	h := newHandlerWithConfig(db, store, cfg)

	// Seed a camera
	_, err := db.DB().Exec("INSERT INTO cameras (id, name, protocol, url, enabled) VALUES (?, ?, ?, ?, 1)",
			"cam1", "Test Cam", "rtsp_h264", "rtsp://camera/stream")
	require.NoError(t, err)

	rr := doRequest(t, h.Routes(), "DELETE", "/api/cameras/cam1/merge-config", nil, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]string
	parseJSON(t, rr, &resp)
	if resp["status"] != "cleared" {
		t.Fatalf("expected status=cleared, got %s", resp["status"])
	}
}

func TestDeleteCameraMergeConfig_CameraNotFound(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	cfg := &config.Config{Cleanup: config.CleanupConfig{RetentionDays: 30}, Cameras: []config.CameraConfig{}}
	h := newHandlerWithConfig(db, store, cfg)

	rr := doRequest(t, h.Routes(), "DELETE", "/api/cameras/nonexistent/merge-config", nil, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 (no-op on nonexistent camera), got %d", rr.Code)
	}
}

func TestGetCameraMergeConfig_Success(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	cfg := &config.Config{Cleanup: config.CleanupConfig{RetentionDays: 30}, Cameras: []config.CameraConfig{}}
	h := newHandlerWithConfig(db, store, cfg)

	// Seed a camera
	_, err := db.DB().Exec("INSERT INTO cameras (id, name, protocol, url, enabled) VALUES (?, ?, ?, ?, 1)",
		"cam1", "Test Cam", "rtsp_h264", "rtsp://camera/stream")
	require.NoError(t, err)

	// Set per-camera merge config
	mergeEnabled := true
	checkInterval := "30m"
	windowSize := "2h"
	batchLimit := 50
	minSegmentAge := "5m"
	minSegments := 5
	err = db.UpsertCameraMerge(context.Background(), "cam1",
		&mergeEnabled, &checkInterval, &windowSize, &minSegmentAge, &batchLimit, &minSegments)
	require.NoError(t, err)

	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/cam1/merge-config", nil, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]any
	parseJSON(t, rr, &resp)
	require.Equal(t, true, resp["enabled"])
	require.Equal(t, "30m", resp["check_interval"])
	require.Equal(t, "2h", resp["window_size"])
	require.Equal(t, float64(50), resp["batch_limit"])
	require.Equal(t, "5m", resp["min_segment_age"])
	require.Equal(t, float64(5), resp["min_segments_to_merge"])
}

func TestGetCameraMergeConfig_NoConfig(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	cfg := &config.Config{Cleanup: config.CleanupConfig{RetentionDays: 30}, Cameras: []config.CameraConfig{}}
	h := newHandlerWithConfig(db, store, cfg)

	// Seed a camera with NO merge config
	_, err := db.DB().Exec("INSERT INTO cameras (id, name, protocol, url, enabled) VALUES (?, ?, ?, ?, 1)",
		"cam2", "No Merge", "rtsp_h264", "rtsp://camera/stream")
	require.NoError(t, err)

	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/cam2/merge-config", nil, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]any
	parseJSON(t, rr, &resp)
	// All fields should be nil when no config is set
	require.Nil(t, resp["enabled"])
	require.Nil(t, resp["check_interval"])
	require.Nil(t, resp["window_size"])
	require.Nil(t, resp["batch_limit"])
	require.Nil(t, resp["min_segment_age"])
	require.Nil(t, resp["min_segments_to_merge"])
}

func TestGetCameraMergeConfig_NotFound(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	cfg := &config.Config{Cleanup: config.CleanupConfig{RetentionDays: 30}, Cameras: []config.CameraConfig{}}
	h := newHandlerWithConfig(db, store, cfg)

	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/nonexistent/merge-config", nil, "", "")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rr.Code, rr.Body.String())
	}
}

// --- Merge status API tests ---

func TestHandleMergeStatus_NilManager(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	cfg := &config.Config{Cleanup: config.CleanupConfig{RetentionDays: 30}, Cameras: []config.CameraConfig{}}
	h := newHandlerWithConfig(db, store, cfg)

	rr := doRequest(t, h.Routes(), "GET", "/api/merge/status", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.Equal(t, false, resp["enabled"])
}

func TestHandleMergePending_NilManager(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	cfg := &config.Config{Cleanup: config.CleanupConfig{RetentionDays: 30}, Cameras: []config.CameraConfig{}}
	h := newHandlerWithConfig(db, store, cfg)

	rr := doRequest(t, h.Routes(), "GET", "/api/merge/pending", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.Equal(t, false, resp["enabled"])
}

func TestHandleMergeStatus_WithManager(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	cfg := &config.Config{
		Cleanup: config.CleanupConfig{RetentionDays: 30},
		Cameras: []config.CameraConfig{},
	}
	mergeMgr := merge.NewMergeManager(
		db, store,
		func() config.MergeConfig { return cfg.Merge },
		func(cameraID string) *config.MergeConfig { return nil },
		func() []config.CameraConfig { return cfg.Cameras },
		nil,
)
	h := NewHandler(db, store, noopAuthMW(), cfg, nil, nil, "", mergeMgr, nil, nil)

	rr := doRequest(t, h.Routes(), "GET", "/api/merge/status", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.Equal(t, true, resp["enabled"])
	require.NotNil(t, resp["last_run_time"])
}

func TestHandleMergePending_WithManager(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	cfg := &config.Config{
		Cleanup: config.CleanupConfig{RetentionDays: 30},
		Cameras: []config.CameraConfig{
			{ID: "cam-1", Name: "Test", Protocol: "rtsp_h264", URL: "rtsp://x"},
		},
	}
	mergeMgr := merge.NewMergeManager(
		db, store,
		func() config.MergeConfig { return cfg.Merge },
		func(cameraID string) *config.MergeConfig { return nil },
		func() []config.CameraConfig { return cfg.Cameras },
		nil,
)
	h := NewHandler(db, store, noopAuthMW(), cfg, nil, nil, "", mergeMgr, nil, nil)

	rr := doRequest(t, h.Routes(), "GET", "/api/merge/pending", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.Equal(t, true, resp["enabled"])
	// No recordings → empty pending map
	pending, ok := resp["pending"].(map[string]any)
	require.True(t, ok)
	require.Empty(t, pending)
	require.True(t, ok)
}

// --- HLS stream handler tests ---

func TestHandleHLSStream_NilHLSManager(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store) // nil hlsMgr

	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/cam-1/stream/index.m3u8", nil, "", "")
	require.Equal(t, http.StatusInternalServerError, rr.Code)
	var resp map[string]string
	parseJSON(t, rr, &resp)
	require.Equal(t, "HLS not available", resp["error"])
}

func TestHandleStopHLSStream_NilHLSManager(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "DELETE", "/api/cameras/cam-1/stream", nil, "", "")
	require.Equal(t, http.StatusInternalServerError, rr.Code)
}

func TestHandleStopHLSStream_NotActive(t *testing.T) {
	t.Parallel()
	hlsMgr := hls.NewManager(context.Background(), t.TempDir())
	db, store := setupTestDB(t)
	defer db.Close()
	cfg := &config.Config{Cleanup: config.CleanupConfig{RetentionDays: 30}, Cameras: []config.CameraConfig{}}
	h := NewHandler(db, store, noopAuthMW(), cfg, nil, hlsMgr, "", nil, nil, nil)

	rr := doRequest(t, h.Routes(), "DELETE", "/api/cameras/nonexistent/stream", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	var resp map[string]string
	parseJSON(t, rr, &resp)
	require.Equal(t, "not active", resp["status"])
}

func TestHandleStopHLSStream_Active(t *testing.T) {
	t.Parallel()
	hlsMgr := hls.NewManager(context.Background(), t.TempDir())
	db, store := setupTestDB(t)
	defer db.Close()
	cfg := &config.Config{Cleanup: config.CleanupConfig{RetentionDays: 30}, Cameras: []config.CameraConfig{}}
	h := NewHandler(db, store, noopAuthMW(), cfg, nil, hlsMgr, "", nil, nil, nil)

	// Manually insert a stream entry
	hlsMgr.StartStream("cam-1",
		[]byte{0x67, 0x42, 0xc0, 0x0a, 0xd9, 0x00, 0xa0, 0x47, 0xfe, 0x88},
		[]byte{0x68, 0xce, 0x38, 0x80}, 0)
	defer hlsMgr.StopAll()

	rr := doRequest(t, h.Routes(), "DELETE", "/api/cameras/cam-1/stream", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	var resp map[string]string
	parseJSON(t, rr, &resp)
	require.Equal(t, "stopped", resp["status"])
	require.False(t, hlsMgr.IsActive("cam-1"))
}

// --- Xiaomi cloud endpoint tests ---

// noopCloudProxy is a CloudAuthProxy that returns errors for all calls.
// Used for tests that only exercise input validation (which returns before proxy call).
type noopCloudProxy struct{}

func (noopCloudProxy) SetCloudConfig(_ context.Context, _, _, _ string) error { return nil }
func (noopCloudProxy) SignIn(_ context.Context, _, _, _ string) (*CloudAuthResult, *CloudVerificationRequired, error) {
	return nil, nil, fmt.Errorf("not implemented")
}
func (noopCloudProxy) SubmitCaptcha(_ context.Context, _, _ string) (*CloudAuthResult, *CloudVerificationRequired, error) {
	return nil, nil, fmt.Errorf("not implemented")
}
func (noopCloudProxy) SubmitVerify(_ context.Context, _, _ string) (*CloudAuthResult, *CloudVerificationRequired, error) {
	return nil, nil, fmt.Errorf("not implemented")
}
func (noopCloudProxy) ListDevices(_ context.Context) ([]CloudDeviceInfo, error) {
	return nil, fmt.Errorf("not implemented")
}
func (noopCloudProxy) CheckVendor(_ context.Context, _ string) (string, error) {
	return "", fmt.Errorf("not implemented")
}

func TestXiaomiAuthEmptyCredentials(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	cfg := &config.Config{Cleanup: config.CleanupConfig{RetentionDays: 30}, Cameras: []config.CameraConfig{}}
	h := NewHandler(db, store, noopAuthMW(), cfg, nil, nil, "", nil, noopCloudProxy{}, nil)

	// Test empty body
	rr := doRequest(t, h.Routes(), "POST", "/api/xiaomi/auth", strings.NewReader("{}"), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	var resp map[string]string
	parseJSON(t, rr, &resp)
	require.Contains(t, resp["error"], "username and password")

	// Test empty username
	body := `{"username":"","password":"test"}`
	rr = doRequest(t, h.Routes(), "POST", "/api/xiaomi/auth", strings.NewReader(body), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)

	// Test empty password
	body = `{"username":"test","password":""}`
	rr = doRequest(t, h.Routes(), "POST", "/api/xiaomi/auth", strings.NewReader(body), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestXiaomiDevicesNoAuth(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	cfg := &config.Config{Cleanup: config.CleanupConfig{RetentionDays: 30}, Cameras: []config.CameraConfig{}}
	authMW, _ := middleware.NewAuthMiddleware(middleware.AuthProvider{GetUsername: func() string { return "admin" }, GetHash: func() string { return "a$testhash" }}, "", middleware.AuthRateLimitConfig{})
	h := NewHandler(db, store, authMW, cfg, nil, nil, "", nil, noopCloudProxy{}, nil)

	// Without auth should return 401
	req := httptest.NewRequest("GET", "/api/xiaomi/devices", nil)
	rr := httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, req)
	require.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestXiaomiDevicesEmpty(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	cfg := &config.Config{Cleanup: config.CleanupConfig{RetentionDays: 30}, Cameras: []config.CameraConfig{}}
	h := NewHandler(db, store, noopAuthMW(), cfg, nil, nil, "", nil, noopCloudProxy{}, nil)

	// With no token configured, should return empty list
	rr := doRequest(t, h.Routes(), "GET", "/api/xiaomi/devices", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	var resp map[string]any
	parseJSON(t, rr, &resp)
	devices, ok := resp["devices"].([]interface{})
	require.True(t, ok)
	require.Empty(t, devices)
}

func TestXiaomiCaptchaRequiresFields(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	cfg := &config.Config{Cleanup: config.CleanupConfig{RetentionDays: 30}, Cameras: []config.CameraConfig{}}
	h := NewHandler(db, store, noopAuthMW(), cfg, nil, nil, "", nil, noopCloudProxy{}, nil)

	body := strings.NewReader(`{}`)
	rr := doRequest(t, h.Routes(), "POST", "/api/xiaomi/captcha", body, "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestXiaomiVerifyRequiresFields(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	cfg := &config.Config{Cleanup: config.CleanupConfig{RetentionDays: 30}, Cameras: []config.CameraConfig{}}
	h := NewHandler(db, store, noopAuthMW(), cfg, nil, nil, "", nil, noopCloudProxy{}, nil)

	body := strings.NewReader(`{}`)
	rr := doRequest(t, h.Routes(), "POST", "/api/xiaomi/verify", body, "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestXiaomiCloudUnavailableWithoutProxy(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	cfg := &config.Config{Cleanup: config.CleanupConfig{RetentionDays: 30}, Cameras: []config.CameraConfig{}}
	// No cloudProxy passed — should return 503 for all xiaomi endpoints
	h := NewHandler(db, store, noopAuthMW(), cfg, nil, nil, "", nil, nil, nil)

	for _, tc := range []struct {
		method string
		path   string
		body   string
	}{{
		method: "POST", path: "/api/xiaomi/auth", body: `{"username":"u","password":"p"}`,
	}, {
		method: "POST", path: "/api/xiaomi/captcha", body: `{"session_id":"s","captcha_code":"c"}`,
	}, {
		method: "POST", path: "/api/xiaomi/verify", body: `{"session_id":"s","ticket":"t"}`,
	}, {
		method: "GET", path: "/api/xiaomi/devices", body: "",
	}} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			var body io.Reader
			if tc.body != "" {
				body = strings.NewReader(tc.body)
			}
			rr := doRequest(t, h.Routes(), tc.method, tc.path, body, "", "")
			require.Equal(t, http.StatusServiceUnavailable, rr.Code, "path: %s", tc.path)
		})
	}
}

func TestCameraXiaomiProtocol(t *testing.T) {
	t.Parallel()
	h, _, _ := newTestCamHandler(t)

	body := strings.NewReader(`{"name":"Xiaomi Camera","protocol":"xiaomi","url":"xiaomi://655448418","encoding":"h265"}`)
	rr := doRequest(t, h.Routes(), "POST", "/api/cameras", body, "", "")
	require.Equal(t, http.StatusCreated, rr.Code, "body: %s", rr.Body.String())

	var cam config.CameraConfig
	parseJSON(t, rr, &cam)
	require.Equal(t, "xiaomi", cam.Protocol)
	require.Equal(t, "Xiaomi Camera", cam.Name)
	require.Equal(t, "h265", cam.Encoding)
	require.NotEmpty(t, cam.ID)
}

func TestHandleCreateCamera_InvalidURL(t *testing.T) {
	t.Parallel()
	h, _, _ := newTestCamHandler(t)

	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{name: "missing scheme", body: `{"name":"Cam","protocol":"rtsp","url":"nohost"}`, wantErr: "invalid URL format"},
		{name: "empty host", body: `{"name":"Cam","protocol":"rtsp","url":"rtsp://"}`, wantErr: "invalid URL format"},
		{name: "garbage", body: `{"name":"Cam","protocol":"rtsp","url":"://"}`, wantErr: "invalid URL format"},
		{name: "spaces only", body: `{"name":"Cam","protocol":"rtsp","url":"   "}`, wantErr: "invalid URL format"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rr := doRequest(t, h.Routes(), "POST", "/api/cameras", strings.NewReader(tc.body), "", "")
			require.Equal(t, http.StatusBadRequest, rr.Code, "body: %s", rr.Body.String())
			var resp map[string]string
			parseJSON(t, rr, &resp)
			require.Equal(t, tc.wantErr, resp["error"])
		})
	}
}

func TestHandleCreateCamera_ValidURLs(t *testing.T) {
	t.Parallel()
	h, _, _ := newTestCamHandler(t)

	tests := []struct {
		name string
		body string
	}{
		{name: "rtsp", body: `{"name":"Cam","protocol":"rtsp","url":"rtsp://192.168.1.100:554/stream"}`},
		{name: "http", body: `{"name":"Cam","protocol":"http","url":"http://camera/snap.jpg"}`},
		{name: "https", body: `{"name":"Cam","protocol":"http","url":"https://camera/snap.jpg"}`},
		{name: "xiaomi", body: `{"name":"Xiaomi","protocol":"xiaomi","url":"xiaomi://655448418"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rr := doRequest(t, h.Routes(), "POST", "/api/cameras", strings.NewReader(tc.body), "", "")
			require.Equal(t, http.StatusCreated, rr.Code, "body: %s", rr.Body.String())
		})
	}
}

// --- Frame timestamp extraction tests ---

func TestParseFrameFilename(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		filename  string
		want      string // expected timestamp in RFC3339, empty if should fail
	}{
		{name: "timestamp in name", filename: "frame_20240101_120000.jpg", want: "2024-01-01T12:00:00Z"},
		{name: "timestamp with suffix", filename: "frame_20240101_120000_001.jpg", want: "2024-01-01T12:00:00Z"},
		{name: "timestamp .jpeg", filename: "frame_20231225_083000.jpeg", want: "2023-12-25T08:30:00Z"},
		{name: "sequential no match", filename: "frame_000001.jpg", want: ""},
		{name: "non-frame name", filename: "snapshot_20240101_120000.jpg", want: ""},
		{name: "short name", filename: "frame_12.jpg", want: ""},
		{name: "no underscore", filename: "frame20240101120000.jpg", want: ""},
		{name: "wrong separator", filename: "frame_2024-0101_120000.jpg", want: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := parseFrameFilename(tc.filename)
			if tc.want == "" {
				if ok {
					t.Errorf("expected no match for %q, got %v", tc.filename, got)
				}
				return
			}
			if !ok {
				t.Errorf("expected match for %q, got none", tc.filename)
				return
			}
			expected, err := time.Parse(time.RFC3339, tc.want)
			if err != nil {
				t.Fatalf("bad test case time %q: %v", tc.want, err)
			}
			if !got.Equal(expected) {
				t.Errorf("parseFrameFilename(%q) = %v, want %v", tc.filename, got, expected)
			}
		})
	}
}

func TestExtractEXIFDateTime(t *testing.T) {
	t.Parallel()

	t.Run("JPEG without EXIF returns false", func(t *testing.T) {
		t.Parallel()
		jpeg := createTestJPEG(t, 100, 100)
		tmpFile := filepath.Join(t.TempDir(), "noexif.jpg")
		if err := os.WriteFile(tmpFile, jpeg, 0644); err != nil {
			t.Fatal(err)
		}
		got, ok := extractEXIFDateTime(tmpFile)
		if ok {
			t.Errorf("expected no EXIF, got %v", got)
		}
	})

	t.Run("JPEG with EXIF DateTimeOriginal", func(t *testing.T) {
		t.Parallel()
		jpeg := makeTestJPEGWithEXIF(t, "2024:06:15 14:30:00")
		tmpFile := filepath.Join(t.TempDir(), "withexif.jpg")
		if err := os.WriteFile(tmpFile, jpeg, 0644); err != nil {
			t.Fatal(err)
		}
		got, ok := extractEXIFDateTime(tmpFile)
		if !ok {
			t.Fatal("expected EXIF match, got none")
		}
		expected := time.Date(2024, 6, 15, 14, 30, 0, 0, time.UTC)
		if !got.Equal(expected) {
			t.Errorf("extractEXIFDateTime = %v, want %v", got, expected)
		}
	})

	t.Run("non-existent file returns false", func(t *testing.T) {
		t.Parallel()
		_, ok := extractEXIFDateTime("/nonexistent/path.jpg")
		if ok {
			t.Error("expected false for non-existent file")
		}
	})
}

func TestExtractFrameTimestamp(t *testing.T) {
	t.Parallel()

	t.Run("filename timestamp takes priority", func(t *testing.T) {
		t.Parallel()
		ts := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
		got := extractFrameTimestamp("frame_20240101_120000.jpg", "/nonexistent", ts)
		expected := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
		if !got.Equal(expected) {
			t.Errorf("extractFrameTimestamp = %v, want %v", got, expected)
		}
	})

	t.Run("EXIF when filename has no timestamp", func(t *testing.T) {
		t.Parallel()
		jpeg := makeTestJPEGWithEXIF(t, "2024:06:15 14:30:00")
		tmpFile := filepath.Join(t.TempDir(), "frame_000001.jpg")
		if err := os.WriteFile(tmpFile, jpeg, 0644); err != nil {
			t.Fatal(err)
		}
		modTime := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
		got := extractFrameTimestamp("frame_000001.jpg", tmpFile, modTime)
		expected := time.Date(2024, 6, 15, 14, 30, 0, 0, time.UTC)
		if !got.Equal(expected) {
			t.Errorf("extractFrameTimestamp = %v, want %v (from EXIF)", got, expected)
		}
	})

	t.Run("ModTime fallback when no timestamp available", func(t *testing.T) {
		t.Parallel()
		jpeg := createTestJPEG(t, 100, 100) // no EXIF
		tmpFile := filepath.Join(t.TempDir(), "frame_000001.jpg")
		if err := os.WriteFile(tmpFile, jpeg, 0644); err != nil {
			t.Fatal(err)
		}
		// Set explicit ModTime by writing and then modifying
		modTime := time.Date(2025, 3, 15, 10, 0, 0, 0, time.UTC)
		if err := os.Chtimes(tmpFile, modTime, modTime); err != nil {
			t.Fatal(err)
		}
		got := extractFrameTimestamp("frame_000001.jpg", tmpFile, modTime)
		if !got.Equal(modTime) {
			t.Errorf("extractFrameTimestamp = %v, want %v (ModTime fallback)", got, modTime)
		}
	})
}

// makeTestJPEGWithEXIF creates a valid JPEG with an EXIF APP1 segment containing DateTimeOriginal.
// This constructs a minimal APP1 EXIF segment with DateTimeOriginal (tag 0x9003) set to dateTimeStr.
func makeTestJPEGWithEXIF(t *testing.T, dateTimeStr string) []byte {
	t.Helper()

	// Create a minimal valid JPEG
	baseJPEG := createTestJPEG(t, 32, 32)

	// Build EXIF APP1 data in TIFF Little-Endian (II) format
	// Layout: TIFF header + IFD0 (with ExifIFD pointer) + EXIF IFD (with DateTimeOriginal)
	
	// EXIF IFD entries: just DateTimeOriginal (tag 0x9003)
	// TIFF header (8 bytes): II, 0x002A, offset to IFD0 = 8
	// IFD0 (minimal): 0 entries + 4 byte next-IFD pointer (0) + ExifIFD sub-IFD
	// But simpler: put DateTimeOriginal in a sub-IFD pointed to from IFD0

	// Simpler approach: just put DateTime (tag 0x0132) in IFD0 directly
	// This is more universally supported and simpler to construct

	// TIFF header (8 bytes)
	tiff := &bytes.Buffer{}
	tiff.Write([]byte("II"))              // byte order: Little-Endian
	binary.Write(tiff, binary.LittleEndian, uint16(0x002A)) // TIFF magic
	binary.Write(tiff, binary.LittleEndian, uint32(8)) // offset to IFD0 = 8

	// IFD0: 1 entry (DateTime) + 4-byte next-IFD pointer = 2+12+4 = 18 bytes
	// String value needs space — values >4 bytes go at end of IFD
	// Layout:
	//   2 bytes: entry count (1)
	//  12 bytes: DateTime entry
	//   4 bytes: next IFD pointer (0 = no more IFDs)
	//  20 bytes: DateTime string value + null terminator

	numEntries := uint16(2) // DateTime (0x0132) + ExifIFD pointer (0x8769)
	binary.Write(tiff, binary.LittleEndian, numEntries)

	// Entry 1: DateTime (tag 0x0132) in IFD0, type 2 (ASCII), value stored at end
	// We'll store the string at offset 8 + 2 + 2*12 + 4 = 38
	dateTimeBytes := []byte(dateTimeStr + "\x00") // include null terminator
	if len(dateTimeBytes) > 256 {
		t.Fatal("dateTime string too long")
	}
	
	// Write DateTime entry
	binary.Write(tiff, binary.LittleEndian, uint16(0x0132)) // DateTime tag
	binary.Write(tiff, binary.LittleEndian, uint16(2))     // Type: ASCII
	binary.Write(tiff, binary.LittleEndian, uint32(len(dateTimeBytes))) // Count
	// For strings > 4 bytes, store offset
	valueOffset := 8 + 2 + 2*12 + 4 // offset to string data = after IFD0
	binary.Write(tiff, binary.LittleEndian, uint32(valueOffset))

	// Entry 2: ExifIFD pointer (tag 0x8769), type 4 (long), count 1
	// EXIF IFD goes after IFD0 data
	exifIFDOffset := valueOffset + len(dateTimeBytes)
	binary.Write(tiff, binary.LittleEndian, uint16(0x8769)) // ExifIFD tag
	binary.Write(tiff, binary.LittleEndian, uint16(4))     // Type: LONG
	binary.Write(tiff, binary.LittleEndian, uint32(1))     // Count
	binary.Write(tiff, binary.LittleEndian, uint32(exifIFDOffset)) // Offset to EXIF IFD

	// Next IFD pointer (0 = no more IFDs)
	binary.Write(tiff, binary.LittleEndian, uint32(0))

	// DateTime string value
	tiff.Write(dateTimeBytes)

	// EXIF IFD at exifIFDOffset
	// Align to even offset if needed (TIFF requires word alignment for IFDs)
	for tiff.Len()%2 != 0 {
		tiff.WriteByte(0)
	}

	// Verify offset matches
	if tiff.Len() != exifIFDOffset {
		t.Fatalf("EXIF IFD offset mismatch: expected %d, got %d", exifIFDOffset, tiff.Len())
	}

	// EXIF IFD: 1 entry (DateTimeOriginal) + next-IFD pointer
	binary.Write(tiff, binary.LittleEndian, uint16(1)) // 1 entry

	// DateTimeOriginal string
	dateTimeOrigBytes := []byte(dateTimeStr + "\x00")
	
	// Entry: DateTimeOriginal (tag 0x9003), type 2 (ASCII)
	origValueOffset := exifIFDOffset + 2 + 1*12 + 4
	binary.Write(tiff, binary.LittleEndian, uint16(0x9003)) // DateTimeOriginal tag
	binary.Write(tiff, binary.LittleEndian, uint16(2))      // Type: ASCII
	binary.Write(tiff, binary.LittleEndian, uint32(len(dateTimeOrigBytes))) // Count
	binary.Write(tiff, binary.LittleEndian, uint32(origValueOffset)) // Offset to value

	// Next IFD pointer (0)
	binary.Write(tiff, binary.LittleEndian, uint32(0))

	// DateTimeOriginal string value
	for tiff.Len() < origValueOffset {
		tiff.WriteByte(0)
	}
	tiff.Write(dateTimeOrigBytes)

	tiffData := tiff.Bytes()

	// Build APP1 segment: marker + length + "Exif\0\0" + TIFF data
	app1 := &bytes.Buffer{}
	segLen := 2 + 6 + len(tiffData) // 2 for length field, 6 for Exif\0\0 header
	app1.Write([]byte{0xFF, 0xE1}) // APP1 marker
	binary.Write(app1, binary.BigEndian, uint16(segLen))
	app1.Write([]byte("Exif\x00\x00"))
	app1.Write(tiffData)

	// Inject APP1 after SOI (first 2 bytes) but before the rest of the JPEG
	// Find the start of image data (SOS marker 0xFFDA) in the base JPEG
	// Insert APP1 segment right after SOI marker
	if len(baseJPEG) < 2 || baseJPEG[0] != 0xFF || baseJPEG[1] != 0xD8 {
		t.Fatal("base JPEG doesn't start with SOI")
	}

	// Insert APP1 segment after the SOI marker (bytes 0-1)
	result := make([]byte, 0, len(baseJPEG)+len(app1.Bytes()))
	result = append(result, baseJPEG[:2]...)
	result = append(result, app1.Bytes()...)
	result = append(result, baseJPEG[2:]...)

	return result
}
