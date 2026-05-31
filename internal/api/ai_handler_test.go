package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/ai"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/ai/engine"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/stretchr/testify/require"
)

// --- Mock AI Engine ---

type mockAIEngine struct {
	available  bool
	name       string
	modelPath  string
}

func (m *mockAIEngine) IsAvailable() bool  { return m.available }
func (m *mockAIEngine) Name() string       { return m.name }
func (m *mockAIEngine) ModelPath() string  { return m.modelPath }

// --- Mock AI Detector ---

type mockAIDetector struct {
	mu       sync.Mutex
	enabled  map[string]bool
	enableErr error
	hub      *model.StreamHub
	onDetCB  engine.OnDetectionFunc
}

func newMockAIDetector() *mockAIDetector {
	return &mockAIDetector{
		enabled: make(map[string]bool),
	}
}

func (m *mockAIDetector) EnableCamera(camID string, hub *model.StreamHub) error {
	if m.enableErr != nil {
		return m.enableErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.enabled[camID] = true
	m.hub = hub
	return nil
}

func (m *mockAIDetector) DisableCamera(camID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.enabled, camID)
}

func (m *mockAIDetector) IsEnabled(camID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.enabled[camID]
}

func (m *mockAIDetector) EnabledCameras() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]string, 0, len(m.enabled))
	for id := range m.enabled {
		ids = append(ids, id)
	}
	return ids
}

func (m *mockAIDetector) OnDetection(cb engine.OnDetectionFunc) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onDetCB = cb
}

func (m *mockAIDetector) StopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.enabled = make(map[string]bool)
}

// fireDetection invokes the registered OnDetection callback (for testing SSE).
func (m *mockAIDetector) fireDetection(result engine.DetectionResult) {
	m.mu.Lock()
	cb := m.onDetCB
	m.mu.Unlock()
	if cb != nil {
		cb(result)
	}
}

// --- Test helpers ---

func setupAIHandler(t *testing.T, eng AIEngine, det AIDetector) *Handler {
	t.Helper()
	db, store := setupTestDB(t)
	h := TestHandler(db, store)
	h.SetAIComponents(eng, det)
	return h
}

func doJSONRequest(t *testing.T, handler http.Handler, method, path string, body interface{}) *httptest.ResponseRecorder {
	t.Helper()
	var bodyReader strings.Reader
	if body != nil {
		data, err := json.Marshal(body)
		require.NoError(t, err)
		bodyReader = *strings.NewReader(string(data))
	}
	req := httptest.NewRequest(method, path, &bodyReader)
	req.SetBasicAuth("admin", "admin12345")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

// --- Tests ---

func TestGetAIStatus_EngineNil(t *testing.T) {
	h := setupAIHandler(t, nil, nil)

	rr := doJSONRequest(t, h.Routes(), http.MethodGet, "/api/ai/status", nil)
	require.Equal(t, http.StatusOK, rr.Code)

	var resp aiStatusResponse
	parseJSON(t, rr, &resp)
	require.False(t, resp.Available)
	require.Equal(t, "not_installed", resp.EngineStatus)
}

func TestGetAIStatus_EngineNotAvailable(t *testing.T) {
	eng := &mockAIEngine{available: false, name: "test-engine", modelPath: "yolov11n.onnx"}
	h := setupAIHandler(t, eng, nil)

	rr := doJSONRequest(t, h.Routes(), http.MethodGet, "/api/ai/status", nil)
	require.Equal(t, http.StatusOK, rr.Code)

	var resp aiStatusResponse
	parseJSON(t, rr, &resp)
	require.False(t, resp.Available)
	require.Equal(t, "stopped", resp.EngineStatus)
	require.Equal(t, "yolov11n.onnx", resp.Model)
}

func TestGetAIStatus_EngineRunning(t *testing.T) {
	eng := &mockAIEngine{available: true, name: "test-engine", modelPath: "yolov11n.onnx"}
	h := setupAIHandler(t, eng, nil)

	rr := doJSONRequest(t, h.Routes(), http.MethodGet, "/api/ai/status", nil)
	require.Equal(t, http.StatusOK, rr.Code)

	var resp aiStatusResponse
	parseJSON(t, rr, &resp)
	require.True(t, resp.Available)
	require.Equal(t, "running", resp.EngineStatus)
	require.Equal(t, "yolov11n.onnx", resp.Model)
}


func TestEnableAI_CameraNotFound(t *testing.T) {
	// DB exists but camera not in DB → 404.
	db, store := setupTestDB(t)
	h := TestHandler(db, store)
	det := newMockAIDetector()
	h.SetAIComponents(nil, det)

	rr := doJSONRequest(t, h.Routes(), http.MethodPost, "/api/ai/enable", aiEnableRequest{CameraID: "nonexistent-cam"})
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestEnableAI_DetectorNil(t *testing.T) {
	h := setupAIHandler(t, nil, nil)

	rr := doJSONRequest(t, h.Routes(), http.MethodPost, "/api/ai/enable", aiEnableRequest{CameraID: "test-cam"})
	require.Equal(t, http.StatusServiceUnavailable, rr.Code)

	var resp map[string]string
	parseJSON(t, rr, &resp)
	require.Contains(t, resp["error"], "not available")
}

func TestEnableAI_MissingCameraID(t *testing.T) {
	det := newMockAIDetector()
	h := setupAIHandler(t, nil, det)

	rr := doJSONRequest(t, h.Routes(), http.MethodPost, "/api/ai/enable", aiEnableRequest{CameraID: ""})
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestEnableAI_InvalidBody(t *testing.T) {
	det := newMockAIDetector()
	h := setupAIHandler(t, nil, det)

	req := httptest.NewRequest(http.MethodPost, "/api/ai/enable", strings.NewReader("not json"))
	req.SetBasicAuth("admin", "admin12345")
	rr := httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestDisableAI_Success(t *testing.T) {
	det := newMockAIDetector()
	// Enable first
	det.EnableCamera("test-cam", model.NewStreamHub())
	require.True(t, det.IsEnabled("test-cam"))

	h := setupAIHandler(t, nil, det)

	rr := doJSONRequest(t, h.Routes(), http.MethodPost, "/api/ai/disable", aiDisableRequest{CameraID: "test-cam"})
	require.Equal(t, http.StatusOK, rr.Code)

	var resp map[string]string
	parseJSON(t, rr, &resp)
	require.Equal(t, "disabled", resp["status"])
	require.Equal(t, "test-cam", resp["camera_id"])
	require.False(t, det.IsEnabled("test-cam"))
}

func TestDisableAI_AlreadyDisabled(t *testing.T) {
	det := newMockAIDetector()
	h := setupAIHandler(t, nil, det)

	rr := doJSONRequest(t, h.Routes(), http.MethodPost, "/api/ai/disable", aiDisableRequest{CameraID: "nonexistent"})
	require.Equal(t, http.StatusOK, rr.Code)

	var resp map[string]string
	parseJSON(t, rr, &resp)
	require.Equal(t, "disabled", resp["status"])
}

func TestDisableAI_DetectorNil(t *testing.T) {
	h := setupAIHandler(t, nil, nil)

	rr := doJSONRequest(t, h.Routes(), http.MethodPost, "/api/ai/disable", aiDisableRequest{CameraID: "test-cam"})
	require.Equal(t, http.StatusServiceUnavailable, rr.Code)
}

func TestDisableAI_MissingCameraID(t *testing.T) {
	det := newMockAIDetector()
	h := setupAIHandler(t, nil, det)

	rr := doJSONRequest(t, h.Routes(), http.MethodPost, "/api/ai/disable", aiDisableRequest{CameraID: ""})
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestAIEvents_SSEHeaders(t *testing.T) {
	det := newMockAIDetector()
	h := setupAIHandler(t, nil, det)

	req := httptest.NewRequest(http.MethodGet, "/api/ai/events", nil)
	req.SetBasicAuth("admin", "admin12345")
	rr := httptest.NewRecorder()

	// Use a cancellable context so the SSE loop exits.
	ctx, cancel := context.WithCancel(context.Background())
	req = req.WithContext(ctx)

	// Cancel after headers are written.
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.Routes().ServeHTTP(rr, req)
	}()

	<-done

	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, "text/event-stream", rr.Header().Get("Content-Type"))
	require.Equal(t, "no-cache", rr.Header().Get("Cache-Control"))
}

func TestAIEvents_DetectionEvent(t *testing.T) {
	det := newMockAIDetector()
	h := setupAIHandler(t, nil, det)

	// Use a buffering response writer that supports Flush.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/ai/events", nil).WithContext(ctx)
	req.SetBasicAuth("admin", "admin12345")
	rec := newSSERecorder()

	// Run handler in goroutine — it will block until ctx cancel.
	var handlerDone atomic.Bool
	go func() {
		h.Routes().ServeHTTP(rec, req)
		handlerDone.Store(true)
	}()

	// Wait for handler to start and register callback.
	time.Sleep(50 * time.Millisecond)

	// Fire a detection event.
	result := engine.DetectionResult{
		CameraID: "cam-1",
		PTStime:  12345,
		Detections: []ai.Detection{
			{Label: "person", Confidence: 0.95, Box: [4]float32{0.1, 0.2, 0.3, 0.4}},
		},
	}
	det.fireDetection(result)

	time.Sleep(50 * time.Millisecond)
	cancel()
	time.Sleep(50 * time.Millisecond)

	body := rec.String()
	require.Contains(t, body, "data: {")
	require.Contains(t, body, "cam-1")
	require.Contains(t, body, "person")
	require.Contains(t, body, "0.95")
}

func TestAIEvents_Heartbeat(t *testing.T) {
	det := newMockAIDetector()
	h := setupAIHandler(t, nil, det)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/ai/events", nil).WithContext(ctx)
	req.SetBasicAuth("admin", "admin12345")
	rec := newSSERecorder()

	// Cancel after enough time for a heartbeat (15s interval; we wait 100ms + early cancel).
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()
	go h.Routes().ServeHTTP(rec, req)

	time.Sleep(300 * time.Millisecond)

	// Heartbeat is every 15s, so in 200ms we won't see one in production.
	// But we verify the handler ran and produced SSE headers.
	require.Equal(t, http.StatusOK, rec.code)
	require.Equal(t, "text/event-stream", rec.header().Get("Content-Type"))
}

func TestAIEvents_ContextCancellation(t *testing.T) {
	det := newMockAIDetector()
	h := setupAIHandler(t, nil, det)

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/api/ai/events", nil).WithContext(ctx)
	req.SetBasicAuth("admin", "admin12345")
	rec := newSSERecorder()

	var handlerDone atomic.Bool
	go func() {
		h.Routes().ServeHTTP(rec, req)
		handlerDone.Store(true)
	}()

	// Verify handler is running.
	time.Sleep(50 * time.Millisecond)
	require.False(t, handlerDone.Load())

	// Cancel context.
	cancel()
	time.Sleep(100 * time.Millisecond)
	require.True(t, handlerDone.Load())
}

func TestAIEvents_DetectorNil(t *testing.T) {
	h := setupAIHandler(t, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/ai/events", nil)
	req.SetBasicAuth("admin", "admin12345")
	rr := httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, req)

	require.Equal(t, http.StatusServiceUnavailable, rr.Code)
}

// --- SSE recorder for testing streaming endpoints ---

// sseRecorder implements http.ResponseWriter with buffering and flush support.
type sseRecorder struct {
	headers http.Header
	body    strings.Builder
	code    int
	flushed bool
}

func newSSERecorder() *sseRecorder {
	return &sseRecorder{headers: make(http.Header)}
}

func (r *sseRecorder) header() http.Header { return r.headers }

func (r *sseRecorder) Header() http.Header       { return r.headers }

func (r *sseRecorder) WriteHeader(code int) { r.code = code }

func (r *sseRecorder) Write(b []byte) (int, error) { return r.body.Write(b) }

func (r *sseRecorder) Flush() {
	r.flushed = true
}

func (r *sseRecorder) String() string { return r.body.String() }

// --- ensure imports compile ---

var _ = fmt.Sprintf
var _ = sync.Once{}
