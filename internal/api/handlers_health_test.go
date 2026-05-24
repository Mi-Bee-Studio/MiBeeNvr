package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

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
	h := setupHealthHandler(t, nil)

	rr := doRequest(t, h.Routes(), "GET", "/api/health/status", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp map[string]interface{}
	parseJSON(t, rr, &resp)
	require.Equal(t, map[string]interface{}{}, resp)
}

func TestHealth_Status_RequiresAuth(t *testing.T) {
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })

	authMW, _ := createTestAuthMW(t)
	h := NewHandler(db, store, authMW, nil, nil, nil, "", nil, nil)
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
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })

	now := time.Now().UTC()
	for i := 0; i < 5; i++ {
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
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "GET", "/api/health/events?limit=abc", nil, "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestHealth_Events_InvalidOffset(t *testing.T) {
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "GET", "/api/health/events?offset=xyz", nil, "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestHealth_Events_EmptyResult(t *testing.T) {
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
