package api

// Tests for the FFmpeg-gated decoded latest-frame path (#657): H.264/H.265
// cameras (and cameras with only a device snapshot URL) get frames through
// the snapshot.Capturer with the shared 10s TTL cache.

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeSnapCapturer is the injectable snapshot capturer stub.
type fakeSnapCapturer struct {
	mu    sync.Mutex
	calls int
	jpeg  []byte
	err   error
}

func (f *fakeSnapCapturer) Capture(string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.jpeg, f.err
}

func (f *fakeSnapCapturer) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func TestLatestFrame_DecodedPath_Served(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)
	jpeg := []byte{0xff, 0xd8, 0x00, 0x01, 0xff, 0xd9}
	h.SetSnapshotCapturer(&fakeSnapCapturer{jpeg: jpeg})

	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/cam-h264/latest-frame", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "image/jpeg", rr.Header().Get("Content-Type"))
	assert.Equal(t, jpeg, rr.Body.Bytes())
}

func TestLatestFrame_DecodedPath_CachedWithinTTL(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)
	capt := &fakeSnapCapturer{jpeg: []byte{0xff, 0xd8}}
	h.SetSnapshotCapturer(capt)

	rr1 := doRequest(t, h.Routes(), "GET", "/api/cameras/cam-h264/latest-frame", nil, "", "")
	require.Equal(t, http.StatusOK, rr1.Code)
	rr2 := doRequest(t, h.Routes(), "GET", "/api/cameras/cam-h264/latest-frame", nil, "", "")
	require.Equal(t, http.StatusOK, rr2.Code)

	assert.Equal(t, 1, capt.callCount(), "second poll within the TTL must be served from cache")
	assert.Equal(t, rr1.Header().Get("ETag"), rr2.Header().Get("ETag"), "cached frame keeps a stable ETag")
}

func TestLatestFrame_DecodedPath_ETagConditional(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)
	h.SetSnapshotCapturer(&fakeSnapCapturer{jpeg: []byte{0xff, 0xd8}})

	rr1 := doRequest(t, h.Routes(), "GET", "/api/cameras/cam-h264/latest-frame", nil, "", "")
	require.Equal(t, http.StatusOK, rr1.Code)
	etag := rr1.Header().Get("ETag")
	require.NotEmpty(t, etag)

	req := httptest.NewRequest(http.MethodGet, "/api/cameras/cam-h264/latest-frame", nil)
	req.Header.Set("If-None-Match", etag)
	rr2 := httptest.NewRecorder()
	h.Routes().ServeHTTP(rr2, req)
	require.Equal(t, http.StatusNotModified, rr2.Code)
	assert.Empty(t, rr2.Body.Len())
}

func TestLatestFrame_DecodedPath_StaleCacheRefreshes(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)
	fresh := []byte{0xff, 0xd9}
	capt := &fakeSnapCapturer{jpeg: fresh}
	h.SetSnapshotCapturer(capt)

	// Seed an expired cache entry: the next request must re-capture.
	h.snapshotMu.Lock()
	h.snapshots["cam-h264"] = &snapshotCache{data: []byte("stale"), timestamp: time.Now().Add(-latestFrameCacheTTL - time.Second)}
	h.snapshotMu.Unlock()

	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/cam-h264/latest-frame", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, fresh, rr.Body.Bytes())
	assert.Equal(t, 1, capt.callCount())
}

func TestLatestFrame_CaptureError_Still404(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)
	h.SetSnapshotCapturer(&fakeSnapCapturer{err: errFakeCapture})

	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/cam-h264/latest-frame", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code, "capture failure must degrade to 404, never 500")
}

func TestLatestFrame_NoCapturer_Unchanged404(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store) // no capturer wired — pre-existing behavior

	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/cam-h264/latest-frame", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
}

// errFakeCapture distinguishes a stub error from snapshot.ErrNoFrame.
var errFakeCapture = &fakeCaptureError{}

type fakeCaptureError struct{}

func (*fakeCaptureError) Error() string { return "fake capture failure" }
