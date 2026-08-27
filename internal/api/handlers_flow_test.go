package api

// Tests for the flow-path observability endpoints (#469 Phase 2):
// GET /api/streams and GET /api/cameras/{id}/flow.

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/camera"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/stretchr/testify/require"
)

func newFlowTestHandler(t *testing.T) (*Handler, *camera.CameraManager) {
	t.Helper()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	cfg := &config.Config{}
	camMgr := camera.NewCameraManager(cfg, nil, nil, "", nil)
	h := NewHandler(db, store, noopAuthMW(), cfg, camMgr, nil, "", nil, nil, nil, nil, nil)
	return h, camMgr
}

func TestHandleListStreams_Empty(t *testing.T) {
	h, _ := newFlowTestHandler(t)

	rr := doRequest(t, h.Routes(), http.MethodGet, "/api/streams", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp struct {
		Streams []json.RawMessage `json:"streams"`
	}
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	require.Empty(t, resp.Streams)
}

func TestHandleListStreams_WithHub(t *testing.T) {
	h, camMgr := newFlowTestHandler(t)

	hub := camMgr.GetOrCreateHub("flow-cam")
	require.NotNil(t, hub)
	hub.Broadcast(1, [][]byte{{0x65}}, true)
	require.NoError(t, hub.Subscribe("hls", func(int64, [][]byte) {}))

	rr := doRequest(t, h.Routes(), http.MethodGet, "/api/streams", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp struct {
		Streams []struct {
			CameraID  string `json:"camera_id"`
			Source    string `json:"source"`
			FramesIn  int64  `json:"frames_in"`
			Consumers []struct {
				ID    string `json:"id"`
				Sends int64  `json:"sends"`
			} `json:"consumers"`
			Viewers map[string]int `json:"viewers"`
		} `json:"streams"`
	}
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	require.Len(t, resp.Streams, 1)
	require.Equal(t, "flow-cam", resp.Streams[0].CameraID)
	require.Equal(t, int64(1), resp.Streams[0].FramesIn)
	require.Len(t, resp.Streams[0].Consumers, 1)
	require.Equal(t, "hls", resp.Streams[0].Consumers[0].ID)
	require.NotNil(t, resp.Streams[0].Viewers, "viewers map must serialize")
}

// The sub-stream branch (#513 observability) is omitted when the camera has
// no live sub entry — never a zombie object — and appears with state/refs/hub
// fan-out once the pull is active (covered end-to-end on-device; here only
// the absence contract is cheap to prove).
func TestHandleListStreams_SubBranchOmittedWithoutEntry(t *testing.T) {
	h, camMgr := newFlowTestHandler(t)

	hub := camMgr.GetOrCreateHub("flow-cam")
	require.NotNil(t, hub)

	rr := doRequest(t, h.Routes(), http.MethodGet, "/api/streams", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp struct {
		Streams []map[string]any `json:"streams"`
	}
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	require.Len(t, resp.Streams, 1)
	_, hasSub := resp.Streams[0]["sub"]
	require.False(t, hasSub, "sub must be omitted when no sub-stream entry exists")
}

func TestHandleCameraFlow(t *testing.T) {
	h, camMgr := newFlowTestHandler(t)

	hub := camMgr.GetOrCreateHub("flow-cam")
	require.NotNil(t, hub)

	rr := doRequest(t, h.Routes(), http.MethodGet, "/api/cameras/flow-cam/flow", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	require.Equal(t, "flow-cam", resp["camera_id"])
}

func TestHandleCameraFlow_NotFound(t *testing.T) {
	h, _ := newFlowTestHandler(t)

	rr := doRequest(t, h.Routes(), http.MethodGet, "/api/cameras/nope/flow", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestHandleCameraFlow_LastFrameAgeS(t *testing.T) {
	h, camMgr := newFlowTestHandler(t)

	// Camera with frames flowing: age must be a small positive number.
	hub := camMgr.GetOrCreateHub("flow-live")
	require.NotNil(t, hub)
	hub.Broadcast(1, [][]byte{{0x65}}, true)

	rr := doRequest(t, h.Routes(), http.MethodGet, "/api/cameras/flow-live/flow", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	var resp struct {
		LastFrameAgeS *float64 `json:"last_frame_age_s"`
	}
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	require.NotNil(t, resp.LastFrameAgeS, "active camera must report last_frame_age_s")
	require.GreaterOrEqual(t, *resp.LastFrameAgeS, float64(0))
	require.Less(t, *resp.LastFrameAgeS, float64(5), "age should be near zero right after a frame")

	// Camera with a hub but no frames ever: age must be null (#490).
	empty := camMgr.GetOrCreateHub("flow-silent")
	require.NotNil(t, empty)
	rr2 := doRequest(t, h.Routes(), http.MethodGet, "/api/cameras/flow-silent/flow", nil, "", "")
	require.Equal(t, http.StatusOK, rr2.Code)
	var resp2 struct {
		LastFrameAgeS *float64 `json:"last_frame_age_s"`
	}
	require.NoError(t, json.NewDecoder(rr2.Body).Decode(&resp2))
	require.Nil(t, resp2.LastFrameAgeS, "never-had-a-frame camera must report null")
}
