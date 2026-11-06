package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/stretchr/testify/require"
)

// seedLineage inserts a merge_lineage row the way recordMergeLineageInTx does
// at fold time (source row deleted, provenance left behind).
func seedLineage(t *testing.T, db *storage.DB, sourceID, mergedID, cameraID string) {
	t.Helper()
	_, err := db.DB().ExecContext(context.Background(),
		`INSERT INTO merge_lineage(source_id, merged_id, camera_id, created_at) VALUES (?,?,?,?)`,
		sourceID, mergedID, cameraID, time.Now().UTC().Format("2006-01-02 15:04:05.999999999"))
	require.NoError(t, err)
}

// TestRecordingAIStatus_FoldedLineage pins the #46 contract: a Vision
// instance holds segment IDs from push time; when rolling merge folds the
// source before the write-back lands, the ai-status update must resolve to
// the merged bucket row instead of 404ing (1593 lost results/hour in the
// field). Exact lineage only — unknown IDs still 404.
func TestRecordingAIStatus_FoldedLineage(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)
	routes := withAPIKey("vision", h.Routes())

	seedRecording(t, db, makeRecording("fold-bucket", "cam-1", "mp4", time.Now(), true))
	seedLineage(t, db, "fold-src", "fold-bucket", "cam-1")

	// Folded source resolves: status lands on the bucket, response carries
	// the resolved id + the stale one in the fallback header.
	rr := doRequest(t, routes, http.MethodPatch, "/api/recordings/fold-src/ai-status",
		strings.NewReader(`{"ai_status":"completed"}`), "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, "fold-src", rr.Header().Get("X-Recording-Fallback-For"))
	require.Contains(t, rr.Body.String(), `"id":"fold-bucket"`)

	rec, err := db.GetRecording(context.Background(), "fold-bucket")
	require.NoError(t, err)
	require.Equal(t, "completed", rec.AIStatus)

	// Rank guard still holds through the lineage path: a later per-source
	// failed report must not regress the bucket's completed aggregate.
	rr = doRequest(t, routes, http.MethodPatch, "/api/recordings/fold-src/ai-status",
		strings.NewReader(`{"ai_status":"failed","ai_error":"boom"}`), "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	rec, err = db.GetRecording(context.Background(), "fold-bucket")
	require.NoError(t, err)
	require.Equal(t, "completed", rec.AIStatus)
}

// TestRecordingUpdate_FoldedAcknowledged: metadata writes on a folded ID are
// acknowledged (200) but never applied — duration/file_size are
// source-segment scoped and would corrupt the bucket row.
func TestRecordingUpdate_FoldedAcknowledged(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)
	routes := withAPIKey("vision", h.Routes())

	seedRecording(t, db, makeRecording("fold-bucket-2", "cam-1", "mp4", time.Now(), true))
	seedLineage(t, db, "fold-src-2", "fold-bucket-2", "cam-1")

	rr := doRequest(t, routes, http.MethodPatch, "/api/recordings/fold-src-2",
		strings.NewReader(`{"duration":42,"file_size":100}`), "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), `"status":"folded"`)

	rec, err := db.GetRecording(context.Background(), "fold-bucket-2")
	require.NoError(t, err)
	require.Equal(t, 300.0, rec.Duration, "bucket metadata must stay untouched")
	require.Equal(t, int64(1024), rec.FileSize)
}

// TestRecordingUnknownID_NoLineageStill404: resolution is exact provenance —
// an unknown ID with no lineage must keep 404ing.
func TestRecordingUnknownID_NoLineageStill404(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)
	routes := withAPIKey("vision", h.Routes())

	rr := doRequest(t, routes, http.MethodPatch, "/api/recordings/ghost/ai-status",
		strings.NewReader(`{"ai_status":"completed"}`), "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
}
