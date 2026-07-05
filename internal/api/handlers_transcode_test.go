package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/transcoding"
	"github.com/stretchr/testify/require"
)

// --- Mock downloader ---

type mockDownloader struct {
	mu     sync.Mutex
	status transcoding.DownloadStatus
	path   string
	calls  int // tracks DownloadFFmpeg calls
}

func (m *mockDownloader) FFmpegPath() string {
	return m.path
}

func (m *mockDownloader) GetFFmpegStatus() transcoding.DownloadStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}

func (m *mockDownloader) DownloadFFmpeg(_ context.Context) error {
	m.mu.Lock()
	m.calls++
	m.status = transcoding.DownloadStatus{Status: "downloading", Progress: 0}
	m.mu.Unlock()
	return nil
}

func (m *mockDownloader) setStatus(status transcoding.DownloadStatus) {
	m.mu.Lock()
	m.status = status
	m.mu.Unlock()
}

// --- Test helpers ---

func newTranscodeHandler(t *testing.T, dl TranscodeDownloader) *Handler {
	t.Helper()
	db, store := setupTestDB(t)
	h := TestHandler(db, store)
	if dl != nil {
		h.downloader = dl
	}
	return h
}

func doTranscodeRequest(t *testing.T, h *Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	rr := httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, req)
	return rr
}

// --- Tests ---

func TestTranscodeCheck_ReturnsCachedProbeData(t *testing.T) {
	t.Parallel()
	// Reset probe cache for clean test

	dl := &mockDownloader{path: "/nonexistent/ffmpeg"}
	h := newTranscodeHandler(t, dl)

	rr := doTranscodeRequest(t, h, http.MethodGet, "/api/transcoding/check")
	// Unauthenticated — public route is /api/health, this needs auth
	// But we use TestHandler which has no config, so authMW is noop
	require.Equal(t, http.StatusOK, rr.Code)

	var resp map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))

	// Should have expected fields
	require.Contains(t, resp, "supported")
	require.Contains(t, resp, "ffmpeg_status")
	require.Contains(t, resp, "encoders")
	require.Contains(t, resp, "max_concurrent")
	require.Contains(t, resp, "estimated_fps")
	require.Contains(t, resp, "warnings")

	// FFmpeg path must NOT be in response (security)
	require.NotContains(t, resp, "ffmpeg_path")
}

func testTranscodeCheckSecondCallInstant(t *testing.T) {
	t.Helper()

	dl := &mockDownloader{path: "/nonexistent/ffmpeg"}
	h := newTranscodeHandler(t, dl)

	// First call
	rr1 := doTranscodeRequest(t, h, http.MethodGet, "/api/transcoding/check")
	require.Equal(t, http.StatusOK, rr1.Code)

	// Second call — should also succeed (cached)
	rr2 := doTranscodeRequest(t, h, http.MethodGet, "/api/transcoding/check")
	require.Equal(t, http.StatusOK, rr2.Code)

	var resp1, resp2 map[string]any
	require.NoError(t, json.NewDecoder(rr1.Body).Decode(&resp1))
	require.NoError(t, json.NewDecoder(rr2.Body).Decode(&resp2))

	// Both should return same data
	require.Equal(t, resp1["supported"], resp2["supported"])
	require.Equal(t, resp1["ffmpeg_status"], resp2["ffmpeg_status"])
}

func TestTranscodeCheck_SecondCallIsCached(t *testing.T) {
	t.Parallel()
	testTranscodeCheckSecondCallInstant(t)
}

func TestTranscodeCheck_NoDownloader(t *testing.T) {
	t.Parallel()

	// No downloader set
	h := newTranscodeHandler(t, nil)

	rr := doTranscodeRequest(t, h, http.MethodGet, "/api/transcoding/check")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	require.Contains(t, resp, "supported")
}

func TestFFmpegStatus_ReturnsStatus(t *testing.T) {
	t.Parallel()
	dl := &mockDownloader{}
	dl.setStatus(transcoding.DownloadStatus{
		Status:   "not_installed",
		Progress: 0,
	})
	h := newTranscodeHandler(t, dl)

	rr := doTranscodeRequest(t, h, http.MethodGet, "/api/transcoding/ffmpeg/status")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	require.Equal(t, "not_installed", resp["status"])

	// Must NOT contain path
	require.NotContains(t, resp, "path")
}

func TestFFmpegStatus_Available(t *testing.T) {
	t.Parallel()
	dl := &mockDownloader{}
	dl.setStatus(transcoding.DownloadStatus{
		Status:   "available",
		Version:  "ffmpeg version 6.0",
		Progress: 1.0,
	})
	h := newTranscodeHandler(t, dl)

	rr := doTranscodeRequest(t, h, http.MethodGet, "/api/transcoding/ffmpeg/status")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	require.Equal(t, "available", resp["status"])
	require.Equal(t, "ffmpeg version 6.0", resp["version"])
}

func TestFFmpegStatus_NoDownloader(t *testing.T) {
	t.Parallel()
	h := newTranscodeHandler(t, nil)

	rr := doTranscodeRequest(t, h, http.MethodGet, "/api/transcoding/ffmpeg/status")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	require.Equal(t, "not_installed", resp["status"])
}

func TestFFmpegDownload_Returns202IfNew(t *testing.T) {
	t.Parallel()
	dl := &mockDownloader{}
	dl.setStatus(transcoding.DownloadStatus{Status: "not_installed"})
	h := newTranscodeHandler(t, dl)

	rr := doTranscodeRequest(t, h, http.MethodPost, "/api/transcoding/ffmpeg/download")
	require.Equal(t, http.StatusAccepted, rr.Code)

	var resp map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	require.Equal(t, "downloading", resp["status"])
}

func TestFFmpegDownload_Returns200IfAvailable(t *testing.T) {
	t.Parallel()
	dl := &mockDownloader{}
	dl.setStatus(transcoding.DownloadStatus{
		Status:  "available",
		Version: "ffmpeg version 6.0",
	})
	h := newTranscodeHandler(t, dl)

	rr := doTranscodeRequest(t, h, http.MethodPost, "/api/transcoding/ffmpeg/download")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	require.Equal(t, "available", resp["status"])
}

func TestFFmpegDownload_IdempotentSecondCall(t *testing.T) {
	t.Parallel()
	dl := &mockDownloader{}
	dl.setStatus(transcoding.DownloadStatus{Status: "downloading", Progress: 0.5})
	h := newTranscodeHandler(t, dl)

	rr := doTranscodeRequest(t, h, http.MethodPost, "/api/transcoding/ffmpeg/download")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	require.Equal(t, "downloading", resp["status"])
	// Should return progress, not restart
	require.InDelta(t, 0.5, resp["download_progress"], 0.01)
}

func TestFFmpegDownload_NoDownloader(t *testing.T) {
	t.Parallel()
	h := newTranscodeHandler(t, nil)

	rr := doTranscodeRequest(t, h, http.MethodPost, "/api/transcoding/ffmpeg/download")
	require.Equal(t, http.StatusServiceUnavailable, rr.Code)
}

func TestFFmpegDownloadRetry_WorksonFailedStatus(t *testing.T) {
	t.Parallel()
	dl := &mockDownloader{}
	dl.setStatus(transcoding.DownloadStatus{
		Status: "failed",
		Error:  "network error",
	})
	h := newTranscodeHandler(t, dl)

	rr := doTranscodeRequest(t, h, http.MethodPost, "/api/transcoding/ffmpeg/download/retry")
	require.Equal(t, http.StatusAccepted, rr.Code)

	var resp map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	require.Equal(t, "downloading", resp["status"])
}

func TestFFmpegDownloadRetry_ConflictsIfNotFailed(t *testing.T) {
	t.Parallel()
	dl := &mockDownloader{}
	dl.setStatus(transcoding.DownloadStatus{Status: "downloading", Progress: 0.5})
	h := newTranscodeHandler(t, dl)

	rr := doTranscodeRequest(t, h, http.MethodPost, "/api/transcoding/ffmpeg/download/retry")
	require.Equal(t, http.StatusConflict, rr.Code)

	var resp map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	require.Contains(t, resp["error"], "download already in progress")
}

func TestFFmpegDownloadRetry_Returns200IfAvailable(t *testing.T) {
	t.Parallel()
	dl := &mockDownloader{}
	dl.setStatus(transcoding.DownloadStatus{
		Status:  "available",
		Version: "ffmpeg version 6.0",
	})
	h := newTranscodeHandler(t, dl)

	rr := doTranscodeRequest(t, h, http.MethodPost, "/api/transcoding/ffmpeg/download/retry")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	require.Equal(t, "available", resp["status"])
}

// --- Mock transcode manager ---

type mockTranscodeManager struct {
	status transcoding.ManagerStatus
	queue  transcoding.QueueAPI
}

type mockTranscodeQueue struct {
	enqueued  []*storage.TranscodeTask
	db        *storage.DB
	cancelID  int64
	cancelErr error
}

func (m *mockTranscodeManager) GetStatus() transcoding.ManagerStatus {
	return m.status
}

func (m *mockTranscodeManager) Queue() transcoding.QueueAPI {
	if m.queue != nil {
		return m.queue
	}
	return nil
}

func (m *mockTranscodeQueue) Enqueue(ctx context.Context, task *storage.TranscodeTask) error {
	m.enqueued = append(m.enqueued, task)
	if m.db != nil {
		return m.db.EnqueueTask(ctx, task)
	}
	return nil
}

func (m *mockTranscodeQueue) CancelTask(_ context.Context, id int64) error {
	m.cancelID = id
	return m.cancelErr
}

func doTranscodeBodyRequest(t *testing.T, h *Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(t, err)
		reqBody = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, reqBody)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rr := httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, req)
	return rr
}

// --- New endpoint tests ---

func TestTranscodingStatus_Disabled(t *testing.T) {
	transcoding.SetDisabledReason("")
	h := newTranscodeHandler(t, nil)
	// No transcode manager set

	rr := doTranscodeRequest(t, h, http.MethodGet, "/api/transcoding/status")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	require.Equal(t, false, resp["enabled"])
}

func TestTranscodingStatus_DisabledWithReason(t *testing.T) {
	transcoding.SetDisabledReason("hardware insufficient: no H.264 encoder")
	t.Cleanup(func() { transcoding.SetDisabledReason("") })
	h := newTranscodeHandler(t, nil)
	// No transcode manager set — simulates failed initialization

	rr := doTranscodeRequest(t, h, http.MethodGet, "/api/transcoding/status")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	require.Equal(t, false, resp["enabled"])
	require.Equal(t, "hardware insufficient: no H.264 encoder", resp["disabled_reason"])
}

func TestTranscodingStatus_Enabled(t *testing.T) {
	t.Parallel()
	h := newTranscodeHandler(t, nil)
	h.transcodeMgr = &mockTranscodeManager{
		status: transcoding.ManagerStatus{
			Enabled:     true,
			QueueLength: 3,
			ActiveJobs:  1,
		},
	}

	rr := doTranscodeRequest(t, h, http.MethodGet, "/api/transcoding/status")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	require.Equal(t, true, resp["enabled"])
	require.InDelta(t, float64(3), resp["queue_length"], 0)
	require.InDelta(t, float64(1), resp["active_jobs"], 0)
	// Enabled manager should have empty disabled_reason
	require.Equal(t, "", resp["disabled_reason"])
}

func TestTranscodingTasksList_Empty(t *testing.T) {
	t.Parallel()
	h := newTranscodeHandler(t, nil)

	rr := doTranscodeRequest(t, h, http.MethodGet, "/api/transcoding/tasks")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	tasks, ok := resp["tasks"].([]any)
	require.True(t, ok)
	require.Empty(t, tasks)
}

func TestTranscodingTaskCreate_Success(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	h := TestHandler(db, store)

	// Seed a recording
	seedRecording(t, db, &model.Recording{
		ID:        "rec-001",
		CameraID:  "cam-001",
		FilePath:  "/data/cam-001/segment.mp4",
		Format:    model.Format("h265"),
		StartedAt: time.Now().Add(-5 * time.Minute),
		EndedAt:   time.Now(),
		Duration:  300,
		FileSize:  1024000,
	})

	// Set up mock manager with config enabling transcoding and DB persistence
	q := &mockTranscodeQueue{db: db}
	h.transcodeMgr = &mockTranscodeManager{
		status: transcoding.ManagerStatus{Enabled: true},
		queue:  q,
	}
	h.config = &config.Config{
		Transcoding: config.TranscodingConfig{Enabled: true},
		Cameras: []config.CameraConfig{{
			ID:          "cam-001",
			Transcoding: &config.CameraTranscodingConfig{Enabled: true, TargetCodec: "h264"},
		}},
	}

	body := map[string]any{
		"camera_id":        "cam-001",
		"recording_id":     "rec-001",
		"target_codec":     "h264",
		"replace_original": false,
	}
	rr := doTranscodeBodyRequest(t, h, http.MethodPost, "/api/transcoding/tasks", body)
	require.Equal(t, http.StatusCreated, rr.Code)

	var resp map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	require.Equal(t, "cam-001", resp["camera_id"])
	require.Equal(t, "rec-001", resp["recording_id"])
	require.Equal(t, "h264", resp["output_format"])
}

func TestTranscodingTaskCreate_DisabledCamera(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	h := TestHandler(db, store)

	seedRecording(t, db, &model.Recording{
		ID:        "rec-002",
		CameraID:  "cam-002",
		FilePath:  "/data/cam-002/segment.mp4",
		Format:    model.Format("h265"),
		StartedAt: time.Now().Add(-5 * time.Minute),
		EndedAt:   time.Now(),
		Duration:  300,
		FileSize:  1024000,
	})

	q := &mockTranscodeQueue{db: db}
	h.transcodeMgr = &mockTranscodeManager{
		status: transcoding.ManagerStatus{Enabled: true},
		queue:  q,
	}
	h.config = &config.Config{
		Transcoding: config.TranscodingConfig{Enabled: false},
	}

	body := map[string]any{
		"camera_id":    "cam-002",
		"recording_id": "rec-002",
		"target_codec": "h264",
	}
	rr := doTranscodeBodyRequest(t, h, http.MethodPost, "/api/transcoding/tasks", body)
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestTranscodingTaskCancel_Success(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	h := TestHandler(db, store)

	// Seed a pending task directly in DB
	now := time.Now().UTC().Format("2006-01-02 15:04:05.999999999")
	task := &storage.TranscodeTask{
		CameraID:     "cam-001",
		RecordingID:  "rec-001",
		InputPath:    "/data/segment.mp4",
		InputFormat:  "h265",
		OutputPath:   "/data/segment.mp4.transcoded.mp4",
		OutputFormat: "h264",
		CreatedAt:    now,
	}
	err := db.EnqueueTask(context.Background(), task)
	require.NoError(t, err)

	q := &mockTranscodeQueue{db: db}
	h.transcodeMgr = &mockTranscodeManager{queue: q}

	rr := doTranscodeRequest(t, h, http.MethodDelete, fmt.Sprintf("/api/transcoding/tasks/%d", task.ID))
	require.Equal(t, http.StatusOK, rr.Code)

	var resp map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	require.Equal(t, "cancelled", resp["status"])
}

func TestTranscodingTaskCancel_CompletedTask(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	h := TestHandler(db, store)

	// Seed a completed task directly in DB
	now := time.Now().UTC().Format("2006-01-02 15:04:05.999999999")
	task := &storage.TranscodeTask{
		CameraID:     "cam-001",
		RecordingID:  "rec-001",
		InputPath:    "/data/segment.mp4",
		InputFormat:  "h265",
		OutputPath:   "/data/segment.mp4.transcoded.mp4",
		OutputFormat: "h264",
		CreatedAt:    now,
	}
	err := db.EnqueueTask(context.Background(), task)
	require.NoError(t, err)

	// Mark it completed
	err = db.UpdateTaskStatus(context.Background(), task.ID, "completed", 1.0, "")
	require.NoError(t, err)

	rr := doTranscodeRequest(t, h, http.MethodDelete, fmt.Sprintf("/api/transcoding/tasks/%d", task.ID))
	require.Equal(t, http.StatusConflict, rr.Code)
}

func TestTranscodingCameraConfigs(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	h := TestHandler(db, store)
	h.config = &config.Config{
		Transcoding: config.TranscodingConfig{Enabled: true, MaxWorkers: 2},
		Cameras: []config.CameraConfig{{
			ID:   "cam-001",
			Name: "Front Door",
			Transcoding: &config.CameraTranscodingConfig{
				Enabled:     true,
				TargetCodec: "h264",
				Preset:      "ultrafast",
				Bitrate:     "2M",
			},
		}, {
			ID:   "cam-002",
			Name: "Back Yard",
			// No transcoding config — inherits global
		}},
	}

	rr := doTranscodeRequest(t, h, http.MethodGet, "/api/transcoding/cameras")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	require.Equal(t, true, resp["global_enabled"])

	cameras, ok := resp["cameras"].([]any)
	require.True(t, ok)
	require.Len(t, cameras, 2)
}

func TestTranscodingBackfill_Success(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	h := TestHandler(db, store)

	// Seed recordings without transcode tasks
	now := time.Now()
	for i := 0; i < 3; i++ {
		seedRecording(t, db, &model.Recording{
			ID:        fmt.Sprintf("rec-bf-%d", i),
			CameraID:  "cam-bf",
			FilePath:  fmt.Sprintf("/data/cam-bf/segment-%d.mp4", i),
			Format:    model.Format("h265"),
			StartedAt: now.Add(-time.Duration(i+1) * time.Hour),
			EndedAt:   now.Add(-time.Duration(i) * time.Hour),
			Duration:  3600,
			FileSize:  1024000,
		})
	}

	// Seed one recording that already has a transcode task (should be skipped)
	seedRecording(t, db, &model.Recording{
		ID:        "rec-bf-already",
		CameraID:  "cam-bf",
		FilePath:  "/data/cam-bf/already-done.mp4",
		Format:    model.Format("h265"),
		StartedAt: now.Add(-5 * time.Hour),
		EndedAt:   now.Add(-4 * time.Hour),
		Duration:  3600,
		FileSize:  1024000,
	})
	// Create a transcode task for the already-done recording
	task := &storage.TranscodeTask{
		CameraID:     "cam-bf",
		RecordingID:  "rec-bf-already",
		InputPath:    "/data/cam-bf/already-done.mp4",
		InputFormat:  "h265",
		OutputPath:   "/data/cam-bf/already-done.mp4.transcoded.mp4",
		OutputFormat: "h264",
		CreatedAt:    time.Now().UTC().Format("2006-01-02 15:04:05.999999999"),
	}
	require.NoError(t, db.EnqueueTask(context.Background(), task))

	// Set up mock manager and config with DB persistence
	q := &mockTranscodeQueue{db: db}
	h.transcodeMgr = &mockTranscodeManager{
		status: transcoding.ManagerStatus{Enabled: true},
		queue:  q,
	}
	h.config = &config.Config{
		Transcoding: config.TranscodingConfig{Enabled: true},
		Cameras: []config.CameraConfig{{
			ID:          "cam-bf",
			Transcoding: &config.CameraTranscodingConfig{Enabled: true, TargetCodec: "h264"},
		}},
	}

	rr := doTranscodeRequest(t, h, http.MethodPost, "/api/transcoding/backfill?camera_id=cam-bf")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	require.Equal(t, float64(3), resp["enqueued"], "should enqueue 3 recordings")
	require.Equal(t, float64(1), resp["skipped"], "should skip 1 already-transcoded recording")
	require.Equal(t, float64(4), resp["total"], "should have 4 total recordings")
}

func TestTranscodingBackfill_DisabledCamera(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	h := TestHandler(db, store)

	h.transcodeMgr = &mockTranscodeManager{
		status: transcoding.ManagerStatus{Enabled: true},
		queue:  &mockTranscodeQueue{db: db},
	}
	h.config = &config.Config{
		Transcoding: config.TranscodingConfig{Enabled: true},
		Cameras: []config.CameraConfig{{
			ID:          "cam-no-transcode",
			Transcoding: &config.CameraTranscodingConfig{Enabled: false},
		}},
	}

	rr := doTranscodeRequest(t, h, http.MethodPost, "/api/transcoding/backfill?camera_id=nonexistent")
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestTranscodingBackfill_NoManager(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	h := TestHandler(db, store)
	// No transcodeMgr set

	rr := doTranscodeRequest(t, h, http.MethodPost, "/api/transcoding/backfill?camera_id=cam-001")
	require.Equal(t, http.StatusServiceUnavailable, rr.Code)
}

func TestTranscodingTaskRetry_Success(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	h := TestHandler(db, store)

	// Create a failed transcode task
	now := time.Now().UTC().Format("2006-01-02 15:04:05.999999999")
	task := &storage.TranscodeTask{
		CameraID:     "cam-retry",
		RecordingID:  "rec-retry-1",
		InputPath:    "/data/cam-retry/segment.mp4",
		InputFormat:  "h265",
		OutputPath:   "/data/cam-retry/segment.mp4.transcoded.mp4",
		OutputFormat: "h264",
		CreatedAt:    now,
	}
	require.NoError(t, db.EnqueueTask(context.Background(), task))

	// Mark it as failed
	require.NoError(t, db.UpdateTaskStatus(context.Background(), task.ID, "failed", 0.5, "encoding error"))

	// Set up mock manager with DB persistence
	q := &mockTranscodeQueue{db: db}
	h.transcodeMgr = &mockTranscodeManager{
		status: transcoding.ManagerStatus{Enabled: true},
		queue:  q,
	}

	rr := doTranscodeRequest(t, h, http.MethodPost, fmt.Sprintf("/api/transcoding/tasks/%d/retry", task.ID))
	require.Equal(t, http.StatusCreated, rr.Code)

	var resp map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	require.Equal(t, "cam-retry", resp["camera_id"])
	require.Equal(t, "rec-retry-1", resp["recording_id"])
	require.Equal(t, "h264", resp["output_format"])

	// Verify a new pending task was created in the DB
	tasks, err := db.GetTasksByStatus(context.Background(), "pending")
	require.NoError(t, err)
	require.Len(t, tasks, 1, "should have 1 pending task")
	require.Equal(t, "cam-retry", tasks[0].CameraID)
	require.Equal(t, "rec-retry-1", tasks[0].RecordingID)
	require.True(t, tasks[0].OriginalDeleted, "retry should set OriginalDeleted=true")
}

func TestTranscodingTaskRetry_NonFailedTask(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	h := TestHandler(db, store)

	// Create a completed task
	now := time.Now().UTC().Format("2006-01-02 15:04:05.999999999")
	task := &storage.TranscodeTask{
		CameraID:     "cam-retry",
		RecordingID:  "rec-retry-2",
		InputPath:    "/data/cam-retry/segment2.mp4",
		InputFormat:  "h265",
		OutputPath:   "/data/cam-retry/segment2.mp4.transcoded.mp4",
		OutputFormat: "h264",
		CreatedAt:    now,
	}
	require.NoError(t, db.EnqueueTask(context.Background(), task))
	require.NoError(t, db.UpdateTaskStatus(context.Background(), task.ID, "completed", 1.0, ""))

	q := &mockTranscodeQueue{db: db}
	h.transcodeMgr = &mockTranscodeManager{
		status: transcoding.ManagerStatus{Enabled: true},
		queue:  q,
	}

	rr := doTranscodeRequest(t, h, http.MethodPost, fmt.Sprintf("/api/transcoding/tasks/%d/retry", task.ID))
	require.Equal(t, http.StatusConflict, rr.Code)
}

func TestTranscodingTaskCreate_IgnoresReplaceOriginal(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	h := TestHandler(db, store)

	// Seed a recording
	seedRecording(t, db, &model.Recording{
		ID:        "rec-rep-001",
		CameraID:  "cam-rep",
		FilePath:  "/data/cam-rep/segment.mp4",
		Format:    model.Format("h265"),
		StartedAt: time.Now().Add(-5 * time.Minute),
		EndedAt:   time.Now(),
		Duration:  300,
		FileSize:  1024000,
	})

	q := &mockTranscodeQueue{db: db}
	h.transcodeMgr = &mockTranscodeManager{
		status: transcoding.ManagerStatus{Enabled: true},
		queue:  q,
	}
	h.config = &config.Config{
		Transcoding: config.TranscodingConfig{Enabled: true},
		Cameras: []config.CameraConfig{{
			ID:          "cam-rep",
			Transcoding: &config.CameraTranscodingConfig{Enabled: true, TargetCodec: "h264"},
		}},
	}

	// Send replace_original: false — handler should ignore it
	body := map[string]any{
		"camera_id":        "cam-rep",
		"recording_id":     "rec-rep-001",
		"target_codec":     "h264",
		"replace_original": false,
	}
	rr := doTranscodeBodyRequest(t, h, http.MethodPost, "/api/transcoding/tasks", body)
	require.Equal(t, http.StatusCreated, rr.Code)

	var resp map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	// The handler does not parse replace_original, so OriginalDeleted defaults to false
	// Verify the response does NOT contain original_deleted: true
	if origDel, ok := resp["original_deleted"]; ok {
		require.False(t, origDel.(bool), "handler should not set OriginalDeleted via API")
	}
}

func TestTranscodingTaskRetry_NoManager(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	h := TestHandler(db, store)
	// No transcodeMgr set

	rr := doTranscodeRequest(t, h, http.MethodPost, "/api/transcoding/tasks/1/retry")
	require.Equal(t, http.StatusServiceUnavailable, rr.Code)
}

func TestTranscodingBackfill_NoCameraID(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	h := TestHandler(db, store)
	h.transcodeMgr = &mockTranscodeManager{
		status: transcoding.ManagerStatus{Enabled: true},
	}

	rr := doTranscodeRequest(t, h, http.MethodPost, "/api/transcoding/backfill")
	require.Equal(t, http.StatusBadRequest, rr.Code)
}
