package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/stretchr/testify/assert"
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

func TestUpdateSettings_ValidTimezone(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	cfg := &config.Config{Cleanup: config.CleanupConfig{RetentionDays: 30}, Cameras: []config.CameraConfig{}}
	h := newHandlerWithConfig(db, store, cfg)

	body := `{"timezone":"Asia/Shanghai"}`
	rr := doRequest(t, h.Routes(), "PUT", "/api/settings", bytes.NewReader([]byte(body)), "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, "Asia/Shanghai", h.config.Timezone)
}

func TestUpdateSettings_TimezoneEmpty(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	cfg := &config.Config{Timezone: "Asia/Shanghai", Cleanup: config.CleanupConfig{RetentionDays: 30}, Cameras: []config.CameraConfig{}}
	h := newHandlerWithConfig(db, store, cfg)

	body := `{"timezone":""}`
	rr := doRequest(t, h.Routes(), "PUT", "/api/settings", bytes.NewReader([]byte(body)), "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, "", h.config.Timezone)
}

func TestUpdateSettings_TimezoneUTC(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	cfg := &config.Config{Timezone: "Asia/Shanghai", Cleanup: config.CleanupConfig{RetentionDays: 30}, Cameras: []config.CameraConfig{}}
	h := newHandlerWithConfig(db, store, cfg)

	body := `{"timezone":"UTC"}`
	rr := doRequest(t, h.Routes(), "PUT", "/api/settings", bytes.NewReader([]byte(body)), "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, "UTC", h.config.Timezone)
}

func TestUpdateSettings_InvalidTimezone(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	cfg := &config.Config{Cleanup: config.CleanupConfig{RetentionDays: 30}, Cameras: []config.CameraConfig{}}
	h := newHandlerWithConfig(db, store, cfg)

	body := `{"timezone":"Invalid/TZ"}`
	rr := doRequest(t, h.Routes(), "PUT", "/api/settings", bytes.NewReader([]byte(body)), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestUpdateSettings_ListenPort(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	cfg := &config.Config{Cleanup: config.CleanupConfig{RetentionDays: 30}, Cameras: []config.CameraConfig{}}
	h := newHandlerWithConfig(db, store, cfg)

	body := `{"server":{"listen":"8080"}}`
	rr := doRequest(t, h.Routes(), "PUT", "/api/settings", bytes.NewReader([]byte(body)), "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, ":8080", h.config.Server.Listen)
}

func TestUpdateSettings_ListenPortWithColon(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	cfg := &config.Config{Cleanup: config.CleanupConfig{RetentionDays: 30}, Cameras: []config.CameraConfig{}}
	h := newHandlerWithConfig(db, store, cfg)

	body := `{"server":{"listen":":8080"}}`
	rr := doRequest(t, h.Routes(), "PUT", "/api/settings", bytes.NewReader([]byte(body)), "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, ":8080", h.config.Server.Listen)
}

func TestUpdateSettings_InvalidListenPortRange(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	cfg := &config.Config{Cleanup: config.CleanupConfig{RetentionDays: 30}, Cameras: []config.CameraConfig{}}
	h := newHandlerWithConfig(db, store, cfg)

	body := `{"server":{"listen":"99999"}}`
	rr := doRequest(t, h.Routes(), "PUT", "/api/settings", bytes.NewReader([]byte(body)), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestUpdateSettings_InvalidListenPortNaN(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	cfg := &config.Config{Cleanup: config.CleanupConfig{RetentionDays: 30}, Cameras: []config.CameraConfig{}}
	h := newHandlerWithConfig(db, store, cfg)

	body := `{"server":{"listen":"abc"}}`
	rr := doRequest(t, h.Routes(), "PUT", "/api/settings", bytes.NewReader([]byte(body)), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestGetSettings_IncludesServerListen(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	cfg := &config.Config{Cleanup: config.CleanupConfig{RetentionDays: 30}, Cameras: []config.CameraConfig{}, Server: config.ServerConfig{Listen: ":9090"}}
	h := newHandlerWithConfig(db, store, cfg)

	rr := doRequest(t, h.Routes(), "GET", "/api/settings", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp map[string]interface{}
	parseJSON(t, rr, &resp)
	server, ok := resp["server"].(map[string]interface{})
	require.True(t, ok, "expected server object in response")
	require.Equal(t, ":9090", server["listen"])
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
	require.Len(t, protocols, 8) // rtsp, http, onvif, xiaomi, srt, whip, rtmp, gb28181

	// Xiaomi cameras authenticate via cloud account token, not per-camera
	// username/password — auth must be false so the form hides credentials.
	for _, p := range protocols {
		pm := p.(map[string]interface{})
		if pm["id"] == "xiaomi" {
			caps := pm["capabilities"].(map[string]interface{})
			assert.False(t, caps["auth"].(bool),
				"xiaomi protocol must have auth=false (cloud-token auth, not per-camera credentials)")
		}
	}
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
	require.Equal(t, "1h 0m 0s", formatUptime(1*time.Hour))
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

// --- streaming settings RTMP/SRT persistence (regression: the PUT body used
// to have no rtmp/srt fields, so the UI switches silently no-op'd) ---

func TestStreamingSettings_RTMPAndSRT(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := newHandlerWithConfig(db, store, &config.Config{})
	h.config.RTMP.Port = 1935
	h.config.SRT.Port = 9000

	body := `{"rtmp":{"enabled":true,"port":1936,"stream_keys":{"cam-1":"front-door"}},"srt":{"enabled":true,"port":9001,"streams":[{"camera_id":"cam-2","mode":"listener","address":"","passphrase":"pw","stream_id":"s1"}]}}`
	rr := doRequest(t, h.Routes(), "PUT", "/api/settings/streaming", bytes.NewReader([]byte(body)), "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	require.NotNil(t, h.config.RTMP.Enabled)
	require.True(t, *h.config.RTMP.Enabled)
	require.Equal(t, 1936, h.config.RTMP.Port)
	require.Equal(t, map[string]string{"cam-1": "front-door"}, h.config.RTMP.StreamKeys)

	require.NotNil(t, h.config.SRT.Enabled)
	require.True(t, *h.config.SRT.Enabled)
	require.Equal(t, 9001, h.config.SRT.Port)
	require.Len(t, h.config.SRT.Streams, 1)
	require.Equal(t, "cam-2", h.config.SRT.Streams[0].CameraID)

	// GET must round-trip what was stored.
	rr = doRequest(t, h.Routes(), "GET", "/api/settings/streaming", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	var got struct {
		RTMP struct {
			Enabled    bool              `json:"enabled"`
			Port       int               `json:"port"`
			StreamKeys map[string]string `json:"stream_keys"`
		} `json:"rtmp"`
		SRT struct {
			Enabled bool               `json:"enabled"`
			Port    int                `json:"port"`
			Streams []config.SRTStream `json:"streams"`
		} `json:"srt"`
	}
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&got))
	require.True(t, got.RTMP.Enabled)
	require.Equal(t, 1936, got.RTMP.Port)
	require.Equal(t, "front-door", got.RTMP.StreamKeys["cam-1"])
	require.True(t, got.SRT.Enabled)
	require.Equal(t, 9001, got.SRT.Port)
	require.Equal(t, "cam-2", got.SRT.Streams[0].CameraID)
}
