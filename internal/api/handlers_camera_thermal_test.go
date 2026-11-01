package api

// Thermal imaging channel (dual-spectrum): imaging_type create/update at the
// API boundary — validation, response echo (YAML overlay), and the vision
// skip default policy side effect.

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCreateCameraImagingType(t *testing.T) {
	t.Helper()
	t.Parallel()
	h, cm, _ := newCameraAPIEnv(t)

	// Invalid value rejected at the boundary (config validation, #402 class).
	rr := camDo(t, h, http.MethodPost, "/api/cameras",
		`{"name":"Bad","protocol":"rtsp","url":"rtsp://127.0.0.1:1/s","encoding":"h264","imaging_type":"infrared"}`)
	require.Equal(t, http.StatusBadRequest, rr.Code, "body: %s", rr.Body.String())

	// Thermal camera created; response echoes imaging_type (YAML overlay).
	rr = camDo(t, h, http.MethodPost, "/api/cameras",
		`{"name":"热成像通道","protocol":"srt","stream_key":"sk-thermal-1","encoding":"h264","imaging_type":"thermal"}`)
	require.Equal(t, http.StatusCreated, rr.Code, "body: %s", rr.Body.String())
	var created map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &created))
	require.Equal(t, "thermal", created["imaging_type"])

	// Default policy side effect: thermal lands in vision.skip_cameras.
	require.True(t, h.config.Vision.ShouldSkipCamera(created["id"].(string)))

	// Manager sees the persisted value.
	cfg := cm.GetCameraConfig(created["id"].(string))
	require.NotNil(t, cfg)
	require.Equal(t, "thermal", cfg.ImagingType)
}

func TestUpdateCameraImagingType(t *testing.T) {
	t.Helper()
	t.Parallel()
	h, cm, _ := newCameraAPIEnv(t)

	// Invalid value rejected.
	rr := camDo(t, h, http.MethodPut, "/api/cameras/rtsp-cam", `{"imaging_type":"ir"}`)
	require.Equal(t, http.StatusBadRequest, rr.Code, "body: %s", rr.Body.String())

	// Transition to thermal: response echoes; skip policy applied.
	rr = camDo(t, h, http.MethodPut, "/api/cameras/rtsp-cam", `{"imaging_type":"thermal"}`)
	require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())
	var updated map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &updated))
	require.Equal(t, "thermal", updated["imaging_type"])
	require.True(t, h.config.Vision.ShouldSkipCamera("rtsp-cam"))
	require.Equal(t, "thermal", cm.GetCameraConfig("rtsp-cam").ImagingType)

	// Back to visible: echoed empty (omitempty drops the key); skip removal
	// stays manual.
	rr = camDo(t, h, http.MethodPut, "/api/cameras/rtsp-cam", `{"imaging_type":"visible"}`)
	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, "visible", cm.GetCameraConfig("rtsp-cam").ImagingType)
	require.True(t, h.config.Vision.ShouldSkipCamera("rtsp-cam"),
		"skip removal is manual — reverting imaging_type must not un-skip")

	// List view carries the overlay too (badge data source).
	rr = camDo(t, h, http.MethodGet, "/api/cameras", "")
	require.Equal(t, http.StatusOK, rr.Code)
	var list []map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &list))
	found := false
	for _, c := range list {
		if c["id"] == "rtsp-cam" {
			found = true
			require.Equal(t, "visible", c["imaging_type"])
		}
	}
	require.True(t, found)
}
