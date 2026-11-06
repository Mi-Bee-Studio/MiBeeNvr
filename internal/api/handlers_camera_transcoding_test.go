package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/stretchr/testify/require"
)

// POST /api/cameras persists transcoding and dark-frame fields exactly like
// PUT does; the edit form sends them on create.
func TestHandleCreateCamera_PersistsTranscodingAndDarkFrame(t *testing.T) {
	h := setupDedupHandler(t)
	body := `{"name":"Cam","protocol":"rtsp","encoding":"h264","url":"rtsp://192.0.2.9:554/s",
		"transcoding":{"enabled":true,"target_codec":"h264","preset":"faster","bitrate":"4M","crf":20},
		"dark_frame_filter_enabled":true,"dark_frame_threshold":18}`
	rr := doRequest(t, h.Routes(), "POST", "/api/cameras/", bytes.NewReader([]byte(body)), "", "")
	require.Equal(t, http.StatusCreated, rr.Code, "body: %s", rr.Body.String())

	var created struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &created))
	cam := h.camMgr.GetCameraConfig(created.ID)
	require.NotNil(t, cam)
	require.Equal(t, &config.CameraTranscodingConfig{
		Enabled: true, TargetCodec: "h264", Preset: "faster", Bitrate: "4M", CRF: 20,
	}, cam.Transcoding)
	require.True(t, cam.DarkFrameFilterEnabled)
	require.Equal(t, 18, cam.DarkFrameThreshold)

	// The 201 body carries the same fields back (injectYAMLConfigFields).
	var resp struct {
		Transcoding    *config.CameraTranscodingConfig `json:"transcoding"`
		DarkFrameOn    bool                            `json:"dark_frame_filter_enabled"`
		DarkFrameThold int                             `json:"dark_frame_threshold"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.Equal(t, true, resp.Transcoding != nil && resp.Transcoding.Enabled)
	require.True(t, resp.DarkFrameOn)
	require.Equal(t, 18, resp.DarkFrameThold)
}

// The transcode-task gate's 400 tells the caller WHERE to turn transcoding
// on — a playback fallback hitting it needs a configuration pointer, not a
// bare refusal.
func TestTranscodeGateErrorNamesTheSetting(t *testing.T) {
	h := setupDedupHandler(t)

	// Camera exists, transcoding disabled → gate fires on task creation.
	rr := doRequest(t, h.Routes(), "POST", "/api/cameras/",
		bytes.NewReader([]byte(`{"name":"G","protocol":"rtsp","url":"rtsp://192.0.2.9:554/s","encoding":"h264"}`)), "", "")
	require.Equal(t, http.StatusCreated, rr.Code, "body: %s", rr.Body.String())
	var created struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &created))

	rr = doRequest(t, h.Routes(), "POST", "/api/transcoding/tasks",
		bytes.NewReader([]byte(`{"camera_id":"`+created.ID+`","recording_id":"r1","target_codec":"h264"}`)), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code, "body: %s", rr.Body.String())
	require.Contains(t, rr.Body.String(), "is not enabled for camera")
	require.Contains(t, rr.Body.String(), "Settings")
}
