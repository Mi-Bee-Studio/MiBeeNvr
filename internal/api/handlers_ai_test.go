package api

// Tests for handlers_ai.go — AI event ingestion (MiBeeVision collaboration)
// and read APIs (list/get/stats). Covers the POST validation paths, the
// API-key-auth gate on creation, and the GET read paths (#232).

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/middleware"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/stretchr/testify/require"
)

// withAPIKey wraps a handler so the request context carries an authenticated
// API key (mirrors what APIKeyAuthMiddleware sets). Used to exercise the
// post-auth path of handleCreateAIEvent without standing up the full key map.
func withAPIKey(name string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := middleware.WithAPIKeyName(r.Context(), name)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// aiTestHandler builds a Handler wired with BasicAuth + an API-key-wrapped
// router so both GET (BasicAuth) and POST (API key) AI routes are reachable.
func aiTestHandler(t *testing.T, username, password, apiKeyName string) (*Handler, http.Handler) {
	t.Helper()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := testHandlerWithAuth(db, store, username, "$2a$10$abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJKLMN")
	routes := withAPIKey(apiKeyName, h.Routes())
	return h, routes
}

func TestAI_CreateEvent_RequiresAPIKey(t *testing.T) {
	t.Parallel()
	// No API-key wrapper → the handler's IsAPIKeyAuthenticated check must reject.
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := testHandlerWithAuth(db, store, "admin", "$2a$10$abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJKLMN")

	body := bytes.NewBufferString(`{"camera_id":"cam-1","event_type":"zone_intrusion"}`)
	rr := doRequest(t, h.Routes(), "POST", "/api/ai/events", body, "admin", "wrong")
	require.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestAI_CreateEvent_InvalidBody(t *testing.T) {
	t.Parallel()
	_, routes := aiTestHandler(t, "admin", "p", "vision")
	// Malformed JSON
	rr := doRequest(t, routes, "POST", "/api/ai/events", bytes.NewBufferString(`{bad json`), "admin", "p")
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestAI_CreateEvent_MissingRequiredFields(t *testing.T) {
	t.Parallel()
	_, routes := aiTestHandler(t, "admin", "p", "vision")
	// camera_id present but event_type missing
	rr := doRequest(t, routes, "POST", "/api/ai/events", bytes.NewBufferString(`{"camera_id":"cam-1"}`), "admin", "p")
	require.Equal(t, http.StatusBadRequest, rr.Code)

	// event_type present but camera_id missing
	rr = doRequest(t, routes, "POST", "/api/ai/events", bytes.NewBufferString(`{"event_type":"zone_intrusion"}`), "admin", "p")
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestAI_CreateEvent_Success(t *testing.T) {
	t.Parallel()
	h, routes := aiTestHandler(t, "admin", "p", "vision")

	payload := map[string]interface{}{
		"camera_id":    "cam-test",
		"recording_id": "rec-1",
		"event_type":   "zone_intrusion",
		"severity":     "warning",
		"zone_name":    "gate",
		"class_name":   "person",
		"confidence":   0.92,
		"frame_idx":    42,
		"bbox":         []float64{10, 20, 30, 40},
		"metadata":     map[string]string{"src": "vision"},
	}
	b, _ := json.Marshal(payload)
	rr := doRequest(t, routes, "POST", "/api/ai/events", bytes.NewBuffer(b), "admin", "p")
	require.Equal(t, http.StatusCreated, rr.Code, "body=%s", rr.Body.String())

	var resp struct {
		ID     int64  `json:"id"`
		Status string `json:"status"`
	}
	parseJSON(t, rr, &resp)
	require.Equal(t, "stored", resp.Status)
	require.Positive(t, resp.ID)

	// The stored event should be retrievable via the GET-by-id path.
	_ = h // (handler retained for future direct-DB assertions)
}

func TestAI_CreateEvent_DefaultSeverity(t *testing.T) {
	t.Parallel()
	_, routes := aiTestHandler(t, "admin", "p", "vision")
	// No severity → should default to "info" and still succeed.
	b, _ := json.Marshal(map[string]string{"camera_id": "cam-1", "event_type": "loitering"})
	rr := doRequest(t, routes, "POST", "/api/ai/events", bytes.NewBuffer(b), "admin", "p")
	require.Equal(t, http.StatusCreated, rr.Code, "body=%s", rr.Body.String())
}

func TestAI_ListEvents_Empty(t *testing.T) {
	t.Parallel()
	_, routes := aiTestHandler(t, "admin", "p", "vision")
	rr := doRequest(t, routes, "GET", "/api/ai/events", nil, "admin", "p")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp struct {
		Events []storage.AIEvent `json:"events"`
		Total  int               `json:"total"`
	}
	parseJSON(t, rr, &resp)
	require.Equal(t, 0, resp.Total)
	require.Empty(t, resp.Events) // nil → [] in handler
}

func TestAI_ListEvents_WithFilter(t *testing.T) {
	t.Parallel()
	_, routes := aiTestHandler(t, "admin", "p", "vision")

	// Seed two events.
	for _, et := range []string{"zone_intrusion", "loitering"} {
		b, _ := json.Marshal(map[string]string{"camera_id": "cam-1", "event_type": et})
		rr := doRequest(t, routes, "POST", "/api/ai/events", bytes.NewBuffer(b), "admin", "p")
		require.Equal(t, http.StatusCreated, rr.Code)
	}

	// Filter by event_type=zone_intrusion
	rr := doRequest(t, routes, "GET", "/api/ai/events?event_type=zone_intrusion", nil, "admin", "p")
	require.Equal(t, http.StatusOK, rr.Code)
	var resp struct {
		Events []storage.AIEvent `json:"events"`
		Total  int               `json:"total"`
	}
	parseJSON(t, rr, &resp)
	require.Equal(t, 1, resp.Total, "expected exactly one zone_intrusion event")
	require.Len(t, resp.Events, 1)
	require.Equal(t, "zone_intrusion", resp.Events[0].EventType)
}

func TestAI_GetEvent_NotFound(t *testing.T) {
	t.Parallel()
	_, routes := aiTestHandler(t, "admin", "p", "vision")
	rr := doRequest(t, routes, "GET", "/api/ai/events/999999", nil, "admin", "p")
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestAI_GetEvent_InvalidID(t *testing.T) {
	t.Parallel()
	_, routes := aiTestHandler(t, "admin", "p", "vision")
	rr := doRequest(t, routes, "GET", "/api/ai/events/notanumber", nil, "admin", "p")
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestAI_GetEvent_Found(t *testing.T) {
	t.Parallel()
	_, routes := aiTestHandler(t, "admin", "p", "vision")

	b, _ := json.Marshal(map[string]string{"camera_id": "cam-x", "event_type": "line_crossing"})
	rr := doRequest(t, routes, "POST", "/api/ai/events", bytes.NewBuffer(b), "admin", "p")
	require.Equal(t, http.StatusCreated, rr.Code)
	var created struct {
		ID int64 `json:"id"`
	}
	parseJSON(t, rr, &created)

	rr2 := doRequest(t, routes, "GET", "/api/ai/events/"+itoa(created.ID), nil, "admin", "p")
	require.Equal(t, http.StatusOK, rr2.Code)
	var got storage.AIEvent
	parseJSON(t, rr2, &got)
	require.Equal(t, created.ID, got.ID)
	require.Equal(t, "cam-x", got.CameraID)
	require.Equal(t, "line_crossing", got.EventType)
}

func TestAI_Stats_GlobalAndCamera(t *testing.T) {
	t.Parallel()
	_, routes := aiTestHandler(t, "admin", "p", "vision")

	// Seed events for two cameras.
	for _, cam := range []string{"cam-a", "cam-b"} {
		b, _ := json.Marshal(map[string]string{"camera_id": cam, "event_type": "zone_intrusion"})
		rr := doRequest(t, routes, "POST", "/api/ai/events", bytes.NewBuffer(b), "admin", "p")
		require.Equal(t, http.StatusCreated, rr.Code)
	}

	// Global stats (no camera_id) — must NOT 400 (#213 regression guard).
	rr := doRequest(t, routes, "GET", "/api/ai/stats", nil, "admin", "p")
	require.Equal(t, http.StatusOK, rr.Code)
	var global struct {
		CameraID string                 `json:"camera_id"`
		Stats    []storage.AIEventStats `json:"stats"`
	}
	parseJSON(t, rr, &global)
	require.Empty(t, global.CameraID, "global stats must have empty camera_id")

	// Per-camera stats.
	rr = doRequest(t, routes, "GET", "/api/ai/stats?camera_id=cam-a", nil, "admin", "p")
	require.Equal(t, http.StatusOK, rr.Code)
	var perCam struct {
		CameraID string                 `json:"camera_id"`
		Stats    []storage.AIEventStats `json:"stats"`
	}
	parseJSON(t, rr, &perCam)
	require.Equal(t, "cam-a", perCam.CameraID)
}

func TestAI_Stats_Periods(t *testing.T) {
	t.Parallel()
	_, routes := aiTestHandler(t, "admin", "p", "vision")
	for _, period := range []string{"", "1h", "24h", "7d", "30d", "unknown"} {
		rr := doRequest(t, routes, "GET", "/api/ai/stats?period="+period, nil, "admin", "p")
		require.Equal(t, http.StatusOK, rr.Code, "period=%q", period)
	}
}

func TestGetDefaultStatsSince(t *testing.T) {
	t.Parallel()
	cases := []struct {
		period string
		minAge int // hours back, minimum
	}{
		{"1h", 1}, {"24h", 24}, {"", 24}, {"7d", 168}, {"30d", 720}, {"unknown", 24},
	}
	for _, tc := range cases {
		since := getDefaultStatsSince(tc.period)
		require.Less(t, time.Hour*time.Duration(tc.minAge), time.Since(since)+time.Second,
			"period=%q should reach back at least %dh", tc.period, tc.minAge)
	}
}

// itoa is a stdlib-free int64→string to avoid an extra import line.
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
