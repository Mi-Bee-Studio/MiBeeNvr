package api

// Tests for handlers_vision.go (#578): heartbeat + status with and without
// a real vision.Coordinator (db/provider nil are supported constructor
// values — only the health tracker matters here).

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/middleware"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/vision"
	"github.com/stretchr/testify/require"
)

func TestVision_NilCoordinatorDegrades(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)
	routes := h.Routes()

	// Heartbeat without coordinator → 503 (public, unauthenticated route).
	rr := doRequest(t, routes, http.MethodPost, "/api/vision/heartbeat",
		strings.NewReader(`{"status":"ok"}`), "", "")
	require.Equal(t, http.StatusServiceUnavailable, rr.Code)

	// Status without coordinator → {"enabled":false}, 200.
	rr = doRequest(t, routes, http.MethodGet, "/api/vision/status", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), `"enabled":false`)
}

func TestVision_HeartbeatAndStatus(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)
	h.SetVisionCoordinator(vision.NewCoordinator(
		func() config.VisionConfig { return config.VisionConfig{HeartbeatTimeoutSecs: 30} },
		func() string { return store.RootDir() },
		event.NewEventBus(4),
		nil, nil,
	))
	routes := h.Routes()

	// Status before any heartbeat: enabled, no last_seen (zero time omitted).
	rr := doRequest(t, routes, http.MethodGet, "/api/vision/status", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), `"enabled":true`)
	require.NotContains(t, rr.Body.String(), "last_seen")
	require.NotContains(t, rr.Body.String(), "0001-01-01")

	// Bad body → 400.
	rr = doRequest(t, routes, http.MethodPost, "/api/vision/heartbeat", strings.NewReader(`{bad`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)

	// Healthy heartbeat → ok + push_enabled true; status now carries fields.
	body := `{"status":"healthy","device":"vision-1","queue_depth":2,"processed_count":9,"skip_cameras":["cam-x"]}`
	rr = doRequest(t, routes, http.MethodPost, "/api/vision/heartbeat", strings.NewReader(body), "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), `"push_enabled":true`)

	rr = doRequest(t, routes, http.MethodGet, "/api/vision/status", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), `"device":"vision-1"`)
	require.Contains(t, rr.Body.String(), `"last_seen"`)
	require.Contains(t, rr.Body.String(), "cam-x")
}

func TestVision_HeartbeatV2DropsAndMetrics(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)
	h.SetVisionCoordinator(vision.NewCoordinator(
		func() config.VisionConfig { return config.VisionConfig{HeartbeatTimeoutSecs: 30} },
		func() string { return store.RootDir() },
		event.NewEventBus(4),
		nil, nil,
	))
	routes := h.Routes()

	// Seed recordings: precise-id drop target (sub-layer join), range-fallback
	// target, and a terminal-status row that must stay untouched.
	base := time.Date(2026, 9, 2, 4, 0, 0, 0, time.UTC)
	seedRecording(t, db, makeRecording("drop-precise", "cam-1", "mp4", base, false))
	seedRecording(t, db, makeRecording("drop-range", "cam-1", "mp4", base.Add(10*time.Minute), false))
	seedRecording(t, db, makeRecording("keep-done", "cam-1", "mp4", base.Add(11*time.Minute), false))
	require.NoError(t, db.UpdateRecordingAIStatus(t.Context(), "keep-done", "completed", ""))

	body := `{
		"protocol": 2,
		"status": "healthy",
		"device": "cuda",
		"queue_depth": 12,
		"processed_count": 1024,
		"skip_cameras": ["cam-x"],
		"drops": {
			"seq": 7,
			"ranges": [
				{"camera_id":"cam-1","reason":"queue_full","count":2,
				 "from":"2026-09-02T04:00:01Z","to":"2026-09-02T04:01:00Z",
				 "ids":["drop-precise","1786611799700038099#1756742400000000000"]},
				{"camera_id":"cam-1","reason":"ttl_expired","count":1,
				 "from":"2026-09-02T04:09:00Z","to":"2026-09-02T04:12:00Z"}
			]
		},
		"metrics": {
			"queue_capacity": 64, "decode_workers": 2, "workers_busy": 1,
			"received_total": 2049, "dropped_total": 1045,
			"dropped_queue_full": 1040, "dropped_ttl": 5,
			"events_emitted": 88, "seg_ms_p50": 16600, "seg_ms_p90": 76400,
			"decoded_queue_depth": 1, "mem_available_mb": 620, "load1": 2.1
		}
	}`
	rr := doRequest(t, routes, http.MethodPost, "/api/vision/heartbeat", strings.NewReader(body), "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), `"ack_drops":7`)

	// Marking: precise id marked; the non-existent joined sub-id is a no-op;
	// the id-less range covers drop-range; keep-done stays completed.
	for id, want := range map[string]string{
		"drop-precise": "skipped",
		"drop-range":   "skipped",
		"keep-done":    "completed",
	} {
		rec, err := db.GetRecording(t.Context(), id)
		require.NoError(t, err)
		require.Equal(t, want, rec.AIStatus, id)
	}

	// Status now carries metrics + the marked-drop counter.
	rr = doRequest(t, routes, http.MethodGet, "/api/vision/status", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), `"metrics"`)
	require.Contains(t, rr.Body.String(), `"decode_workers":2`)
	require.Contains(t, rr.Body.String(), `"drops_marked_total":2`)

	// Metrics history endpoint returns the recorded samples.
	rr = doRequest(t, routes, http.MethodGet, "/api/vision/metrics", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	var resp struct {
		Points []vision.VisionSample `json:"points"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.Len(t, resp.Points, 1)
	require.Equal(t, 12, resp.Points[0].QueueDepth)
	require.Equal(t, int64(1045), resp.Points[0].DroppedTotal)

	// A v1 (legacy) heartbeat after a v2 one keeps working and does not
	// produce an ack field for a report that wasn't sent.
	rr = doRequest(t, routes, http.MethodPost, "/api/vision/heartbeat",
		strings.NewReader(`{"status":"healthy","device":"cuda","queue_depth":0,"processed_count":1025}`), "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.NotContains(t, rr.Body.String(), "ack_drops")
}

func TestVision_HeartbeatV2UnparseableRangeTimesStill200(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)
	h.SetVisionCoordinator(vision.NewCoordinator(
		func() config.VisionConfig { return config.VisionConfig{HeartbeatTimeoutSecs: 30} },
		func() string { return store.RootDir() },
		event.NewEventBus(4),
		nil, nil,
	))
	routes := h.Routes()

	// Garbage range (no ids, bad times): marking is skipped, heartbeat still
	// succeeds and is acked — the consumer must not retry forever.
	body := `{"status":"healthy","drops":{"seq":9,"ranges":[
		{"camera_id":"cam-9","reason":"queue_full","count":1,"from":"garbage","to":"garbage"}]}}`
	rr := doRequest(t, routes, http.MethodPost, "/api/vision/heartbeat", strings.NewReader(body), "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), `"ack_drops":9`)
}

// 多实例心跳归因:携带 Bearer API Key 的心跳落到 key 关联的实例
// (vision.instances[].api_key_name);未关联的 key/匿名落 default。
// GET /api/vision/status 的 instances[] 按实例展开健康状态。
func TestVision_HeartbeatInstanceAttribution(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)

	keyStore := middleware.NewAPIKeyStore()
	keyStore.SetKeys(map[string]string{
		"mbv_testkey0001": "key-a",
		"mbv_testkey0002": "key-b",
	})
	h.SetAPIKeyStore(keyStore)

	enabled := true
	h.SetVisionCoordinator(vision.NewCoordinator(
		func() config.VisionConfig {
			return config.VisionConfig{
				HeartbeatTimeoutSecs: 30,
				Instances: []config.VisionInstance{
					{Name: "a", URL: "http://a:9091", APIKeyName: "key-a"},
					{Name: "b", URL: "http://b:9091", APIKeyName: "key-b", Enabled: &enabled},
				},
			}
		},
		func() string { return store.RootDir() },
		event.NewEventBus(4),
		nil, nil,
	))
	// 生产链路里 APIKey 中间件挂在根路由(Routes() 之外)——测试手动补一层,
	// 否则 Bearer 不会被识别、归因永远落 default。
	routes := middleware.APIKeyAuthMiddleware(keyStore, h.Routes())

	beat := func(key, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/vision/heartbeat", strings.NewReader(body))
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		rr := httptest.NewRecorder()
		routes.ServeHTTP(rr, req)
		return rr
	}

	// key-a 的心跳 → 只实例 a 健康。
	rr := beat("mbv_testkey0001", `{"status":"healthy","device":"cuda-a"}`)
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), `"push_enabled":true`)

	// key-b 的心跳带 degraded → 实例 b 不健康(但请求本身成功)。
	rr = beat("mbv_testkey0002", `{"status":"degraded","device":"cuda-b"}`)
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), `"push_enabled":false`)

	rr = doRequest(t, routes, http.MethodGet, "/api/vision/status", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	var resp struct {
		Instances []vision.InstanceStatus `json:"instances"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.Len(t, resp.Instances, 2)
	require.Equal(t, "a", resp.Instances[0].Name)
	require.True(t, resp.Instances[0].Healthy)
	require.Equal(t, "cuda-a", resp.Instances[0].Device)
	require.Equal(t, "b", resp.Instances[1].Name)
	require.False(t, resp.Instances[1].Healthy)
	require.Equal(t, "cuda-b", resp.Instances[1].Device)

	// ?instance=b 的 metrics 端点返回实例 b 的采样。
	req := httptest.NewRequest(http.MethodGet, "/api/vision/metrics?instance=b", nil)
	rr = httptest.NewRecorder()
	routes.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), `"instance":"b"`)
}
