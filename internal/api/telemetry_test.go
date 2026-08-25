package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/middleware"
	"github.com/stretchr/testify/require"
)

func TestHandleTelemetry_ValidPayload(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()

	// Pre-compute a known bcrypt hash for "admin123"
	validHash, err := middleware.HashPassword("admin123")
	require.NoError(t, err)
	authMW, _ := middleware.NewAuthMiddleware(middleware.AuthProvider{
		GetUsername: func() string { return "admin" },
		GetHash:     func() string { return validHash },
	}, "", middleware.AuthRateLimitConfig{})

	h := NewHandler(db, store, authMW, nil, nil, nil, "", nil, nil, nil, nil, nil)

	body := `{"event":"playback_start","camera_id":"front-door","duration_ms":5000}`
	req := httptest.NewRequest(http.MethodPost, "/api/telemetry", bytes.NewBufferString(body))
	req.SetBasicAuth("admin", "admin123")
	rr := httptest.NewRecorder()

	telemetryRateLimiter()(http.HandlerFunc(h.HandleTelemetry)).ServeHTTP(rr, req)

	require.Equal(t, http.StatusNoContent, rr.Code)
}

// TestHandleTelemetry_AggregatesFlowMetrics verifies the #469 Phase 3
// aggregation: "live_latency" beacons set the latency gauge and
// "playback_stall" beacons increment the stall counter.
func TestHandleTelemetry_AggregatesFlowMetrics(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()

	m := metrics.NewMetrics()
	SetAPIMetrics(m)
	t.Cleanup(func() { SetAPIMetrics(nil) })

	h := NewHandler(db, store, noopAuthMW(), nil, nil, nil, "", nil, nil, nil, nil, nil)

	post := func(body string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/telemetry", bytes.NewBufferString(body))
		rr := httptest.NewRecorder()
		h.HandleTelemetry(rr, req)
		require.Equal(t, http.StatusNoContent, rr.Code)
	}

	post(`{"event":"live_latency","camera_id":"cam-1","duration_ms":830,"details":{"protocol":"ws"}}`)
	post(`{"event":"playback_stall","camera_id":"cam-1","details":{"protocol":"ws","kind":"decode"}}`)

	families, err := m.Registry.Gather()
	require.NoError(t, err)

	var latencyFound, stallFound bool
	for _, f := range families {
		switch f.GetName() {
		case "nvr_playback_live_latency_ms":
			for _, metric := range f.GetMetric() {
				if metric.GetGauge().GetValue() == 830 {
					latencyFound = true
				}
			}
		case "nvr_playback_stalls_total":
			for _, metric := range f.GetMetric() {
				if metric.GetCounter().GetValue() >= 1 {
					stallFound = true
				}
			}
		}
	}
	require.True(t, latencyFound, "live_latency beacon must set nvr_playback_live_latency_ms")
	require.True(t, stallFound, "playback_stall beacon must increment nvr_playback_stalls_total")
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
	}, "", middleware.AuthRateLimitConfig{})

	h := NewHandler(db, store, authMW, nil, nil, nil, "", nil, nil, nil, nil, nil)

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
	for i := range 10 {
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
