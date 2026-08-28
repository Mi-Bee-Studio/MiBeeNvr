package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// --- handleListRecordings with sorting ---

func TestListRecordings_SortByDuration(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	seedRecording(t, db, makeRecording("rec-1", "cam-1", "h264", now, false))
	seedRecording(t, db, makeRecording("rec-2", "cam-1", "h264", now.Add(-time.Hour), false))

	rr := doRequest(t, h.Routes(), "GET", "/api/recordings?sort_by=duration&order=asc", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp recordingsResponse
	parseJSON(t, rr, &resp)
	require.Len(t, resp.Recordings, 2)
}

func TestListRecordings_Search(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	seedRecording(t, db, makeRecording("rec-1", "front-door", "h264", now, false))
	seedRecording(t, db, makeRecording("rec-2", "back-yard", "h264", now, false))

	rr := doRequest(t, h.Routes(), "GET", "/api/recordings?search=front", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp recordingsResponse
	parseJSON(t, rr, &resp)
	require.Len(t, resp.Recordings, 1)
	require.Equal(t, "rec-1", resp.Recordings[0].ID)
}

func TestListRecordings_InvalidLimit(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "GET", "/api/recordings?limit=-1", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code) // negative limit ignored
}

func TestListRecordings_InvalidOffset(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "GET", "/api/recordings?offset=-1", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code) // negative offset ignored
}

// --- handleGetRecording edge cases ---

func TestGetRecording_EdgeCases(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	rec := makeRecording("rec-test", "cam-1", "h264", now, false)
	seedRecording(t, db, rec)

	rr := doRequest(t, h.Routes(), "GET", "/api/recordings/rec-test", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	var got map[string]interface{}
	parseJSON(t, rr, &got)
	require.Equal(t, "rec-test", got["id"])
	require.Equal(t, "cam-1", got["camera_id"])
}

// --- handleDownloadRecording tests ---

func TestDownloadRecording_Missing(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "GET", "/api/recordings/nonexistent/download", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestDownloadRecording_NoFilePath(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	rec := makeRecording("rec-nofile", "cam-1", "h264", now, false)
	rec.FilePath = ""
	seedRecording(t, db, rec)

	rr := doRequest(t, h.Routes(), "GET", "/api/recordings/rec-nofile/download", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestDownloadRecording_FileNotFound(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	rec := makeRecording("rec-gone", "cam-1", "h264", now, false)
	rec.FilePath = filepath.Join(store.RootDir(), "nonexistent.mp4")
	seedRecording(t, db, rec)

	rr := doRequest(t, h.Routes(), "GET", "/api/recordings/rec-gone/download", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestDownloadRecording_Success(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	rec := makeRecording("rec-dl", "cam-1", "h264", now, false)
	rec.FilePath = filepath.Join(store.RootDir(), "rec-dl.mp4")
	require.NoError(t, os.WriteFile(rec.FilePath, []byte("test-video-data"), 0o644))
	seedRecording(t, db, rec)

	rr := doRequest(t, h.Routes(), "GET", "/api/recordings/rec-dl/download", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, "test-video-data", rr.Body.String())
}

// --- handleListFrames tests ---

func TestListFrames_NoRecording(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "GET", "/api/recordings/nonexistent/frames", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestListFrames_NotMJPEG(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	rec := makeRecording("rec-h264", "cam-1", "h264", now, false)
	seedRecording(t, db, rec)

	rr := doRequest(t, h.Routes(), "GET", "/api/recordings/rec-h264/frames", nil, "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestListFrames_MJPEGDirWithImages(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	dir := filepath.Join(store.RootDir(), "mjpeg-frames")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "frame001.jpg"), []byte("jpg-data"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "frame002.jpg"), []byte("jpg-data"), 0o644))

	rec := makeRecording("rec-mjpeg", "cam-1", "mjpeg", now, false)
	rec.FilePath = dir
	seedRecording(t, db, rec)

	rr := doRequest(t, h.Routes(), "GET", "/api/recordings/rec-mjpeg/frames", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp map[string]interface{}
	parseJSON(t, rr, &resp)
	frames, ok := resp["frames"].([]interface{})
	require.True(t, ok)
	require.Len(t, frames, 2)
}

func TestListFrames_MJPEGNotDir(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	filePath := filepath.Join(store.RootDir(), "single.jpg")
	require.NoError(t, os.WriteFile(filePath, []byte("jpg-data"), 0o644))

	rec := makeRecording("rec-single", "cam-1", "mjpeg", now, false)
	rec.FilePath = filePath
	seedRecording(t, db, rec)

	rr := doRequest(t, h.Routes(), "GET", "/api/recordings/rec-single/frames", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code) // not a directory
}

// --- handleDeleteRecording with path traversal ---

func TestDeleteRecording_PathTraversal(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	rec := makeRecording("rec-trav", "cam-1", "h264", now, false)
	rec.FilePath = "../../../etc/passwd"
	seedRecording(t, db, rec)

	// Delete should still work (deletes from DB), file deletion fails gracefully
	rr := doRequest(t, h.Routes(), "DELETE", "/api/recordings/rec-trav", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
}

// --- Batch delete with actual recordings ---

func TestBatchDeleteRecordings_Success(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	// Create files
	rec1 := makeRecording("batch-1", "cam-1", "h264", now, false)
	rec1.FilePath = filepath.Join(store.RootDir(), "batch-1.mp4")
	require.NoError(t, os.WriteFile(rec1.FilePath, []byte("data1"), 0o644))
	seedRecording(t, db, rec1)

	rec2 := makeRecording("batch-2", "cam-1", "h264", now, false)
	rec2.FilePath = filepath.Join(store.RootDir(), "batch-2.mp4")
	require.NoError(t, os.WriteFile(rec2.FilePath, []byte("data2"), 0o644))
	seedRecording(t, db, rec2)

	body, _ := json.Marshal(map[string][]string{"ids": {"batch-1", "batch-2"}})
	rr := doRequest(t, h.Routes(), "POST", "/api/recordings/batch-delete", bytes.NewReader(body), "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	// Verify deleted from DB
	got, _ := db.GetRecording(context.Background(), "batch-1")
	require.Nil(t, got)
	got2, _ := db.GetRecording(context.Background(), "batch-2")
	require.Nil(t, got2)
}

// --- handleDeleteRecording must also delete the merged MP4 (merge_path) ---
// Regression test for issue #117: deleting a recording left the merged-output
// MP4 (recordings.merge_path) on disk permanently, because the handler only
// removed file_path. The merged MP4 is the largest artifact and the file
// playback actually loads.

func TestDeleteRecording_DeletesMergePath(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	rec := makeRecording("rec-merge", "cam-1", "h264", now, false)
	rec.FilePath = filepath.Join(store.RootDir(), "rec-merge-src.mp4")
	require.NoError(t, os.WriteFile(rec.FilePath, []byte("src"), 0o644))
	mergePath := filepath.Join(store.RootDir(), "rec-merge-merged.mp4")
	require.NoError(t, os.WriteFile(mergePath, []byte("merged"), 0o644))
	seedRecording(t, db, rec)
	// Set the merge_path on the row (mirrors how the rolling merge writes it).
	require.NoError(t, db.SetMergeResult(context.Background(), rec.ID, mergePath, "go"))

	rr := doRequest(t, h.Routes(), "DELETE", "/api/recordings/rec-merge", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	// Both on-disk files must be gone.
	_, err := os.Stat(rec.FilePath)
	require.True(t, os.IsNotExist(err), "source file should be deleted, got err=%v", err)
	_, err = os.Stat(mergePath)
	require.True(t, os.IsNotExist(err), "merged file (merge_path) should be deleted, got err=%v", err)

	got, _ := db.GetRecording(context.Background(), "rec-merge")
	require.Nil(t, got)
}

func TestDeleteRecording_MissingMergePath_NoError(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	// Recording with an empty merge_path (never merged) — deletion must still
	// succeed and clean up the source file.
	now := time.Now().UTC().Truncate(time.Second)
	rec := makeRecording("rec-nomerge", "cam-1", "h264", now, false)
	rec.FilePath = filepath.Join(store.RootDir(), "rec-nomerge.mp4")
	require.NoError(t, os.WriteFile(rec.FilePath, []byte("data"), 0o644))
	seedRecording(t, db, rec)

	rr := doRequest(t, h.Routes(), "DELETE", "/api/recordings/rec-nomerge", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	_, err := os.Stat(rec.FilePath)
	require.True(t, os.IsNotExist(err))
}

func TestBatchDeleteRecordings_DeletesMergePath(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	rec := makeRecording("batch-merge", "cam-1", "h264", now, false)
	rec.FilePath = filepath.Join(store.RootDir(), "batch-merge-src.mp4")
	require.NoError(t, os.WriteFile(rec.FilePath, []byte("src"), 0o644))
	mergePath := filepath.Join(store.RootDir(), "batch-merge-merged.mp4")
	require.NoError(t, os.WriteFile(mergePath, []byte("merged"), 0o644))
	seedRecording(t, db, rec)
	require.NoError(t, db.SetMergeResult(context.Background(), rec.ID, mergePath, "go"))

	body, _ := json.Marshal(map[string][]string{"ids": {"batch-merge"}})
	rr := doRequest(t, h.Routes(), "POST", "/api/recordings/batch-delete", bytes.NewReader(body), "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	// Both files reclaimed.
	_, err := os.Stat(rec.FilePath)
	require.True(t, os.IsNotExist(err), "source file should be deleted, got err=%v", err)
	_, err = os.Stat(mergePath)
	require.True(t, os.IsNotExist(err), "merged file (merge_path) should be deleted, got err=%v", err)
	got, _ := db.GetRecording(context.Background(), "batch-merge")
	require.Nil(t, got)
}

func TestListRecordings_ArchivedFilter(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	// Two cameras: cam-live keeps an active recording; cam-arch's recordings
	// are archived via the real storage path (InsertRecording always writes
	// archived=0, mirroring production where archiving happens post-insert).
	seedRecording(t, db, makeRecording("rec-live", "cam-live", "h264", now, false))
	seedRecording(t, db, makeRecording("rec-arch", "cam-arch", "h264", now.Add(-time.Hour), false))
	n, err := db.ArchiveAllRecordings(t.Context(), "cam-arch")
	require.NoError(t, err)
	require.Equal(t, int64(1), n)

	// Default: active recordings only (cam-arch hidden).
	rr := doRequest(t, h.Routes(), "GET", "/api/recordings", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	var resp recordingsResponse
	parseJSON(t, rr, &resp)
	require.Len(t, resp.Recordings, 1)
	require.Equal(t, "rec-live", resp.Recordings[0].ID)

	// archived=true: the archive view must reach archived rows — the param
	// used to be dropped by handleListRecordings, hiding all archived data.
	rr = doRequest(t, h.Routes(), "GET", "/api/recordings?archived=true", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	resp = recordingsResponse{}
	parseJSON(t, rr, &resp)
	require.Len(t, resp.Recordings, 1)
	require.Equal(t, "rec-arch", resp.Recordings[0].ID)
}
