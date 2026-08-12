package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/health"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/stretchr/testify/require"
)

// --- mock HealthManager ---

type mockHealthManager struct {
	allHealth  map[string]*model.CameraHealth
	cameraByID map[string]*model.CameraHealth
}

func (m *mockHealthManager) GetAllHealth() map[string]*model.CameraHealth {
	return m.allHealth
}

func (m *mockHealthManager) GetCameraHealth(cameraID string) *model.CameraHealth {
	return m.cameraByID[cameraID]
}

// setupHealthHandler creates a Handler with a mock HealthManager for testing.
func setupHealthHandler(t *testing.T, mgr HealthManager) *Handler {
	t.Helper()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)
	h.healthMgr = mgr
	return h
}

// --- handleGetHealthStatus tests ---

func TestHealth_Status_OK(t *testing.T) {
	t.Parallel()
	mgr := &mockHealthManager{
		allHealth: map[string]*model.CameraHealth{
			"cam-1": {CameraID: "cam-1", LatestStatus: "healthy"},
			"cam-2": {CameraID: "cam-2", LatestStatus: "warning"},
		},
	}
	h := setupHealthHandler(t, mgr)

	rr := doRequest(t, h.Routes(), "GET", "/api/health/status", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp map[string]interface{}
	parseJSON(t, rr, &resp)
	cameras, ok := resp["cam-1"]
	require.True(t, ok, "expected cam-1 in response")
	_ = cameras
}

func TestHealth_Status_NilManager(t *testing.T) {
	t.Parallel()
	h := setupHealthHandler(t, nil)

	rr := doRequest(t, h.Routes(), "GET", "/api/health/status", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp map[string]interface{}
	parseJSON(t, rr, &resp)
	require.Equal(t, map[string]interface{}{}, resp)
}

func TestHealth_Status_RequiresAuth(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })

	authMW, _ := createTestAuthMW(t)
	h := NewHandler(db, store, authMW, nil, nil, nil, "", nil, nil, nil, nil, nil)
	h.healthMgr = &mockHealthManager{
		allHealth: map[string]*model.CameraHealth{
			"cam-1": {CameraID: "cam-1", LatestStatus: "healthy"},
		},
	}

	// No auth → should get 401
	rr := doRequest(t, h.Routes(), "GET", "/api/health/status", nil, "", "")
	require.Equal(t, http.StatusUnauthorized, rr.Code)

	// With auth → 200
	rr = doRequest(t, h.Routes(), "GET", "/api/health/status", nil, "admin", "password123")
	require.Equal(t, http.StatusOK, rr.Code)
}

// --- handleGetHealthEvents tests ---

func TestHealth_Events_OK(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })

	// Seed health events
	now := time.Now().UTC()
	evt1 := model.HealthEvent{
		CameraID:  "cam-1",
		EventType: "connection_lost",
		Status:    "error",
		Message:   "camera disconnected",
		CreatedAt: now,
	}
	evt2 := model.HealthEvent{
		CameraID:  "cam-1",
		EventType: "connection_restored",
		Status:    "healthy",
		Message:   "camera reconnected",
		CreatedAt: now.Add(1 * time.Minute),
	}
	require.NoError(t, db.InsertHealthEvent(context.Background(), evt1))
	require.NoError(t, db.InsertHealthEvent(context.Background(), evt2))

	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "GET", "/api/health/events", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp struct {
		Events []model.HealthEvent `json:"events"`
		Total  int                 `json:"total"`
	}
	parseJSON(t, rr, &resp)
	require.Equal(t, 2, resp.Total)
	require.Len(t, resp.Events, 2)
}

func TestHealth_Events_FilterByCameraID(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })

	now := time.Now().UTC()
	require.NoError(t, db.InsertHealthEvent(context.Background(), model.HealthEvent{
		CameraID: "cam-1", EventType: "connection_lost", Status: "error", CreatedAt: now,
	}))
	require.NoError(t, db.InsertHealthEvent(context.Background(), model.HealthEvent{
		CameraID: "cam-2", EventType: "freeze_detected", Status: "warning", CreatedAt: now,
	}))

	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "GET", "/api/health/events?camera_id=cam-1", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp struct {
		Events []model.HealthEvent `json:"events"`
		Total  int                 `json:"total"`
	}
	parseJSON(t, rr, &resp)
	require.Equal(t, 1, resp.Total)
	require.Len(t, resp.Events, 1)
	require.Equal(t, "cam-1", resp.Events[0].CameraID)
}

func TestHealth_Events_WithPagination(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })

	now := time.Now().UTC()
	for i := range 5 {
		require.NoError(t, db.InsertHealthEvent(context.Background(), model.HealthEvent{
			CameraID: "cam-1", EventType: "connection_lost", Status: "error", CreatedAt: now.Add(time.Duration(i) * time.Minute),
		}))
	}

	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "GET", "/api/health/events?limit=2&offset=0", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp struct {
		Events []model.HealthEvent `json:"events"`
		Total  int                 `json:"total"`
	}
	parseJSON(t, rr, &resp)
	require.Equal(t, 5, resp.Total)
	require.Len(t, resp.Events, 2)
}

func TestHealth_Events_InvalidLimit(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "GET", "/api/health/events?limit=abc", nil, "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestHealth_Events_InvalidOffset(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "GET", "/api/health/events?offset=xyz", nil, "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestHealth_Events_EmptyResult(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "GET", "/api/health/events", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp struct {
		Events []model.HealthEvent `json:"events"`
		Total  int                 `json:"total"`
	}
	parseJSON(t, rr, &resp)
	require.Equal(t, 0, resp.Total)
	require.Empty(t, resp.Events)
}

// --- handleGetCameraHealth tests ---

func TestHealth_CameraHealth_OK(t *testing.T) {
	t.Parallel()
	mgr := &mockHealthManager{
		cameraByID: map[string]*model.CameraHealth{
			"front-door": {CameraID: "front-door", LatestStatus: "healthy", LatestEvent: "connection_restored"},
		},
	}
	h := setupHealthHandler(t, mgr)

	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/front-door/health", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp model.CameraHealth
	parseJSON(t, rr, &resp)
	require.Equal(t, "front-door", resp.CameraID)
	require.Equal(t, "healthy", resp.LatestStatus)
}

func TestHealth_CameraHealth_NotFound(t *testing.T) {
	t.Parallel()
	mgr := &mockHealthManager{
		cameraByID: map[string]*model.CameraHealth{},
	}
	h := setupHealthHandler(t, mgr)

	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/nonexistent/health", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
}

// createTestAuthMW returns a real auth middleware with known credentials.
func createTestAuthMW(t *testing.T) (func(http.Handler) http.Handler, func()) {
	t.Helper()
	// Use a temp dir for config so hash-file store works
	dir := t.TempDir()
	authMW, cleanup, err := createTestAuthMiddleware("admin", "password123", dir)
	require.NoError(t, err)
	return authMW, cleanup
}

// createTestAuthMiddleware is a helper that creates a real BasicAuth middleware.
func createTestAuthMiddleware(username, password, dir string) (func(http.Handler) http.Handler, func(), error) {
	// Hash the password with bcrypt
	mw, cleanup := newTestAuthMiddleware(username, password)
	return mw, cleanup, nil
}

// newTestAuthMiddleware creates a real auth middleware for testing.
func newTestAuthMiddleware(username, password string) (func(http.Handler) http.Handler, func()) {
	// Import bcrypt here to avoid import in non-test files
	// Use middleware package directly
	authMW := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u, p, ok := r.BasicAuth()
			if !ok || u != username || p != password {
				w.Header().Set("WWW-Authenticate", `Basic realm="MiBee NVR"`)
				w.WriteHeader(http.StatusUnauthorized)
				json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
	return authMW, func() {}
}

// --- /api/health camera aggregation tests ---

func TestHealth_CameraAggregation(t *testing.T) {
	t.Parallel()
	mgr := &mockHealthManager{
		allHealth: map[string]*model.CameraHealth{
			"cam-1": {CameraID: "cam-1", LatestStatus: "healthy", Score: 100},
			"cam-2": {CameraID: "cam-2", LatestStatus: "reconnecting", Score: 40},
			"cam-3": {CameraID: "cam-3", LatestStatus: "error", Score: 10},
		},
	}
	h := setupHealthHandler(t, mgr)

	// Seed cameras in DB so names are available
	ctx := context.Background()
	h.db.UpsertCamera(ctx, "cam-1", "Front Door", "rtsp", "h264", "rtsp://x", "", "", "", "", "", "")
	h.db.UpsertCamera(ctx, "cam-2", "Back Yard", "rtsp", "h264", "rtsp://x", "", "", "", "", "", "")
	h.db.UpsertCamera(ctx, "cam-3", "Garage", "rtsp", "h264", "rtsp://x", "", "", "", "", "", "")

	rr := doRequest(t, h.Routes(), "GET", "/api/health", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	var body HealthResponse
	parseJSON(t, rr, &body)
	require.NotNil(t, body.Cameras)
	require.Equal(t, 3, body.Cameras.Total)
	require.Equal(t, 1, body.Cameras.Recording)
	require.Equal(t, 1, body.Cameras.Reconnecting)
	require.Equal(t, 1, body.Cameras.Error)
	require.Equal(t, 0, body.Cameras.Offline)
	require.Len(t, body.Cameras.Details, 3)

	// Verify details contain expected camera data
	detailMap := map[string]CameraHealthDetail{}
	for _, d := range body.Cameras.Details {
		detailMap[d.ID] = d
	}
	require.Equal(t, "Front Door", detailMap["cam-1"].Name)
	require.Equal(t, 100, detailMap["cam-1"].Score)
	require.Equal(t, "healthy", detailMap["cam-1"].Status)
}

func TestHealth_CameraAggregation_NilManager(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)
	// No healthMgr set

	rr := doRequest(t, h.Routes(), "GET", "/api/health", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	var body HealthResponse
	parseJSON(t, rr, &body)
	require.Nil(t, body.Cameras)
}

func TestHealth_CameraAggregation_EmptyHealth(t *testing.T) {
	t.Parallel()
	mgr := &mockHealthManager{
		allHealth: map[string]*model.CameraHealth{},
	}
	h := setupHealthHandler(t, mgr)

	rr := doRequest(t, h.Routes(), "GET", "/api/health", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	var body HealthResponse
	parseJSON(t, rr, &body)
	require.NotNil(t, body.Cameras)
	require.Equal(t, 0, body.Cameras.Total)
	require.Empty(t, body.Cameras.Details)
}

func TestHealth_CameraAggregation_OfflineStatus(t *testing.T) {
	t.Parallel()
	mgr := &mockHealthManager{
		allHealth: map[string]*model.CameraHealth{
			"cam-1": {CameraID: "cam-1", LatestStatus: "disabled", Score: 0},
			"cam-2": {CameraID: "cam-2", LatestStatus: "unknown", Score: 0},
		},
	}
	h := setupHealthHandler(t, mgr)

	rr := doRequest(t, h.Routes(), "GET", "/api/health", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	var body HealthResponse
	parseJSON(t, rr, &body)
	require.NotNil(t, body.Cameras)
	require.Equal(t, 2, body.Cameras.Total)
	require.Equal(t, 0, body.Cameras.Recording)
	require.Equal(t, 0, body.Cameras.Reconnecting)
	require.Equal(t, 0, body.Cameras.Error)
	require.Equal(t, 2, body.Cameras.Offline)
}

// --- /api/health/cameras endpoint tests ---

func TestHealthCameras_OK(t *testing.T) {
	t.Parallel()
	mgr := &mockHealthManager{
		allHealth: map[string]*model.CameraHealth{
			"cam-1": {CameraID: "cam-1", LatestStatus: "healthy", Score: 100, ScoreFactors: []string{"recording"}},
			"cam-2": {CameraID: "cam-2", LatestStatus: "error", Score: 10, ScoreFactors: []string{"connection_lost"}},
		},
	}
	h := setupHealthHandler(t, mgr)

	rr := doRequest(t, h.Routes(), "GET", "/api/health/cameras", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp map[string]*model.CameraHealth
	parseJSON(t, rr, &resp)
	require.Len(t, resp, 2)
	require.NotNil(t, resp["cam-1"])
	require.Equal(t, 100, resp["cam-1"].Score)
	require.Equal(t, "healthy", resp["cam-1"].LatestStatus)
}

func TestHealthCameras_NilManager(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "GET", "/api/health/cameras", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp map[string]*model.CameraHealth
	parseJSON(t, rr, &resp)
	require.Equal(t, map[string]*model.CameraHealth{}, resp)
}

func TestHealthCameras_NilHealth(t *testing.T) {
	t.Parallel()
	mgr := &mockHealthManager{
		allHealth: nil,
	}
	h := setupHealthHandler(t, mgr)

	rr := doRequest(t, h.Routes(), "GET", "/api/health/cameras", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp map[string]*model.CameraHealth
	parseJSON(t, rr, &resp)
	require.Equal(t, map[string]*model.CameraHealth{}, resp)
}

func TestHealthCameras_PublicEndpoint(t *testing.T) {
	t.Parallel()
	// Verify /api/health/cameras is accessible without auth
	mgr := &mockHealthManager{
		allHealth: map[string]*model.CameraHealth{
			"cam-1": {CameraID: "cam-1", LatestStatus: "healthy", Score: 100},
		},
	}
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	authMW, _ := createTestAuthMW(t)
	h := NewHandler(db, store, authMW, nil, nil, nil, "", nil, nil, nil, nil, nil)
	h.healthMgr = mgr

	rr := doRequest(t, h.Routes(), "GET", "/api/health/cameras", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
}

// --- mock StabilityProvider ---

type mockStabilityProvider struct {
	allStability    map[string]*health.StabilityData
	cameraStability map[string]*health.StabilityData
}

func (m *mockStabilityProvider) GetAllStability() map[string]*health.StabilityData {
	return m.allStability
}

func (m *mockStabilityProvider) GetStability(cameraID string) *health.StabilityData {
	return m.cameraStability[cameraID]
}

// setupStabilityHandler creates a Handler with a mock StabilityProvider for testing.
func setupStabilityHandler(t *testing.T, provider StabilityProvider) *Handler {
	t.Helper()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)
	h.stabilityProvider = provider
	return h
}

// --- handleGetStability tests ---

func TestStability_All_OK(t *testing.T) {
	t.Parallel()
	provider := &mockStabilityProvider{
		allStability: map[string]*health.StabilityData{
			"cam1": {
				UptimePercent: 95.8,
				TotalFailures: 12,
				MTBF:          "2h30m0s",
				AvgSession:    "45m0s",
				CurrentStatus: "online",
				Trend:         "stable",
			},
			"cam2": {
				UptimePercent: 80.0,
				TotalFailures: 25,
				MTBF:          "1h0m0s",
				AvgSession:    "20m0s",
				CurrentStatus: "offline",
				Trend:         "degrading",
			},
		},
	}
	h := setupStabilityHandler(t, provider)

	rr := doRequest(t, h.Routes(), "GET", "/api/health/stability", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp struct {
		Cameras map[string]*health.StabilityData `json:"cameras"`
	}
	parseJSON(t, rr, &resp)
	require.Len(t, resp.Cameras, 2)

	cam1, ok := resp.Cameras["cam1"]
	require.True(t, ok)
	require.Equal(t, 95.8, cam1.UptimePercent)
	require.Equal(t, 12, cam1.TotalFailures)
	require.Equal(t, "2h30m0s", cam1.MTBF)
	require.Equal(t, "45m0s", cam1.AvgSession)
	require.Equal(t, "online", cam1.CurrentStatus)
	require.Equal(t, "stable", cam1.Trend)

	cam2, ok := resp.Cameras["cam2"]
	require.True(t, ok)
	require.Equal(t, "degrading", cam2.Trend)
}

func TestStability_All_NilProvider(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)
	// No stabilityProvider set

	rr := doRequest(t, h.Routes(), "GET", "/api/health/stability", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp struct {
		Cameras map[string]*health.StabilityData `json:"cameras"`
	}
	parseJSON(t, rr, &resp)
	require.Empty(t, resp.Cameras)
}

func TestStability_All_EmptyResult(t *testing.T) {
	t.Parallel()
	provider := &mockStabilityProvider{
		allStability: map[string]*health.StabilityData{},
	}
	h := setupStabilityHandler(t, provider)

	rr := doRequest(t, h.Routes(), "GET", "/api/health/stability", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp struct {
		Cameras map[string]*health.StabilityData `json:"cameras"`
	}
	parseJSON(t, rr, &resp)
	require.Empty(t, resp.Cameras)
}

// --- handleGetCameraStability tests ---

func TestStability_Camera_OK(t *testing.T) {
	t.Parallel()
	provider := &mockStabilityProvider{
		cameraStability: map[string]*health.StabilityData{
			"front-door": {
				UptimePercent: 99.5,
				TotalFailures: 2,
				MTBF:          "12h0m0s",
				AvgSession:    "6h0m0s",
				LastFailure:   "2026-05-25T10:00:00Z",
				CurrentStatus: "online",
				Trend:         "improving",
			},
		},
	}
	h := setupStabilityHandler(t, provider)

	rr := doRequest(t, h.Routes(), "GET", "/api/health/stability/front-door", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp health.StabilityData
	parseJSON(t, rr, &resp)
	require.Equal(t, 99.5, resp.UptimePercent)
	require.Equal(t, 2, resp.TotalFailures)
	require.Equal(t, "12h0m0s", resp.MTBF)
	require.Equal(t, "online", resp.CurrentStatus)
	require.Equal(t, "improving", resp.Trend)
}

func TestStability_Camera_NotFound(t *testing.T) {
	t.Parallel()
	provider := &mockStabilityProvider{
		cameraStability: map[string]*health.StabilityData{},
	}
	h := setupStabilityHandler(t, provider)

	rr := doRequest(t, h.Routes(), "GET", "/api/health/stability/nonexistent", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestStability_Camera_NilProvider(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "GET", "/api/health/stability/cam1", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestStability_Camera_RequiresAuth(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })

	authMW, _ := createTestAuthMW(t)
	h := NewHandler(db, store, authMW, nil, nil, nil, "", nil, nil, nil, nil, nil)
	h.stabilityProvider = &mockStabilityProvider{
		cameraStability: map[string]*health.StabilityData{
			"cam1": {UptimePercent: 100, CurrentStatus: "online", Trend: "stable"},
		},
	}

	// No auth → should get 401
	rr := doRequest(t, h.Routes(), "GET", "/api/health/stability/cam1", nil, "", "")
	require.Equal(t, http.StatusUnauthorized, rr.Code)

	// With auth → 200
	rr = doRequest(t, h.Routes(), "GET", "/api/health/stability/cam1", nil, "admin", "password123")
	require.Equal(t, http.StatusOK, rr.Code)
}
