package api

// Camera lifecycle/CRUD handler coverage (#596): create validation branches,
// update (incl. stable_id / sub-stream / protocol-combo guards), archive-style
// delete, ingest start/stop, recording stats, push status, and the manual
// rediscover guard paths — against a real CameraManager seeded with passive
// push/GB28181 cameras (no dial-out, no SIP network).

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/camera"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/stretchr/testify/require"
)

// newCameraAPIEnv wires a Handler + real CameraManager with:
//   - ingest-cam: srt push camera (passive recorder — safe to start/stop)
//   - rtsp-cam:   pull camera that is NEVER started (no dial-out)
func newCameraAPIEnv(t *testing.T) (*Handler, *camera.CameraManager, string) {
	t.Helper()
	db, store := setupTestDB(t)
	t.Cleanup(func() { _ = db.Close() })

	cfgPath := filepath.Join(t.TempDir(), "mibee-nvr.yaml")
	cfg := &config.Config{Cameras: []config.CameraConfig{
		{ID: "ingest-cam", Name: "Ingest", Protocol: "srt", Encoding: "h264", StreamKey: "sk-1"},
		{ID: "rtsp-cam", Name: "Pull", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://127.0.0.1:1/stream"},
	}}
	cm := camera.NewCameraManager(cfg, store, db, cfgPath)

	ctx := context.Background()
	require.NoError(t, db.UpsertCamera(ctx, "ingest-cam", "Ingest", "srt", "h264", "", "", "", "", "", "", ""))
	require.NoError(t, db.UpsertCamera(ctx, "rtsp-cam", "Pull", "rtsp", "h264", "rtsp://127.0.0.1:1/stream", "", "", "", "", "", ""))

	h := TestHandler(db, store)
	t.Cleanup(h.Close)
	h.camMgr = cm
	h.config = cfg
	h.configPath = cfgPath
	return h, cm, cfgPath
}

func camDo(t *testing.T, h *Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *strings.Reader = strings.NewReader("")
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)
	return w
}

func TestCameraStartStopLifecycle(t *testing.T) {
	t.Parallel()
	h, cm, _ := newCameraAPIEnv(t)

	// Unknown camera: start maps CameraNotFoundError → 404; stop's
	// "camera not found" is a plain error → 500 (current contract).
	require.Equal(t, http.StatusNotFound, camDo(t, h, http.MethodPost, "/api/cameras/nope/start", "").Code)
	require.Equal(t, http.StatusInternalServerError, camDo(t, h, http.MethodPost, "/api/cameras/nope/stop", "").Code)

	// No manager → 503.
	h2, _, _ := newCameraAPIEnv(t)
	h2.camMgr = nil
	require.Equal(t, http.StatusServiceUnavailable, camDo(t, h2, http.MethodPost, "/api/cameras/ingest-cam/start", "").Code)
	require.Equal(t, http.StatusServiceUnavailable, camDo(t, h2, http.MethodPost, "/api/cameras/ingest-cam/stop", "").Code)

	// Start → started; second start → 409 already running; stop → stopped.
	w := camDo(t, h, http.MethodPost, "/api/cameras/ingest-cam/start", "")
	require.Equal(t, http.StatusOK, w.Code)
	require.NotNil(t, cm.GetRecorder("ingest-cam"))

	require.Equal(t, http.StatusConflict, camDo(t, h, http.MethodPost, "/api/cameras/ingest-cam/start", "").Code)

	w = camDo(t, h, http.MethodPost, "/api/cameras/ingest-cam/stop", "")
	require.Equal(t, http.StatusOK, w.Code)
	require.Nil(t, cm.GetRecorder("ingest-cam"))

	// Stopping a camera with no running recorder is a plain error → 500
	// (current contract: StopCamera reports "camera not found" without recorder).
	require.Equal(t, http.StatusInternalServerError, camDo(t, h, http.MethodPost, "/api/cameras/ingest-cam/stop", "").Code)
}

func TestCameraUpdate(t *testing.T) {
	t.Parallel()
	h, cm, cfgPath := newCameraAPIEnv(t)

	w := camDo(t, h, http.MethodPut, "/api/cameras/ingest-cam", `{"name":"Renamed"}`)
	require.Equal(t, http.StatusOK, w.Code)
	var row struct {
		Name string `json:"name"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &row))
	require.Equal(t, "Renamed", row.Name)

	// Persisted to config (snapshot + YAML on disk).
	cam := cm.GetCameraConfig("ingest-cam")
	require.NotNil(t, cam)
	require.Equal(t, "Renamed", cam.Name)
	disk, err := config.Load(cfgPath)
	require.NoError(t, err)
	found := false
	for i := range disk.Cameras {
		if disk.Cameras[i].ID == "ingest-cam" {
			require.Equal(t, "Renamed", disk.Cameras[i].Name)
			found = true
		}
	}
	require.True(t, found, "updated name must survive persistConfig")

	// Ingest field update persists via UpsertCameraIngest.
	newKey := "sk-2"
	require.Equal(t, http.StatusOK, camDo(t, h, http.MethodPut, "/api/cameras/ingest-cam", `{"stream_key":"sk-2"}`).Code)
	updated := cm.GetCameraConfig("ingest-cam")
	require.NotNil(t, updated)
	require.Equal(t, newKey, updated.StreamKey)

	// Validation branches.
	require.Equal(t, http.StatusBadRequest, camDo(t, h, http.MethodPut, "/api/cameras/ingest-cam", "not json").Code)
	require.Equal(t, http.StatusBadRequest, camDo(t, h, http.MethodPut, "/api/cameras/ingest-cam", `{"sub_stream_url":"http://x"}`).Code)
	require.Equal(t, http.StatusBadRequest, camDo(t, h, http.MethodPut, "/api/cameras/ingest-cam", `{"stable_id":"1.2.3.4"}`).Code) // IP rejected (#216)
	// Protocol-combo guard (#402 class): encoding h264 on an http camera is
	// invalid. (rtmp+h265 is valid since enhanced-RTMP ingest, #433.)
	require.Equal(t, http.StatusBadRequest, camDo(t, h, http.MethodPut, "/api/cameras/ingest-cam", `{"encoding":"h264","protocol":"http"}`).Code)
	// Unknown camera → 404.
	require.Equal(t, http.StatusNotFound, camDo(t, h, http.MethodPut, "/api/cameras/nope", `{"name":"x"}`).Code)
}

func TestCameraDeleteArchive(t *testing.T) {
	t.Parallel()
	h, cm, _ := newCameraAPIEnv(t)

	// Managed camera → archived via manager (removed from config).
	w := camDo(t, h, http.MethodDelete, "/api/cameras/ingest-cam", "")
	require.Equal(t, http.StatusOK, w.Code)
	require.Nil(t, cm.GetCameraConfig("ingest-cam"))
	ctx := context.Background()
	row, err := h.db.GetCamera(ctx, "ingest-cam")
	require.NoError(t, err)
	require.NotNil(t, row)
	require.True(t, row.Archived)

	// Second delete is idempotent ("archived").
	require.Equal(t, http.StatusOK, camDo(t, h, http.MethodDelete, "/api/cameras/ingest-cam", "").Code)
	// Unknown camera → 404.
	require.Equal(t, http.StatusNotFound, camDo(t, h, http.MethodDelete, "/api/cameras/nope", "").Code)

	// Orphaned DB-only camera (not in config) → archived directly in DB.
	h2, cm2, _ := newCameraAPIEnv(t)
	require.NoError(t, h2.db.UpsertCamera(ctx, "orphan", "Orphan", "rtsp", "h264", "rtsp://127.0.0.1:1/x", "", "", "", "", "", ""))
	require.Nil(t, cm2.GetCameraConfig("orphan"))
	require.Equal(t, http.StatusOK, camDo(t, h2, http.MethodDelete, "/api/cameras/orphan", "").Code)
	row, err = h2.db.GetCamera(ctx, "orphan")
	require.NoError(t, err)
	require.True(t, row.Archived)

	// No manager → DB-only archive path.
	h3, _, _ := newCameraAPIEnv(t)
	h3.camMgr = nil
	require.Equal(t, http.StatusOK, camDo(t, h3, http.MethodDelete, "/api/cameras/rtsp-cam", "").Code)
	row, err = h3.db.GetCamera(ctx, "rtsp-cam")
	require.NoError(t, err)
	require.True(t, row.Archived)
}

func TestCameraCreateValidation(t *testing.T) {
	t.Parallel()
	h, cm, _ := newCameraAPIEnv(t)

	createdID := func(t *testing.T, w *httptest.ResponseRecorder) string {
		t.Helper()
		var row struct {
			ID string `json:"id"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &row), w.Body.String())
		require.NotEmpty(t, row.ID)
		return row.ID
	}

	// Happy paths. srt: passive recorder, safe to auto-start.
	w := camDo(t, h, http.MethodPost, "/api/cameras", `{"name":"Push","protocol":"srt","stream_key":"sk-new"}`)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	pushID := createdID(t, w)
	require.NotNil(t, cm.GetCameraConfig(pushID))

	// gb28181 camera with payload (toConfig/toConfigPtr).
	w = camDo(t, h, http.MethodPost, "/api/cameras", `{"name":"GB","protocol":"gb28181","gb28181":{"device_id":"34020000001310000009","channel_id":"34020000001320000009","manufacturer":"Fake"}}`)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	gbID := createdID(t, w)
	gbCam := cm.GetCameraConfig(gbID)
	require.NotNil(t, gbCam)
	require.Equal(t, "34020000001310000009", gbCam.GB28181.DeviceID)
	require.Equal(t, "Fake", gbCam.GB28181.Manufacturer)

	// Validation matrix.
	require.Equal(t, http.StatusBadRequest, camDo(t, h, http.MethodPost, "/api/cameras", `{"protocol":"srt"}`).Code)                // no name
	require.Equal(t, http.StatusBadRequest, camDo(t, h, http.MethodPost, "/api/cameras", `{"name":"X"}`).Code)                      // no protocol
	require.Equal(t, http.StatusBadRequest, camDo(t, h, http.MethodPost, "/api/cameras", `{"name":"X","protocol":"ftp"}`).Code)     // bad protocol
	require.Equal(t, http.StatusBadRequest, camDo(t, h, http.MethodPost, "/api/cameras", `{"name":"X","protocol":"gb28181"}`).Code) // GB needs payload
	require.Equal(t, http.StatusBadRequest, camDo(t, h, http.MethodPost, "/api/cameras", `{"name":"X","protocol":"rtsp"}`).Code)    // url required
	require.Equal(t, http.StatusBadRequest, camDo(t, h, http.MethodPost, "/api/cameras", `{"name":"X","protocol":"rtsp","url":"not a url"}`).Code)
	require.Equal(t, http.StatusBadRequest, camDo(t, h, http.MethodPost, "/api/cameras", `{"name":"X","protocol":"rtsp","url":"rtsp://127.0.0.1:1/x","sub_stream_url":"http://y"}`).Code)
	require.Equal(t, http.StatusBadRequest, camDo(t, h, http.MethodPost, "/api/cameras", `{"name":"X","protocol":"rtsp_h264","url":"rtsp://127.0.0.1:1/x"}`).Code) // combined rejected
	require.Equal(t, http.StatusBadRequest, camDo(t, h, http.MethodPost, "/api/cameras", "not json").Code)

	// The created srt camera's recorder was started (passive ingest — no
	// dial-out) and its ingest fields persisted to the DB.
	require.NotNil(t, cm.GetRecorder(pushID))
}

func TestCameraRecordingStats(t *testing.T) {
	t.Parallel()
	h, _, _ := newCameraAPIEnv(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for i := range 2 {
		require.NoError(t, h.db.InsertRecording(ctx, &model.Recording{
			ID:        statsRecID(i),
			CameraID:  "ingest-cam",
			Format:    "h264",
			FilePath:  "/tmp/x" + statsRecID(i),
			StartedAt: now.Add(-time.Duration(i+1) * time.Hour),
			EndedAt:   now.Add(-time.Duration(i) * time.Hour),
			FileSize:  int64(100 * (i + 1)),
		}))
	}

	w := camDo(t, h, http.MethodGet, "/api/cameras/ingest-cam/stats", "")
	require.Equal(t, http.StatusOK, w.Code)
	var stats struct {
		RecordingCount int   `json:"recording_count"`
		TotalSize      int64 `json:"total_size"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &stats))
	require.Equal(t, 2, stats.RecordingCount)
	require.EqualValues(t, 300, stats.TotalSize)
}

func statsRecID(i int) string { return "stats-rec-" + string(rune('a'+i)) }

func TestCameraPushStatus(t *testing.T) {
	t.Parallel()
	h, _, _ := newCameraAPIEnv(t)

	w := camDo(t, h, http.MethodGet, "/api/cameras/ingest-cam/push-status", "")
	require.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		CameraID string        `json:"camera_id"`
		Targets  []interface{} `json:"targets"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, "ingest-cam", resp.CameraID)
	require.Empty(t, resp.Targets)

	h2, _, _ := newCameraAPIEnv(t)
	h2.camMgr = nil
	require.Equal(t, http.StatusInternalServerError, camDo(t, h2, http.MethodGet, "/api/cameras/ingest-cam/push-status", "").Code)
}

func TestCameraRediscover(t *testing.T) {
	t.Parallel()
	h, _, _ := newCameraAPIEnv(t)

	// Unknown camera → 404.
	require.Equal(t, http.StatusNotFound, camDo(t, h, http.MethodPost, "/api/cameras/nope/rediscover", "").Code)
	// No manager → 503.
	h2, _, _ := newCameraAPIEnv(t)
	h2.camMgr = nil
	require.Equal(t, http.StatusServiceUnavailable, camDo(t, h2, http.MethodPost, "/api/cameras/ingest-cam/rediscover", "").Code)
	// Non-ONVIF camera without stable_id → (false, nil) — 200 found:false, no scan.
	w := camDo(t, h, http.MethodPost, "/api/cameras/rtsp-cam/rediscover", "")
	require.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Found  bool   `json:"found"`
		Reason string `json:"reason"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.False(t, resp.Found)
	require.NotEmpty(t, resp.Reason)
}
