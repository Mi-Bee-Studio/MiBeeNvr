package api

// Tests for handlers_vision.go (#578): heartbeat + status with and without
// a real vision.Coordinator (db/provider nil are supported constructor
// values — only the health tracker matters here).

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
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
