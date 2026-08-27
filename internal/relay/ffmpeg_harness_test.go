package relay

// FFmpeg-subprocess relay tests (#567): the ffmpeg/ffprobe coupling is
// exercised with hermetic stub scripts (echo fixtures — no real binaries),
// plus the remaining pure helpers (splitRTMPPath, avsync.round1) and
// CameraStatusJSON. See #571: fake scripts keep CI dependency-free.

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/stretchr/testify/require"
)

func TestProbeSourceVideoFPS_ParsesFrameRate(t *testing.T) {
	// ffprobe lives next to the "ffmpeg" binary — provide both.
	dir := t.TempDir()
	ffmpeg := filepath.Join(dir, "ffmpeg")
	ffprobe := filepath.Join(dir, "ffprobe")
	require.NoError(t, os.WriteFile(ffmpeg, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	require.NoError(t, os.WriteFile(ffprobe, []byte("#!/bin/sh\nprintf '15/1\\n'\n"), 0o755))

	require.Equal(t, 15, probeSourceVideoFPS(ffmpeg, "rtsp://127.0.0.1:1/x"))
}

func TestProbeSourceVideoFPS_GarbageYieldsZero(t *testing.T) {
	dir := t.TempDir()
	ffmpeg := filepath.Join(dir, "ffmpeg")
	require.NoError(t, os.WriteFile(ffmpeg, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	require.NoError(t, os.WriteFile(ffprobePath(t, dir), []byte("#!/bin/sh\nprintf 'not-a-rate\\n'\n"), 0o755))

	require.Equal(t, 0, probeSourceVideoFPS(ffmpeg, "rtsp://127.0.0.1:1/x"))
	require.Equal(t, 0, probeSourceVideoFPS("/nonexistent/ffmpeg", "rtsp://127.0.0.1:1/x"),
		"missing binary path must degrade to 0, never error")
}

func ffprobePath(t *testing.T, dir string) string {
	t.Helper()
	return filepath.Join(dir, "ffprobe")
}

// connectViaFFmpeg with stub ffmpeg binaries: a clean exit 0 ends the relay
// normally (nil error); a non-zero exit surfaces as an error.
func TestConnectViaFFmpeg_StubProcess(t *testing.T) {
	for _, tc := range []struct {
		name    string
		script  string
		wantErr bool
	}{
		{"clean exit", "#!/bin/sh\nexit 0\n", false},
		{"failed exit", "#!/bin/sh\nexit 1\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			ffmpeg := filepath.Join(dir, "ffmpeg")
			require.NoError(t, os.WriteFile(ffmpeg, []byte(tc.script), 0o755))

			target := NewPushTarget("cam1", PushTargetConfig{
				ID: "ff", Protocol: "rtmp", URL: "rtmp://127.0.0.1:1/live/key",
				SourceURL: "rtsp://127.0.0.1:1/src", UseFFmpeg: true,
			}, newHubForRelay(), nil)
			target.SetFFmpegPath(ffmpeg)

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			err := target.connectAndStream(ctx)
			if tc.wantErr {
				require.Error(t, err, "non-zero ffmpeg exit must surface as an error")
			} else {
				require.NoError(t, err, "clean exit ends the relay without error")
			}
		})
	}
}

func TestConnectViaFFmpeg_NoBinaryNoSource(t *testing.T) {
	target := NewPushTarget("cam1", PushTargetConfig{
		ID: "ff", Protocol: "rtmp", URL: "rtmp://127.0.0.1:1/live/key", UseFFmpeg: true,
	}, newHubForRelay(), nil)
	target.SetFFmpegPath("/nonexistent/ffmpeg")

	err := target.connectAndStream(context.Background())
	require.ErrorIs(t, err, errPermanent, "missing ffmpeg must fail permanently")
	require.Contains(t, target.Status().Error, "cannot resolve source URL",
		"source-URL resolution fails before the binary lookup")
}

func TestSplitRTMPPath(t *testing.T) {
	u := mustParseURL(t, "rtmp://host/live/key?wsSecret=x")
	app, key := splitRTMPPath(u)
	require.Equal(t, "live", app)
	require.Equal(t, "key?wsSecret=x", key, "query must stay attached to the stream key")

	u2 := mustParseURL(t, "rtmp://host/live")
	app2, key2 := splitRTMPPath(u2)
	require.Equal(t, "live", app2)
	require.Empty(t, key2)
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	require.NoError(t, err)
	return u
}

func TestRound1(t *testing.T) {
	// round1 truncates to one decimal (avsync drift bucketing).
	require.Equal(t, 1.4, round1(1.44))
	require.Equal(t, 1.9, round1(1.99))
	require.Equal(t, 0.0, round1(0))
}

func TestCameraStatusJSON(t *testing.T) {
	m := newTestManager()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)
	m.SetCameraTargets("cam1", []config.PushTargetConfig{
		{ID: "t1", Protocol: "bogus", URL: "x://nope", Enabled: true},
	})
	require.Eventually(t, func() bool { return len(m.CameraStatusJSON("cam1")) == 1 },
		5*time.Second, 50*time.Millisecond)
	m.Stop()
}
