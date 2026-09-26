package api

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestGetRecording_FallbackToMergedCovering pins the #903 fix: a stale source
// ID (deleted after rolling merge consumed it) resolves to the covering
// merged row instead of 404.
func TestGetRecording_FallbackToMergedCovering(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	base := time.Now().UTC().Truncate(time.Second)
	merged := makeRecording("merged-1", "cam-1", "h264", base.Add(-40*time.Second), true)
	merged.EndedAt = base.Add(20 * time.Second)
	merged.Duration = 60.0
	seedRecording(t, db, merged)

	// Stale source ID: a UnixNano stamp inside the merged row's span. The
	// row itself is absent (deleted by rolling merge).
	staleID := strconv.FormatInt(base.Add(-10*time.Second).UnixNano(), 10)
	rr := doRequest(t, h.Routes(), "GET", "/api/recordings/"+staleID, nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code, "stale source ID must fall back to the covering merged row")
	require.Equal(t, staleID, rr.Header().Get("X-Recording-Fallback-For"))
	var got map[string]interface{}
	parseJSON(t, rr, &got)
	require.Equal(t, "merged-1", got["id"])
}

func TestGetRecording_FallbackNoCover_Still404(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	base := time.Now().UTC().Truncate(time.Second)
	merged := makeRecording("merged-2", "cam-1", "h264", base.Add(-40*time.Second), true)
	merged.EndedAt = base.Add(-20 * time.Second)
	seedRecording(t, db, merged)

	// Stamp AFTER the row's span: nothing covers it.
	staleID := strconv.FormatInt(base.UnixNano(), 10)
	rr := doRequest(t, h.Routes(), "GET", "/api/recordings/"+staleID, nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestGetRecording_FallbackAmbiguousCameras(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	base := time.Now().UTC().Truncate(time.Second)
	for _, cam := range []string{"cam-a", "cam-b"} {
		m := makeRecording("merged-"+cam, cam, "h264", base.Add(-40*time.Second), true)
		m.EndedAt = base.Add(20 * time.Second)
		seedRecording(t, db, m)
	}

	staleID := strconv.FormatInt(base.Add(-10*time.Second).UnixNano(), 10)

	// Two cameras merged over the same moment: ambiguous without context.
	rr := doRequest(t, h.Routes(), "GET", "/api/recordings/"+staleID, nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)

	// camera_id disambiguates.
	rr = doRequest(t, h.Routes(), "GET", "/api/recordings/"+staleID+"?camera_id=cam-b", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	var got map[string]interface{}
	parseJSON(t, rr, &got)
	require.Equal(t, "merged-cam-b", got["id"])
}

// Non-stamp IDs (arbitrary strings) must skip the fallback entirely.
func TestGetRecording_FallbackNonStampID(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "GET", "/api/recordings/not-a-stamp", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
	require.Empty(t, rr.Header().Get("X-Recording-Fallback-For"))
}

// TestGetRecording_FallbackLineageAmbiguous pins the v40 exact resolution:
// two cameras' merged buckets cover the same moment, so the covering-time
// heuristic (#915) must refuse — but the fold that consumed THIS ID left
// lineage, which resolves it to the right camera without any camera hint.
func TestGetRecording_FallbackLineageAmbiguous(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	base := time.Now().UTC().Truncate(time.Second)
	// Distinct source stamps per camera (real recordings differ by µs).
	srcA := strconv.FormatInt(base.Add(-30*time.Second).UnixNano(), 10)
	srcB := strconv.FormatInt(base.Add(-25*time.Second).UnixNano(), 10)
	for _, tc := range []struct{ cam, srcID string }{{"cam-a", srcA}, {"cam-b", srcB}} {
		src := makeRecording(tc.srcID, tc.cam, "h264", base.Add(-40*time.Second), false)
		src.EndedAt = base.Add(-20 * time.Second)
		src.Duration = 20
		seedRecording(t, db, src)
		m := makeRecording("merged-"+tc.cam, tc.cam, "h264", base.Add(-40*time.Second), true)
		m.EndedAt = base.Add(20 * time.Second)
		m.Duration = 60.0
		// Fold through the real replace path so lineage is recorded exactly
		// as production folds do.
		require.NoError(t, db.MergeAndReplaceRecordings(context.Background(), m, []string{tc.srcID}))
	}

	// Both cameras cover the moment (heuristic alone would 404), yet cam-a's
	// stale source resolves via lineage.
	rr := doRequest(t, h.Routes(), "GET", "/api/recordings/"+srcA, nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code, "lineage must resolve the stale ID despite cross-camera ambiguity")
	require.Equal(t, srcA, rr.Header().Get("X-Recording-Fallback-For"))
	var got map[string]interface{}
	parseJSON(t, rr, &got)
	require.Equal(t, "merged-cam-a", got["id"])

	rr = doRequest(t, h.Routes(), "GET", "/api/recordings/"+srcB, nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	parseJSON(t, rr, &got)
	require.Equal(t, "merged-cam-b", got["id"])
}
