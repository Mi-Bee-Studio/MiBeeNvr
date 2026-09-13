package camera

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/recorder"
)

// TestBuildersMJPEGFormDefaultAVI (#761): the builders must wire the
// single-file AVI container as the DEFAULT MJPEG/JPEG segment shape, with
// mjpeg_form: dir as the only path back to the legacy per-frame directory.
// Guards the builder→config wiring (lesson #653: wiring is behavior — an
// unwired flag passes every unit test and still ships dead).
func TestBuildersMJPEGFormDefaultAVI(t *testing.T) {
	t.Helper()
	cm := &CameraManager{}

	// HTTP-JPEG builder (ESP32 MiBeeCam path): default → AVI on.
	hj, ok := cm.buildHTTPJPEGRecorder(config.CameraConfig{
		ID: "cam-hj-default", Protocol: "http", Encoding: "jpeg", URL: "http://127.0.0.1:81/stream",
	}, 30*time.Second).(*recorder.HTTPJPEGRecorder)
	require.True(t, ok, "expected *HTTPJPEGRecorder")
	require.True(t, hj.AVIForm(), "default form must be AVI single-file (#761)")

	// HTTP-JPEG builder: explicit dir opt-out → AVI off.
	hjDir, ok := cm.buildHTTPJPEGRecorder(config.CameraConfig{
		ID: "cam-hj-dir", Protocol: "http", Encoding: "jpeg", URL: "http://127.0.0.1:81/stream",
		MJPEGForm: config.MJPEGFormDir,
	}, 30*time.Second).(*recorder.HTTPJPEGRecorder)
	require.True(t, ok)
	require.False(t, hjDir.AVIForm(), "mjpeg_form: dir must keep the legacy directory form")

	// RTSP MJPEG builder: Form passes through and "" resolves to AVI.
	mj, ok := cm.buildRTSPRecorder(config.CameraConfig{
		ID: "cam-mj-default", Protocol: "rtsp", Encoding: "mjpeg", URL: "rtsp://127.0.0.1:8554/x",
	}, 30*time.Second).(*recorder.MJPEGRecorder)
	require.True(t, ok, "expected *MJPEGRecorder")
	require.True(t, mj.AVIContainer(), "default form must be AVI single-file (#761)")

	mjDir, ok := cm.buildRTSPRecorder(config.CameraConfig{
		ID: "cam-mj-dir", Protocol: "rtsp", Encoding: "mjpeg", URL: "rtsp://127.0.0.1:8554/x",
		MJPEGForm: config.MJPEGFormDir,
	}, 30*time.Second).(*recorder.MJPEGRecorder)
	require.True(t, ok)
	require.False(t, mjDir.AVIContainer(), "mjpeg_form: dir must keep the legacy directory form")
}
