package api

import (
	"context"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/camera"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/recorder"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/streamhub"
)

func audioLiveEnv(t *testing.T, rec model.Recorder, cameraID string) http.Handler {
	t.Helper()
	db, store := setupTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.UpsertCamera(t.Context(), cameraID, "Mic", "rtsp", "audio", "rtsp://127.0.0.1:1/mic", "", "", "", "", "", ""))
	cfg := &config.Config{
		Storage: config.StorageConfig{RootDir: store.RootDir(), SegmentDuration: "30s"},
		Cameras: []config.CameraConfig{},
	}
	camMgr := camera.NewCameraManager(cfg, store, db, "")
	if rec != nil {
		camMgr.SetTestRecorder(cameraID, rec)
	}
	h := NewHandler(db, store, noopAuthMW(), cfg, camMgr, nil, "", nil, nil, nil, nil, nil, nil)
	return h.Routes()
}

// TestAudioLiveWav_StreamsPCM drives the live endpoint against a real
// AudioRecorder + StreamHub: G.711 frames broadcast to the hub must arrive as
// a streaming WAV (44-byte header with 0xFFFFFFFF sizes + decoded PCM).
func TestAudioLiveWav_StreamsPCM(t *testing.T) {
	ar := recorder.NewAudioRecorder(recorder.AudioConfig{CameraID: "cam-mic"}, nil, nil)
	hub := streamhub.New()
	ar.SetHub(hub)
	ar.ArmPushFormat("g711", 8000, 1) // μ-law 8k mono

	srv := httptest.NewServer(audioLiveEnv(t, ar, "cam-mic"))
	defer srv.Close()

	// Feed the hub while a client is attached. The test body closes `stop`
	// (single closer — no defer here).
	stop := make(chan struct{})
	go func() {
		g711 := make([]byte, 160)
		for i := range g711 {
			g711[i] = 0xFF
		}
		deadline := time.After(3 * time.Second)
		for {
			select {
			case <-deadline:
				return
			case <-stop:
				return
			default:
			}
			hub.BroadcastAudio(time.Now().UnixNano(), model.AudioG711, g711)
			time.Sleep(20 * time.Millisecond)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/cameras/cam-mic/audio/live.wav", nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "audio/wav", resp.Header.Get("Content-Type"))

	head := make([]byte, 44)
	_, err = io.ReadFull(resp.Body, head)
	require.NoError(t, err)
	require.Equal(t, "RIFF", string(head[0:4]))
	require.Equal(t, uint32(0xFFFFFFFF), binary.LittleEndian.Uint32(head[4:8]), "streaming RIFF size")
	require.Equal(t, uint32(8000), binary.LittleEndian.Uint32(head[24:28]))

	body := make([]byte, 4096)
	n, _ := io.ReadFull(resp.Body, body)
	require.Greater(t, n, 320, "expected streaming PCM beyond one frame (got %d bytes)", n)
	// μ-law 0xFF decodes to 0 linear.
	require.EqualValues(t, 0, binary.LittleEndian.Uint16(body[0:2]))
	close(stop)

	// Session teardown must land in the audit ring.
	require.Eventually(t, func() bool {
		audioAudit.mu.Lock()
		defer audioAudit.mu.Unlock()
		for _, e := range audioAudit.entries {
			if e.Kind == "live" && e.CameraID == "cam-mic" && e.DurationS > 0 {
				return true
			}
		}
		return false
	}, 3*time.Second, 100*time.Millisecond, "live session should be audited")
}

func TestAudioLiveWav_NotAudioDevice(t *testing.T) {
	h := audioLiveEnv(t, nil, "cam-none") // no recorder registered
	srv := httptest.NewServer(h)
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/cameras/cam-none/audio/live.wav")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestAudioAuditEndpoint(t *testing.T) {
	h := audioLiveEnv(t, nil, "cam-x")
	recordAudioAudit(audioAuditEntry{Kind: "wav", CameraID: "cam-x", Recording: "r1"})
	srv := httptest.NewServer(h)
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/recordings/audio-audit")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	require.Contains(t, string(body), `"recording":"r1"`)
}
