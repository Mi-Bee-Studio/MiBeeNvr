package api

// Timelapse per-camera config GET/PUT, retry-merge lifecycle (#596), and the
// Handler setter/capabilities long tail. Uses the same RollingMergeManager
// pattern as handlers_timelapse_test.go but with a minimal fast merger.

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/timelapse"
	"github.com/stretchr/testify/require"
)

func TestCameraTimelapseGetPut(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	h := TestHandler(db, store)
	t.Cleanup(h.Close)

	h.config = &config.Config{Cameras: []config.CameraConfig{
		{ID: "cam-a", Name: "A", Protocol: "srt"},
	}}
	h.configPath = filepath.Join(t.TempDir(), "cfg.yaml")

	// No timelapse config → documented defaults.
	w := camDo(t, h, http.MethodGet, "/api/cameras/cam-a/timelapse", "")
	require.Equal(t, http.StatusOK, w.Code)
	var def struct {
		Enabled  bool   `json:"enabled"`
		Interval string `json:"interval"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &def))
	require.False(t, def.Enabled)
	require.Equal(t, "30s", def.Interval)

	// PUT: happy path applies defaults to omitted fields and persists.
	w = camDo(t, h, http.MethodPut, "/api/cameras/cam-a/timelapse", `{"enabled":true,"merge_duration":"8h"}`)
	require.Equal(t, http.StatusOK, w.Code)
	var stored *config.CameraTimelapseConfig
	for i := range h.config.Cameras {
		if h.config.Cameras[i].ID == "cam-a" {
			stored = h.config.Cameras[i].Timelapse
		}
	}
	require.NotNil(t, stored)
	require.True(t, stored.Enabled)
	require.Equal(t, "30s", stored.Interval, "default interval applied")
	require.Equal(t, "auto", stored.FrameSource, "default frame_source applied")
	require.Equal(t, "8h", stored.MergeDuration)
	// Round-tripped through disk.
	disk, err := config.Load(h.configPath)
	require.NoError(t, err)
	require.Len(t, disk.Cameras, 1)
	require.NotNil(t, disk.Cameras[0].Timelapse)
	require.Equal(t, "8h", disk.Cameras[0].Timelapse.MergeDuration)

	// GET now returns the stored config.
	w = camDo(t, h, http.MethodGet, "/api/cameras/cam-a/timelapse", "")
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &def))
	require.True(t, def.Enabled)

	// Validation matrix.
	require.Equal(t, http.StatusBadRequest, camDo(t, h, http.MethodPut, "/api/cameras/cam-a/timelapse", "not json").Code)
	require.Equal(t, http.StatusBadRequest, camDo(t, h, http.MethodPut, "/api/cameras/cam-a/timelapse", `{"interval":"xx"}`).Code)
	require.Equal(t, http.StatusBadRequest, camDo(t, h, http.MethodPut, "/api/cameras/cam-a/timelapse", `{"interval":"100ms"}`).Code)
	require.Equal(t, http.StatusBadRequest, camDo(t, h, http.MethodPut, "/api/cameras/cam-a/timelapse", `{"frame_source":"telepathy"}`).Code)
	require.Equal(t, http.StatusBadRequest, camDo(t, h, http.MethodPut, "/api/cameras/cam-a/timelapse", `{"merge_mode":"magic"}`).Code)
	require.Equal(t, http.StatusBadRequest, camDo(t, h, http.MethodPut, "/api/cameras/cam-a/timelapse", `{"merge_output_fps":99}`).Code)
	require.Equal(t, http.StatusNotFound, camDo(t, h, http.MethodPut, "/api/cameras/nope/timelapse", `{"enabled":true}`).Code)

	// Legacy camelCase alias mergeDuration still accepted. PUT replaces the
	// Timelapse pointer, so re-read the current one instead of `stored`.
	require.Equal(t, http.StatusOK, camDo(t, h, http.MethodPut, "/api/cameras/cam-a/timelapse", `{"enabled":true,"mergeDuration":"24h"}`).Code)
	fresh := h.config.Cameras[0].Timelapse
	require.NotNil(t, fresh)
	require.Equal(t, "24h", fresh.MergeDuration)

	// Guard branches: no config / no config path.
	h2 := TestHandler(db, store)
	require.Equal(t, http.StatusInternalServerError, camDo(t, h2, http.MethodGet, "/api/cameras/cam-a/timelapse", "").Code)
	require.Equal(t, http.StatusInternalServerError, camDo(t, h2, http.MethodPut, "/api/cameras/cam-a/timelapse", `{"enabled":true}`).Code)
	h2.config = &config.Config{Cameras: []config.CameraConfig{{ID: "cam-a", Protocol: "srt"}}}
	require.Equal(t, http.StatusInternalServerError, camDo(t, h2, http.MethodPut, "/api/cameras/cam-a/timelapse", `{"enabled":true}`).Code)
}

// fastFakeMerger satisfies timelapse.TimelapseMerger, writing a tiny output
// file so RollingMergeManager's post-merge verification passes.
type fastFakeMerger struct {
	merged atomic.Int32
}

func (m *fastFakeMerger) CanMerge() bool            { return true }
func (m *fastFakeMerger) Tier() timelapse.MergeTier { return timelapse.TierGo }
func (m *fastFakeMerger) Merge(_ context.Context, _, outputPath string, _ int) (*timelapse.MergeResult, error) {
	m.merged.Add(1)
	if err := os.WriteFile(outputPath, []byte("fake"), 0o644); err != nil {
		return nil, err
	}
	return &timelapse.MergeResult{}, nil
}

func TestRetryTimelapseMerge(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	h := TestHandler(db, store)
	t.Cleanup(h.Close)

	// No manager → 503.
	require.Equal(t, http.StatusServiceUnavailable, camDo(t, h, http.MethodPost, "/api/recordings/r1/retry-merge", "").Code)

	merger := &fastFakeMerger{}
	h.SetTimelapseMergeMgr(timelapse.NewRollingMergeManager(merger, db, 10, false))

	ctx := context.Background()
	now := time.Now().UTC()
	seed := func(id, format, mergeStatus, filePath string) {
		require.NoError(t, db.InsertRecording(ctx, &model.Recording{
			ID: id, CameraID: "cam-a", Format: model.Format(format), FilePath: filePath,
			StartedAt: now.Add(-time.Hour), EndedAt: now, MergeStatus: mergeStatus,
		}))
	}

	// Guard branches.
	seed("r404", "h264", "completed", "/tmp/x")
	require.Equal(t, http.StatusNotFound, camDo(t, h, http.MethodPost, "/api/recordings/missing/retry-merge", "").Code)
	require.Equal(t, http.StatusBadRequest, camDo(t, h, http.MethodPost, "/api/recordings/r404/retry-merge", "").Code, "non-timelapse")
	seed("r-state", "timelapse", "completed", "/tmp/y")
	require.Equal(t, http.StatusBadRequest, camDo(t, h, http.MethodPost, "/api/recordings/r-state/retry-merge", "").Code, "not retryable state")
	seed("r-nodir", "timelapse", "failed", "")
	require.Equal(t, http.StatusBadRequest, camDo(t, h, http.MethodPost, "/api/recordings/r-nodir/retry-merge", "").Code, "no frame dir")

	// Happy: 202 + merge actually starts (observable via the fake merger).
	frameDir := filepath.Join(t.TempDir(), "frames")
	require.NoError(t, os.MkdirAll(frameDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(frameDir, "frame_001.jpg"), []byte("x"), 0o644))
	seed("r-ok", "timelapse", "failed", frameDir)

	w := camDo(t, h, http.MethodPost, "/api/recordings/r-ok/retry-merge", "")
	require.Equal(t, http.StatusAccepted, w.Code)
	var resp struct {
		Status   string `json:"status"`
		FrameDir string `json:"frame_dir"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, "merge_initiated", resp.Status)
	require.Equal(t, frameDir, resp.FrameDir)

	require.Eventually(t, func() bool { return merger.merged.Load() == 1 },
		10*time.Second, 50*time.Millisecond, "rolling merge must run for the retried recording")
}

func TestFormatOffset(t *testing.T) {
	t.Parallel()
	cases := map[int]string{
		0:          "+0",
		3600:       "+1",
		-3600:      "-1",
		19800:      "+5:30",
		-12600:     "-3:30",
		-3600 * 10: "-10",
	}
	for in, want := range cases {
		require.Equal(t, want, formatOffset(in), "input %d", in)
	}
}

func TestHandlerSettersNoPanic(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	h := TestHandler(db, store)
	t.Cleanup(h.Close)

	// Every dependency setter accepts its nil zero value without panicking —
	// guards the "optional manager" branches across the handler surface.
	h.SetHealthManager(nil)
	h.SetStabilityProvider(nil)
	h.SetDownloader(nil)
	h.SetRollingMergeMgr(nil)
	h.SetWHIPServer(nil)
	h.SetGB28181Cascade(nil)
	h.SetGB28181Timezone(nil)
	h.SetGB28181Inviter(nil)
	h.SetGB28181DeviceMedia(nil)
	h.SetTranscodeManager(nil)
	SetUpdateChecker(nil)
}

func TestCapabilitiesEndpoint(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	h := TestHandler(db, store)
	t.Cleanup(h.Close)

	rtmpOn := true
	h.config = &config.Config{
		Server: config.ServerConfig{Listen: ":9090"},
		RTMP:   config.RTMPConfig{Enabled: &rtmpOn, Port: 1935},
		SRT:    config.SRTConfig{Port: 9000},
	}

	w := camDo(t, h, http.MethodGet, "/api/capabilities", "")
	require.Equal(t, http.StatusOK, w.Code)
	var caps struct {
		Ingest struct {
			RTMP struct {
				Enabled bool `json:"enabled"`
				Port    int  `json:"port"`
			} `json:"rtmp"`
			SRT struct {
				Enabled bool `json:"enabled"`
				Port    int  `json:"port"`
			} `json:"srt"`
			WHIP struct {
				Enabled bool `json:"enabled"`
				Port    int  `json:"port"`
			} `json:"whip"`
		} `json:"ingest"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &caps))
	require.True(t, caps.Ingest.RTMP.Enabled)
	require.Equal(t, 1935, caps.Ingest.RTMP.Port)
	require.False(t, caps.Ingest.SRT.Enabled, "nil SRT enabled flag ⇒ disabled")
	require.Equal(t, 9000, caps.Ingest.SRT.Port)
	require.False(t, caps.Ingest.WHIP.Enabled, "no WHIP server wired ⇒ disabled")
	require.Equal(t, 9090, caps.Ingest.WHIP.Port, "WHIP port mirrors the HTTP listener")
}

func TestRelayCapabilities(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	h := TestHandler(db, store)
	t.Cleanup(h.Close)

	w := camDo(t, h, http.MethodGet, "/api/relay/capabilities", "")
	require.Equal(t, http.StatusOK, w.Code)
	var caps struct {
		FFmpegRelaySupported bool `json:"ffmpeg_relay_supported"`
		FFmpegAvailable      bool `json:"ffmpeg_available"`
		MaxTargetsPerCamera  int  `json:"max_targets_per_camera"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &caps))
	require.True(t, caps.FFmpegRelaySupported)
	require.False(t, caps.FFmpegAvailable, "no relay manager wired ⇒ ffmpeg unavailable")
	require.Equal(t, 10, caps.MaxTargetsPerCamera)
}
