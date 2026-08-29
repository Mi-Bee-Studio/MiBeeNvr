package relay

// Manager lifecycle tests (#567): setter wiring, FFmpegAvailable, the
// SetCameraTargets reconcile loop (start / restart-on-change / stop-on-remove
// / disabled-skipped), and Stop joining target goroutines. Deterministic by
// construction: an unsupported protocol fails permanently (no live network),
// and states are observed via CameraStatus polling — never sleeps.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/streamhub"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/transcoding"
	"github.com/stretchr/testify/require"
)

func newTestManager() *Manager {
	return NewManager(
		func(string) *streamhub.StreamHub { return streamhub.New() },
		func(string) ([]byte, []byte, bool) { return []byte{0x67}, []byte{0x68}, true },
	)
}

func TestManager_FFmpegAvailable(t *testing.T) {
	m := newTestManager()
	require.False(t, m.FFmpegAvailable(), "nothing wired → unavailable")

	m.SetFFmpegPath("/usr/local/bin/ffmpeg")
	require.True(t, m.FFmpegAvailable(), "explicit path → available")

	m.SetHardwareCap(&transcoding.HardwareCapabilities{FFmpegAvailable: false})
	require.False(t, m.FFmpegAvailable(), "hardware capability report wins over raw path")
}

func TestManager_SettersAndStart(t *testing.T) {
	m := newTestManager()
	m.SetCodecInfoProvider(func(string) model.CodecInfo { return model.CodecInfo{} })
	m.SetSourceCodecProvider(func(string) string { return "h264" })
	m.SetPresetRegistry(NewPresetRegistry())
	m.SetHardwareCap(&transcoding.HardwareCapabilities{})
	m.SetFFmpegPath("")
	m.SetStreamURLProvider(func(string) string { return "" })
	m.Start(context.Background())
	m.Stop() // no targets — returns immediately
}

func TestManager_ReconcileLifecycle(t *testing.T) {
	m := newTestManager()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)

	target := config.PushTargetConfig{
		ID: "t1", Name: "Test", Protocol: "bogus", URL: "x://nope", Enabled: true,
	}

	// Start: the target runs, hits the permanent protocol error, and lands in
	// reconnecting with the cause attached (the watched wrapper reports the
	// permanent error; Run retries with backoff).
	m.SetCameraTargets("cam1", []config.PushTargetConfig{target})
	require.Eventually(t, func() bool {
		for _, st := range m.CameraStatus("cam1") {
			if st.ID == "t1" && st.Status == StatusReconnecting &&
				strings.Contains(st.Error, "permanent relay error") {
				return true
			}
		}
		return false
	}, 5*time.Second, 50*time.Millisecond, "permanent protocol error must surface in status")

	// Idempotent re-apply: unchanged config does not churn the target.
	m.SetCameraTargets("cam1", []config.PushTargetConfig{target})
	require.Len(t, m.CameraStatus("cam1"), 1)

	// Change the URL → restart (new target instance, same ID).
	changed := target
	changed.URL = "x://other"
	m.SetCameraTargets("cam1", []config.PushTargetConfig{changed})
	require.Len(t, m.CameraStatus("cam1"), 1)

	// Empty list → target removed.
	m.SetCameraTargets("cam1", nil)
	require.Empty(t, m.CameraStatus("cam1"), "removal must stop the target")

	m.Stop()
}

func TestManager_DisabledTargetNotStarted(t *testing.T) {
	m := newTestManager()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)

	m.SetCameraTargets("cam1", []config.PushTargetConfig{
		{ID: "off", Protocol: "rtsp", URL: "rtsp://127.0.0.1:1/x", Enabled: false},
	})
	require.Empty(t, m.CameraStatus("cam1"), "disabled targets must not start")
	m.Stop()
}

func TestPushTarget_RunNilHub(t *testing.T) {
	target := NewPushTarget("cam1", PushTargetConfig{ID: "t1", Protocol: "rtsp"}, nil, nil)
	target.Run(context.Background()) // returns without connecting
	st := target.Status()
	require.Equal(t, StatusError, st.Status)
	require.Contains(t, st.Error, "no stream hub")
}

func TestConnectAndStream_UnsupportedProtocol(t *testing.T) {
	target := NewPushTarget("cam1", PushTargetConfig{ID: "t1", Protocol: "carrier-pigeon"},
		streamhub.New(), func() ([]byte, []byte, bool) { return nil, nil, true })
	err := target.connectAndStream(context.Background())
	require.ErrorIs(t, err, errPermanent)
	st := target.Status()
	require.Equal(t, StatusError, st.Status)
	require.Contains(t, st.Error, "unsupported protocol")
}

// RTSP dial to a refused port fails fast — the error path of connectRTSP
// without any live server.
func TestConnectRTSP_ConnectionRefused(t *testing.T) {
	target := NewPushTarget("cam1", PushTargetConfig{ID: "t1", Protocol: "rtsp", URL: "rtsp://127.0.0.1:1/x"},
		streamhub.New(), func() ([]byte, []byte, bool) { return []byte{0x67}, []byte{0x68}, true })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := target.connectAndStream(ctx)
	require.Error(t, err, "dial to a closed port must fail")
	st := target.Status()
	require.NotEqual(t, StatusStreaming, st.Status)
}
