package api

// Tests for handlers_archive.go — archived-camera listing, archive group/
// recording deletion, and retention setting (#232).

import (
	"bytes"
	"net/http"
	"testing"

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
