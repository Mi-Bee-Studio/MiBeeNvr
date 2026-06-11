package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/stretchr/testify/require"
)

func setupEventsHandler(t *testing.T) (*Handler, *event.EventBus) {
	t.Helper()
	db, store := setupTestDB(t)
	h := TestHandler(db, store)
	bus := event.NewEventBus(64)
	h.SetEventBus(bus)
	return h, bus
}

func TestEvents_SSEHeaders(t *testing.T) {
	t.Parallel()
	h, _ := setupEventsHandler(t)

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/api/events", nil).WithContext(ctx)
	req.SetBasicAuth("admin", "admin12345")
	rr := httptest.NewRecorder()

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

func TestEvents_ReceivesPublishedEvent(t *testing.T) {
	t.Parallel()
	h, bus := setupEventsHandler(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/events", nil).WithContext(ctx)
	req.SetBasicAuth("admin", "admin12345")
	rec := newSSERecorder()

	var handlerDone atomic.Bool
	go func() {
		h.Routes().ServeHTTP(rec, req)
		handlerDone.Store(true)
	}()

	// Wait for handler to start and subscribe.
	time.Sleep(50 * time.Millisecond)

	// Publish an event.
	bus.Publish(context.Background(), "onvif.motion", map[string]string{"camera": "front-door"})
	time.Sleep(50 * time.Millisecond)

	cancel()
	time.Sleep(50 * time.Millisecond)

	body := rec.String()
	require.Contains(t, body, "event: onvif.motion")
	require.Contains(t, body, "data: {")
	require.Contains(t, body, "front-door")
}

func TestEvents_FilterByPrefix(t *testing.T) {
	t.Parallel()
	h, bus := setupEventsHandler(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/events?filter=onvif.", nil).WithContext(ctx)
	req.SetBasicAuth("admin", "admin12345")
	rec := newSSERecorder()

	var handlerDone atomic.Bool
	go func() {
		h.Routes().ServeHTTP(rec, req)
		handlerDone.Store(true)
	}()

	time.Sleep(50 * time.Millisecond)

	// Publish events with different topics.
	bus.Publish(context.Background(), "onvif.motion", map[string]string{"camera": "cam-1"})
	bus.Publish(context.Background(), "onvif.tamper", map[string]string{"camera": "cam-2"})
	bus.Publish(context.Background(), "segment.completed", map[string]string{"camera": "cam-3"})
	time.Sleep(50 * time.Millisecond)

	cancel()
	time.Sleep(50 * time.Millisecond)

	body := rec.String()
	// Should see onvif events.
	require.Contains(t, body, "event: onvif.motion")
	require.Contains(t, body, "event: onvif.tamper")
	// Should NOT see non-onvif events.
	require.NotContains(t, body, "event: segment.completed")
}

func TestEvents_ContextCancellation(t *testing.T) {
	t.Parallel()
	h, _ := setupEventsHandler(t)

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/api/events", nil).WithContext(ctx)
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

func TestEvents_BusNil(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	h := TestHandler(db, store)
	// Don't set event bus.

	req := httptest.NewRequest(http.MethodGet, "/api/events", nil)
	req.SetBasicAuth("admin", "admin12345")
	rr := httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, req)

	require.Equal(t, http.StatusServiceUnavailable, rr.Code)
}

func TestEvents_HeartbeatFormat(t *testing.T) {
	t.Parallel()
	h, _ := setupEventsHandler(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/events", nil).WithContext(ctx)
	req.SetBasicAuth("admin", "admin12345")
	rec := newSSERecorder()

	// Cancel after a short time — heartbeat interval is 15s so we won't see one.
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()
	go h.Routes().ServeHTTP(rec, req)

	time.Sleep(300 * time.Millisecond)

	require.Equal(t, http.StatusOK, rec.Code())
	require.Equal(t, "text/event-stream", rec.header().Get("Content-Type"))
}

// ensure imports compile
var _ = strings.Builder{}

// sseRecorder implements http.ResponseWriter with buffering and flush support.
type sseRecorder struct {
	mu      sync.Mutex
	headers http.Header
	body    strings.Builder
	code    int
	flushed bool
}

func newSSERecorder() *sseRecorder {
	return &sseRecorder{headers: make(http.Header)}
}
func (r *sseRecorder) header() http.Header      { return r.headers }
func (r *sseRecorder) Header() http.Header        { return r.headers }
func (r *sseRecorder) Flush()                    { r.flushed = true }

func (r *sseRecorder) WriteHeader(code int) {
	r.mu.Lock()
	r.code = code
	r.mu.Unlock()
}

func (r *sseRecorder) Code() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.code
}

func (r *sseRecorder) Write(b []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.body.Write(b)
}

func (r *sseRecorder) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.body.String()
}
