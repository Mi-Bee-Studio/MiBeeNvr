package api

	import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/middleware"
	"github.com/stretchr/testify/require"
)

func TestHandleTelemetry_ValidPayload(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()

	authMW, _ := middleware.NewAuthMiddleware(middleware.AuthProvider{
		GetUsername: func() string { return "admin" },
		GetHash:     func() string { return "$2a$10$abcdefghijklmnopqrstuvwxyz1234567890" },
	}, "")
	// Pre-compute a known bcrypt hash for "admin123"
	validHash, err := middleware.HashPassword("admin123")
	require.NoError(t, err)
	authMW, _ = middleware.NewAuthMiddleware(middleware.AuthProvider{
		GetUsername: func() string { return "admin" },
		GetHash:     func() string { return validHash },
	}, "")

	h := NewHandler(db, store, authMW, nil, nil, nil, "", nil, nil, nil)

	body := `{"event":"playback_start","camera_id":"front-door","duration_ms":5000}`
	req := httptest.NewRequest(http.MethodPost, "/api/telemetry", bytes.NewBufferString(body))
	req.SetBasicAuth("admin", "admin123")
	rr := httptest.NewRecorder()

	telemetryRateLimiter()(http.HandlerFunc(h.HandleTelemetry)).ServeHTTP(rr, req)

	require.Equal(t, http.StatusNoContent, rr.Code)
}

func TestHandleTelemetry_InvalidJSON(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	req := httptest.NewRequest(http.MethodPost, "/api/telemetry", bytes.NewBufferString("not-json"))
	rr := httptest.NewRecorder()
	h.HandleTelemetry(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code)
	var resp map[string]string
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	require.Equal(t, "invalid JSON", resp["error"])
}

func TestHandleTelemetry_MissingEvent(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	body := `{"camera_id":"front-door"}`
	req := httptest.NewRequest(http.MethodPost, "/api/telemetry", bytes.NewBufferString(body))
	rr := httptest.NewRecorder()
	h.HandleTelemetry(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code)
	var resp map[string]string
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	require.Equal(t, "event is required", resp["error"])
}

func TestHandleTelemetry_Unauthenticated(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()

	validHash, err := middleware.HashPassword("admin123")
	require.NoError(t, err)
	authMW, _ := middleware.NewAuthMiddleware(middleware.AuthProvider{
		GetUsername: func() string { return "admin" },
		GetHash:     func() string { return validHash },
	}, "")

	h := NewHandler(db, store, authMW, nil, nil, nil, "", nil, nil, nil)

	body := `{"event":"playback_start","camera_id":"front-door"}`
	req := httptest.NewRequest(http.MethodPost, "/api/telemetry", bytes.NewBufferString(body))
	// No auth
	rr := httptest.NewRecorder()

	// Through the full Routes() to get auth middleware
	h.Routes().ServeHTTP(rr, req)

	require.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestHandleTelemetry_RateLimiting(t *testing.T) {
	t.Parallel()
	db, _ := setupTestDB(t)
	defer db.Close()

	rl := telemetryRateLimiter()

	body := `{"event":"playback_start","camera_id":"front-door","duration_ms":100}`
	for i := 0; i < 10; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/telemetry", bytes.NewBufferString(body))
		rr := httptest.NewRecorder()
		rl(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})).ServeHTTP(rr, req)
		require.Equal(t, http.StatusOK, rr.Code, "request %d should pass", i+1)
	}

	// 11th request should be rate limited
	req := httptest.NewRequest(http.MethodPost, "/api/telemetry", bytes.NewBufferString(body))
	rr := httptest.NewRecorder()
	rl(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rr, req)
	require.Equal(t, http.StatusTooManyRequests, rr.Code)
}