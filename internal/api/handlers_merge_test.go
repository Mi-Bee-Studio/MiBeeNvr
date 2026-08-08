package api

// Tests for handlers_merge.go — merge settings + per-camera merge config +
// merge status/reclassify endpoints (#232).

import (
	"bytes"
	"net/http"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/stretchr/testify/require"
)

// mergeTestHandler builds a Handler whose config is non-nil so the
// settings handlers can be exercised. Uses BasicAuth via Routes().
func mergeTestHandler(t *testing.T) *Handler {
	t.Helper()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)
	h.config = &config.Config{
		Merge: config.MergeConfig{
			Enabled:            true,
			CheckInterval:      "1h",
			WindowSize:         "24h",
			BatchLimit:         50,
			MinSegmentAge:      "2h",
			MinSegmentsToMerge: 3,
		},
	}
	h.configPath = t.TempDir() + "/test-config.yaml"
	return h
}

func TestMerge_GetSettings(t *testing.T) {
	t.Parallel()
	h := mergeTestHandler(t)
	rr := doRequest(t, h.Routes(), "GET", "/api/settings/merge", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp map[string]any
	parseJSON(t, rr, &resp)
	require.Equal(t, true, resp["enabled"])
	require.Equal(t, "1h", resp["check_interval"])
	require.EqualValues(t, 50, resp["batch_limit"])
}

func TestMerge_GetSettings_NoConfig(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store) // config stays nil
	rr := doRequest(t, h.Routes(), "GET", "/api/settings/merge", nil, "", "")
	require.Equal(t, http.StatusInternalServerError, rr.Code)
}

func TestMerge_UpdateSettings_InvalidDuration(t *testing.T) {
	t.Parallel()
	h := mergeTestHandler(t)
	// check_interval not a duration
	rr := doRequest(t, h.Routes(), "PUT", "/api/settings/merge",
		bytes.NewBufferString(`{"check_interval":"not-a-duration"}`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestMerge_UpdateSettings_BatchLimitTooSmall(t *testing.T) {
	t.Parallel()
	h := mergeTestHandler(t)
	rr := doRequest(t, h.Routes(), "PUT", "/api/settings/merge",
		bytes.NewBufferString(`{"batch_limit":0}`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestMerge_UpdateSettings_Success(t *testing.T) {
	t.Parallel()
	h := mergeTestHandler(t)
	rr := doRequest(t, h.Routes(), "PUT", "/api/settings/merge",
		bytes.NewBufferString(`{"enabled":false,"check_interval":"30m","window_size":"48h"}`), "", "")
	require.Equal(t, http.StatusOK, rr.Code, "body=%s", rr.Body.String())

	// Verify the change persisted in the in-memory config.
	require.False(t, h.config.Merge.Enabled)
	require.Equal(t, "30m", h.config.Merge.CheckInterval)
	require.Equal(t, "48h", h.config.Merge.WindowSize)
}

func TestMerge_UpdateSettings_InvalidBody(t *testing.T) {
	t.Parallel()
	h := mergeTestHandler(t)
	rr := doRequest(t, h.Routes(), "PUT", "/api/settings/merge",
		bytes.NewBufferString(`{bad json`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestMerge_GetCameraMergeConfig_NotFound(t *testing.T) {
	t.Parallel()
	h := mergeTestHandler(t)
	// Use the chi-routed path so {id} resolves.
	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/cam-missing/merge-config", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestMerge_GetCameraMergeConfig_Defaults(t *testing.T) {
	t.Parallel()
	h := mergeTestHandler(t)
	// Seed a camera with no per-camera merge override.
	require.NoError(t, h.db.UpsertCamera(t.Context(), "cam-1", "Cam 1", "rtsp", "h264", "rtsp://x", "", "", "", "", "", ""))

	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/cam-1/merge-config", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	var resp map[string]any
	parseJSON(t, rr, &resp)
	cust, _ := resp["customized"].(bool)
	require.False(t, cust, "no overrides → not customized")
}

func TestMerge_Status_NoManager(t *testing.T) {
	t.Parallel()
	h := mergeTestHandler(t) // mergeMgr is nil
	rr := doRequest(t, h.Routes(), "GET", "/api/merge/status", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	var resp map[string]any
	parseJSON(t, rr, &resp)
	require.Equal(t, false, resp["enabled"])
}

func TestMerge_Pending_NoManager(t *testing.T) {
	t.Parallel()
	h := mergeTestHandler(t)
	rr := doRequest(t, h.Routes(), "GET", "/api/merge/pending", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	var resp map[string]any
	parseJSON(t, rr, &resp)
	require.Equal(t, false, resp["enabled"])
}

func TestMerge_Reclassify(t *testing.T) {
	t.Parallel()
	h := mergeTestHandler(t)
	// Seed a recording, then mark it failed with NULL merge_error so reclassify picks it up.
	seedRecording(t, h.db, makeRecording("1786000000000000001", "cam-1", "h264", time.Now(), false))
	_, err := h.db.DB().ExecContext(t.Context(), `UPDATE recordings SET merge_status='failed', merge_error=NULL WHERE id='1786000000000000001'`)
	require.NoError(t, err)

	rr := doRequest(t, h.Routes(), "POST", "/api/merge/reclassify", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	var resp map[string]any
	parseJSON(t, rr, &resp)
	require.EqualValues(t, 1, resp["reclassified"])
}

func TestMerge_BackfillCamera_NotEnabled(t *testing.T) {
	t.Parallel()
	h := mergeTestHandler(t) // rollingMergeMgr is nil
	rr := doRequest(t, h.Routes(), "POST", "/api/cameras/cam-1/merge/backfill", nil, "", "")
	require.Equal(t, http.StatusServiceUnavailable, rr.Code)
}

func TestMerge_BackfillAll_NotEnabled(t *testing.T) {
	t.Parallel()
	h := mergeTestHandler(t)
	rr := doRequest(t, h.Routes(), "POST", "/api/merge/backfill", nil, "", "")
	require.Equal(t, http.StatusServiceUnavailable, rr.Code)
}
