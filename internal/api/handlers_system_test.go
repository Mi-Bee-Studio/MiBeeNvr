package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// --- handleGetSettings tests ---

func TestGetSettings_NilConfig(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	// TestHandler creates handler with nil config
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "GET", "/api/settings", nil, "", "")
	require.Equal(t, http.StatusInternalServerError, rr.Code)
}

// --- handleUpdateSettings tests ---

func TestUpdateSettings_BadJSON(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store) // config is nil

	rr := doRequest(t, h.Routes(), "PUT", "/api/settings", bytes.NewReader([]byte("not json")), "", "")
	// config is nil, returns 500 before parsing
	require.Equal(t, http.StatusInternalServerError, rr.Code)
}

func TestUpdateSettings_NilConfig(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	body := `{"cleanup":{"retention_days":7}}`
	rr := doRequest(t, h.Routes(), "PUT", "/api/settings", bytes.NewReader([]byte(body)), "", "")
	require.Equal(t, http.StatusInternalServerError, rr.Code)
}

// --- handleReadyz tests ---

func TestReadyz_OK(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "GET", "/api/readyz", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
}

// --- handleProtocols tests ---

func TestProtocols(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "GET", "/api/protocols", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp map[string]interface{}
	parseJSON(t, rr, &resp)
	protocols, ok := resp["protocols"].([]interface{})
	require.True(t, ok, "expected protocols array")
	require.Len(t, protocols, 4) // rtsp, http, onvif, xiaomi
}

// --- handleBackup tests ---

func TestBackup_Success(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "POST", "/api/backup", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp map[string]interface{}
	parseJSON(t, rr, &resp)
	require.Equal(t, "created", resp["status"])
	// Cleanup backup dir created in ./backups/
	os.RemoveAll("backups")
}

// --- handleListBackups tests ---

func TestListBackups_AfterBackup(t *testing.T) {
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	// First create a backup
	rr := doRequest(t, h.Routes(), "POST", "/api/backup", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	// Then list
	rr = doRequest(t, h.Routes(), "GET", "/api/backups", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	var backups []interface{}
	parseJSON(t, rr, &backups)
	require.Equal(t, 1, len(backups))

	// Cleanup backup dir created in ./backups/
	os.RemoveAll("backups")
}

// --- handleBatchDeleteRecordings tests ---

func TestBatchDeleteRecordings_EmptyIDs(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	body := `{"ids":[]}`
	rr := doRequest(t, h.Routes(), "POST", "/api/recordings/batch-delete", bytes.NewReader([]byte(body)), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestBatchDeleteRecordings_TooManyIDs(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	ids := make([]string, 101)
	for i := range ids {
		ids[i] = "rec"
	}
	bodyBytes, _ := json.Marshal(map[string][]string{"ids": ids})
	rr := doRequest(t, h.Routes(), "POST", "/api/recordings/batch-delete", bytes.NewReader(bodyBytes), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestBatchDeleteRecordings_InvalidBody(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "POST", "/api/recordings/batch-delete", bytes.NewReader([]byte("not json")), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

// --- formatUptime tests ---

func TestFormatUptime_Hours(t *testing.T) {
	t.Parallel()
	require.Equal(t, "1h 30m 0s", formatUptime(90*time.Minute))
}

func TestFormatUptime_Minutes(t *testing.T) {
	t.Parallel()
	require.Equal(t, "5m 30s", formatUptime(330*time.Second))
}

func TestFormatUptime_SecondsOnly(t *testing.T) {
	t.Parallel()
	require.Equal(t, "45s", formatUptime(45*time.Second))
}

func TestFormatUptime_Zero(t *testing.T) {
	t.Parallel()
	require.Equal(t, "0s", formatUptime(0))
}

func TestFormatUptime_ExactHour(t *testing.T) {
	t.Parallel()
	require.Equal(t, "1h 0m 0s", formatUptime(1 * time.Hour))
}

func TestFormatUptime_LargeDuration(t *testing.T) {
	t.Parallel()
	d := 72*time.Hour + 15*time.Minute + 30*time.Second
	require.Equal(t, "72h 15m 30s", formatUptime(d))
}

// --- handleStats tests ---

func TestStats_OK(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "GET", "/api/stats", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp map[string]interface{}
	parseJSON(t, rr, &resp)
	_, hasTotal := resp["total_bytes"]
	require.True(t, hasTotal, "expected total_bytes in response")
}

// --- handleStatsTrends tests ---

func TestStatsTrends_OK(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "GET", "/api/stats/trends", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
}

func TestStatsTrends_CustomDays(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "GET", "/api/stats/trends?days=14", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
}

// --- handleGetFeatures tests ---

func TestGetFeatures(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "GET", "/api/features", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
}

// --- handleUpdateFeatures tests ---

func TestUpdateFeatures_InvalidBody(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "PUT", "/api/features", bytes.NewReader([]byte("not json")), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
}
