package api

// Thermal imaging channel (dual-spectrum): imaging_type create/update at the
// API boundary — validation, response echo (YAML overlay), and the vision
// skip default policy side effect.

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"testing"
	"time"

	onvifserver "github.com/mickeyzzc/onvif-go/v2/server"
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

// TestProbeONVIFStreamDualSpectrumProfiles pins the create-form data source
// against the embedded onvif-go virtual camera configured as a dual-spectrum
// device: even when the RTSP leg cannot be verified (no real streamer in
// the simulator), the probe must carry BOTH profiles plus the automatic
// main-selection token so the manual picker can offer the thermal channel.
func TestProbeONVIFStreamDualSpectrumProfiles(t *testing.T) {
	t.Parallel()

	cfg := onvifserver.DefaultConfig()
	cfg.Host = "127.0.0.1"
	cfg.Port = 0
	cfg.Username = "admin"
	cfg.Password = "sim-pass"
	cfg.Profiles = []onvifserver.ProfileConfig{
		{
			Token: "Profile_visual", Name: "Visible Main",
			VideoSource: onvifserver.VideoSourceConfig{
				Token: "vs-0", Name: "Visible",
				Resolution: onvifserver.Resolution{Width: 2560, Height: 1440},
				Framerate:  25,
				Bounds:     onvifserver.Bounds{Width: 2560, Height: 1440},
			},
			VideoEncoder: onvifserver.VideoEncoderConfig{
				Encoding:   "H264",
				Resolution: onvifserver.Resolution{Width: 2560, Height: 1440},
				Framerate:  25, GovLength: 25, Bitrate: 4096,
			},
		},
		{
			Token: "Profile_thermal", Name: "Thermal",
			VideoSource: onvifserver.VideoSourceConfig{
				Token: "vs-1", Name: "Thermal",
				Resolution: onvifserver.Resolution{Width: 384, Height: 288},
				Framerate:  9,
				Bounds:     onvifserver.Bounds{Width: 384, Height: 288},
			},
			VideoEncoder: onvifserver.VideoEncoderConfig{
				Encoding:   "H264",
				Resolution: onvifserver.Resolution{Width: 384, Height: 288},
				Framerate:  9, GovLength: 9, Bitrate: 512,
			},
		},
	}

	// Port 0 would leak into the advertised service XAddrs (the simulator
	// publishes config.Port, not the ephemeral port the listener got) —
	// reserve a concrete free port instead.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())
	cfg.Port = port

	sim, err := onvifserver.New(cfg)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = sim.Start(ctx) }()
	require.Eventually(t, func() bool { return sim.ListenAddr() != "" },
		5*time.Second, 50*time.Millisecond)

	endpoint := "http://" + sim.ListenAddr() + "/onvif/device_service"
	probe := probeONVIFStream(context.Background(), endpoint, "admin", "sim-pass")

	require.True(t, probe.Reachable, "device_service must answer: %s", probe.Reason)
	require.NotEmpty(t, probe.Profiles, "the profile list must ride the probe even without a stream leg")
	require.Len(t, probe.Profiles, 2)
	require.Equal(t, "Profile_visual", probe.AutoMainToken,
		"automatic selection picks the highest-resolution visible-light profile")
	tokens := []string{probe.Profiles[0].Token, probe.Profiles[1].Token}
	require.Contains(t, tokens, "Profile_thermal")
}
