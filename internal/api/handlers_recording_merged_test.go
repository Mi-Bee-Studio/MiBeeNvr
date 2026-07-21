package api

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/stretchr/testify/require"
)

// TestHandleMergedRecording_NoMergePath verifies the endpoint returns 404
// when the recording has no merged output (the frontend relies on this 404
// to fall back to the JPEG frame viewer).
func TestHandleMergedRecording_NoMergePath(t *testing.T) {
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	seedRecording(t, db, &model.Recording{
		ID:        "rec-no-merge",
		CameraID:  "cam-x",
		FilePath:  "/tmp/rec-no-merge",
		Format:    model.FormatTimelapse,
		StartedAt: time.Now(),
		MergePath: "", // no merged output
	})

	rr := doRequest(t, h.Routes(), "GET", "/api/recordings/rec-no-merge/merged", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
}

// TestHandleMergedRecording_FileMissing verifies the endpoint returns 404
// when the DB has a merge_path but the file is absent on disk.
func TestHandleMergedRecording_FileMissing(t *testing.T) {
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	seedRecording(t, db, &model.Recording{
		ID:        "rec-missing-file",
		CameraID:  "cam-x",
		FilePath:  "/tmp/rec-missing-file",
		Format:    model.FormatTimelapse,
		StartedAt: time.Now(),
	})
	// merge_path is set via SetMergeResult (not INSERT).
	require.NoError(t, db.SetMergeResult(
		context.Background(), "rec-missing-file",
		"/tmp/does-not-exist-"+t.Name()+".mp4", "go",
	))

	rr := doRequest(t, h.Routes(), "GET", "/api/recordings/rec-missing-file/merged", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
	require.Contains(t, rr.Body.String(), "not available")
}

// TestHandleMergedRecording_NonMP4NoCodecHeader verifies that when the merged
// file is not a parseable MP4 (so mediaprobe.ProbeMP4 fails), the
// X-Timelapse-Codec header is simply not set — the file is still served
// (http.ServeFile doesn't care about codec), and the frontend will fall back
// to the JPEG frame viewer on a decode error.
func TestHandleMergedRecording_NonMP4NoCodecHeader(t *testing.T) {
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	// Write a non-MP4 file to the merge_path.
	dir := t.TempDir()
	outFile := filepath.Join(dir, "merged.bin")
	require.NoError(t, os.WriteFile(outFile, []byte("not an mp4"), 0o644))

	seedRecording(t, db, &model.Recording{
		ID:        "rec-non-mp4",
		CameraID:  "cam-x",
		FilePath:  "/tmp/rec-non-mp4",
		Format:    model.FormatTimelapse,
		StartedAt: time.Now(),
	})
	require.NoError(t, db.SetMergeResult(context.Background(), "rec-non-mp4", outFile, "go"))

	rr := doRequest(t, h.Routes(), "GET", "/api/recordings/rec-non-mp4/merged", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code, "file should still be served even if codec probe fails; body: %s", rr.Body.String())
	require.Equal(t, "", rr.Header().Get("X-Timelapse-Codec"),
		"X-Timelapse-Codec must be absent when the merged file is not a parseable MP4")
}
