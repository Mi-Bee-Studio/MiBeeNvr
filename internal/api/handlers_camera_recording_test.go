package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/stretchr/testify/require"
)

// adaptiveCreateBody is an rtsp/h265 adaptive camera carrying every
// recording-strategy field the edit form sends on create.
const adaptiveCreateBody = `{
  "name":"Yard","protocol":"rtsp","encoding":"h265","url":"rtsp://192.0.2.9:554/main",
  "sub_stream_url":"rtsp://192.0.2.9:554/sub",
  "recording_mode":"adaptive",
  "recording_tier":"tiered",
  "pixgate":{"enabled":true,"sample_fps":1,"min_area_pct":1.5,"hold":"30s"},
  "audio_trigger":{"enabled":true,"min_dbfs":-45,"pre_capture_s":3}
}`

var (
	wantPixgate      = &config.CameraPixgateConfig{Enabled: true, SampleFPS: 1, MinAreaPct: 1.5, Hold: "30s"}
	wantAudioTrigger = &config.CameraAudioTriggerConfig{Enabled: true, MinDBFS: -45, PreCaptureS: 3}
)

// cameraRecordingFields is the wire shape the edit form reads back. Declared
// locally so the assertions pin the API contract, not the CameraRow layout.
type cameraRecordingFields struct {
	ID            string                           `json:"id"`
	RecordingMode string                           `json:"recording_mode"`
	RecordingTier string                           `json:"recording_tier"`
	Pixgate       *config.CameraPixgateConfig      `json:"pixgate"`
	AudioTrigger  *config.CameraAudioTriggerConfig `json:"audio_trigger"`
}

func createAdaptiveCamera(t *testing.T, h *Handler) (string, []byte) {
	t.Helper()
	rr := doRequest(t, h.Routes(), "POST", "/api/cameras/", bytes.NewReader([]byte(adaptiveCreateBody)), "", "")
	require.Equal(t, http.StatusCreated, rr.Code, "body: %s", rr.Body.String())
	var created struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &created))
	require.NotEmpty(t, created.ID)
	return created.ID, rr.Body.Bytes()
}

func requireAdaptiveFields(t *testing.T, raw []byte) {
	t.Helper()
	var got cameraRecordingFields
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Equal(t, "adaptive", got.RecordingMode)
	require.Equal(t, "tiered", got.RecordingTier)
	require.Equal(t, wantPixgate, got.Pixgate)
	require.Equal(t, wantAudioTrigger, got.AudioTrigger)
}

// POST /api/cameras persists recording_tier and pixgate exactly like PUT does;
// the edit form sends them on create.
func TestHandleCreateCamera_PersistsRecordingTierAndPixgate(t *testing.T) {
	h := setupDedupHandler(t)
	id, _ := createAdaptiveCamera(t, h)

	cam := h.camMgr.GetCameraConfig(id)
	require.NotNil(t, cam)
	require.Equal(t, "adaptive", cam.RecordingMode)
	require.Equal(t, "tiered", cam.RecordingTier)
	require.Equal(t, wantPixgate, cam.Pixgate)
	require.Equal(t, wantAudioTrigger, cam.AudioTrigger)
}

// recording_tier and pixgate go through the same boundary validation on create
// as on update: an invalid value is a 400 at the wire, not a fatal error at the
// next restart (#402 class).
func TestHandleCreateCamera_ValidatesRecordingTierAndPixgate(t *testing.T) {
	cases := []struct {
		name, body, want string
	}{
		{
			name: "unknown tier",
			body: `{"name":"T","protocol":"rtsp","url":"rtsp://192.0.2.9:554/s","recording_tier":"bogus"}`,
			want: "recording_tier must be",
		},
		{
			name: "tiered on push camera",
			body: `{"name":"T","protocol":"srt","recording_tier":"tiered"}`,
			want: "recording_tier: tiered requires",
		},
		{
			name: "pixgate sample_fps out of range",
			body: `{"name":"T","protocol":"rtsp","url":"rtsp://192.0.2.9:554/s","pixgate":{"enabled":true,"sample_fps":99}}`,
			want: "pixgate.sample_fps",
		},
		{
			name: "pixgate on push camera",
			body: `{"name":"T","protocol":"srt","pixgate":{"enabled":true}}`,
			want: "pixgate requires an RTSP-reachable sub-stream",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := setupDedupHandler(t)
			rr := doRequest(t, h.Routes(), "POST", "/api/cameras/", bytes.NewReader([]byte(tc.body)), "", "")
			require.Equal(t, http.StatusBadRequest, rr.Code, "body: %s", rr.Body.String())
			require.Contains(t, rr.Body.String(), tc.want)
		})
	}
}

// The 201 body is the first thing an API client reads back; it carries the
// YAML-only recording fields the same way GET and PUT do.
func TestHandleCreateCamera_ResponseCarriesRecordingFields(t *testing.T) {
	h := setupDedupHandler(t)
	_, raw := createAdaptiveCamera(t, h)
	requireAdaptiveFields(t, raw)
}

// GET (single and list) returns audio_trigger alongside the other recording
// fields. Without it the edit form reads the trigger back as disabled and
// sends {enabled:false} on the next save, disarming a working trigger.
func TestHandleGetAndListCameras_ReturnRecordingFields(t *testing.T) {
	h := setupDedupHandler(t)
	id, _ := createAdaptiveCamera(t, h)

	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/"+id, nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())
	requireAdaptiveFields(t, rr.Body.Bytes())

	rr = doRequest(t, h.Routes(), "GET", "/api/cameras/", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())
	var list []json.RawMessage
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &list))
	require.Len(t, list, 1)
	requireAdaptiveFields(t, list[0])
}
