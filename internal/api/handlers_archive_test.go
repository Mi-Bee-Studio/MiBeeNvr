package api

// Tests for handlers_archive.go — archived-camera listing, archive group/
// recording deletion, and retention setting (#232).

import (
	"bytes"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestArchive_ListEmpty(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "GET", "/api/archives", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	var resp struct {
		Archives []any `json:"archives"`
	}
	parseJSON(t, rr, &resp)
	require.Empty(t, resp.Archives)
}

func TestArchive_ListRecordings_NoGroup(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "GET", "/api/archives/cam-none/recordings", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	var resp struct {
		Recordings []any `json:"recordings"`
		Total      int   `json:"total"`
	}
	parseJSON(t, rr, &resp)
	require.Equal(t, 0, resp.Total)
}

func TestArchive_DeleteGroup_NotArchived(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)
	require.NoError(t, h.db.UpsertCamera(t.Context(), "cam-1", "Cam 1", "rtsp", "h264", "rtsp://x", "", "", "", "", "", ""))

	// Camera exists but is NOT archived → 404.
	rr := doRequest(t, h.Routes(), "DELETE", "/api/archives/cam-1", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestArchive_DeleteRecording_NotFound(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "DELETE", "/api/archives/cam-1/recordings/rec-missing", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestArchive_SetRetention_InvalidBody(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "PUT", "/api/archives/cam-1/retention",
		bytes.NewBufferString(`{bad json`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestArchive_SetRetention_Negative(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "PUT", "/api/archives/cam-1/retention",
		bytes.NewBufferString(`{"retention_days":-1}`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestArchive_SetRetention_NotFound(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "PUT", "/api/archives/cam-missing/retention",
		bytes.NewBufferString(`{"retention_days":30}`), "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestArchive_SetRetention_Success(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)
	// Seed an archived camera.
	require.NoError(t, h.db.UpsertCamera(t.Context(), "cam-arch", "Archived", "rtsp", "h264", "rtsp://x", "", "", "", "", "", ""))
	_, err := h.db.DB().ExecContext(t.Context(), `UPDATE cameras SET archived=1 WHERE id='cam-arch'`)
	require.NoError(t, err)

	rr := doRequest(t, h.Routes(), "PUT", "/api/archives/cam-arch/retention",
		bytes.NewBufferString(`{"retention_days":90}`), "", "")
	require.Equal(t, http.StatusOK, rr.Code, "body=%s", rr.Body.String())
}

func TestArchive_DeleteGroup_Returns202(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)

	// Seed an archived camera with one archived recording.
	require.NoError(t, h.db.UpsertCamera(t.Context(), "cam-arch", "Archived", "rtsp", "h264", "rtsp://x", "", "", "", "", "", ""))
	_, err := h.db.DB().ExecContext(t.Context(), `UPDATE cameras SET archived=1 WHERE id='cam-arch'`)
	require.NoError(t, err)

	now := time.Now().UTC()
	require.NoError(t, h.db.InsertRecording(t.Context(), &model.Recording{
		ID: "rec-arch-1", CameraID: "cam-arch", FilePath: "/arch/rec1.mp4", Format: model.FormatH264,
		StartedAt: now, EndedAt: now.Add(time.Minute), Duration: 60, FileSize: 1024,
	}))
	_, err = h.db.DB().ExecContext(t.Context(), `UPDATE recordings SET archived=1 WHERE id='rec-arch-1'`)
	require.NoError(t, err)

	rr := doRequest(t, h.Routes(), "DELETE", "/api/archives/cam-arch", nil, "", "")
	require.Equal(t, http.StatusAccepted, rr.Code, "body=%s", rr.Body.String())
	var resp map[string]string
	parseJSON(t, rr, &resp)
	require.Equal(t, "deleting", resp["status"])

	// Camera row removed immediately.
	cam, err := h.db.GetCamera(t.Context(), "cam-arch")
	require.NoError(t, err)
	require.Nil(t, cam)

	// A pending cleanup task was created with the archived stats.
	active, err := h.db.ListActiveArchiveCleanupTasks(t.Context())
	require.NoError(t, err)
	require.Len(t, active, 1)
	require.Equal(t, "cam-arch", active[0].CameraID)
	require.Equal(t, "pending", active[0].Status)
	require.Equal(t, 1, active[0].RecordingCount)
	require.Equal(t, int64(1024), active[0].TotalSize)
}

func TestArchive_GetCleanupStatus(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)

	// Create a pending task directly.
	require.NoError(t, h.db.CreateArchiveCleanupTask(t.Context(), storage.ArchiveCleanupTask{
		CameraID: "cam-1", CameraName: "Cam 1", RecordingCount: 3, TotalSize: 1024, Status: "pending",
	}))

	rr := doRequest(t, h.Routes(), "GET", "/api/archives/cleanup-status", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	var resp map[string]any
	parseJSON(t, rr, &resp)

	active, ok := resp["active"].([]any)
	require.True(t, ok, "active must be an array: %v", resp["active"])
	require.Len(t, active, 1)
	task := active[0].(map[string]any)
	require.Equal(t, "cam-1", task["CameraID"])
	require.Equal(t, "pending", task["Status"])
	require.Equal(t, "Cam 1", task["CameraName"])
	require.Equal(t, float64(3), task["RecordingCount"])
	require.Equal(t, float64(1024), task["TotalSize"])

	// No completed tasks yet → recent empty.
	recent, ok := resp["recent"].([]any)
	require.True(t, ok || resp["recent"] == nil, "recent must be an array or null: %v", resp["recent"])
	require.Empty(t, recent)
}

func TestArchive_DeleteGroup_Concurrent(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)

	require.NoError(t, h.db.UpsertCamera(t.Context(), "cam-arch", "Archived", "rtsp", "h264", "rtsp://x", "", "", "", "", "", ""))
	_, err := h.db.DB().ExecContext(t.Context(), `UPDATE cameras SET archived=1 WHERE id='cam-arch'`)
	require.NoError(t, err)

	start := make(chan struct{})
	codes := make([]int, 2)
	var wg sync.WaitGroup
	for i := range 2 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			rr := doRequest(t, h.Routes(), "DELETE", "/api/archives/cam-arch", nil, "", "")
			codes[i] = rr.Code
		}(i)
	}
	close(start)
	wg.Wait()

	// Both succeed — the loser maps sql.ErrNoRows to success.
	require.Equal(t, http.StatusAccepted, codes[0], "first delete should return 202")
	require.Equal(t, http.StatusAccepted, codes[1], "concurrent delete should return 202")
}

func TestArchive_DeleteGroup_NonExistent(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)

	// Camera does not exist at all → 404.
	rr := doRequest(t, h.Routes(), "DELETE", "/api/archives/cam-missing", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
}
