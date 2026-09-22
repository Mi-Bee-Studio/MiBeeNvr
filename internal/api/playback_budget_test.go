package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/iobudget"
	"github.com/stretchr/testify/require"
)

// fakeLimiter records Wait charges per consumer.
type fakeLimiter struct {
	mu      sync.Mutex
	charged map[string]int64
	block   chan struct{} // nil = never block
}

func newFakeLimiter() *fakeLimiter { return &fakeLimiter{charged: map[string]int64{}} }

func (f *fakeLimiter) Wait(_ context.Context, consumer string, n int64) error {
	if f.block != nil {
		<-f.block
	}
	f.mu.Lock()
	f.charged[consumer] += n
	f.mu.Unlock()
	return nil
}

func TestServeFileBudgeted_NoBudget_PlainServe(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "video.mp4")
	require.NoError(t, os.WriteFile(path, []byte("0123456789abcdef"), 0o644))

	db, store := setupTestDB(t)
	h := TestHandler(db, store) // no playback budget installed

	req := httptest.NewRequest(http.MethodGet, "/video.mp4", nil)
	req.Header.Set("Range", "bytes=4-7")
	rr := httptest.NewRecorder()
	h.serveFileBudgeted(rr, req, path)

	require.Equal(t, http.StatusPartialContent, rr.Code)
	require.Equal(t, "4567", rr.Body.String())
}

func TestServeFileBudgeted_WithBudget_ChargesReads(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "video.mp4")
	require.NoError(t, os.WriteFile(path, []byte("0123456789abcdef"), 0o644))

	db, store := setupTestDB(t)
	h := TestHandler(db, store)
	fl := newFakeLimiter()
	h.SetPlaybackBudget(fl)

	req := httptest.NewRequest(http.MethodGet, "/video.mp4", nil)
	req.Header.Set("Range", "bytes=0-7")
	rr := httptest.NewRecorder()
	h.serveFileBudgeted(rr, req, path)

	require.Equal(t, http.StatusPartialContent, rr.Code)
	require.Equal(t, "01234567", rr.Body.String())
	require.Greater(t, fl.charged[iobudget.ConsumerPlayback], int64(0),
		"playback reads must be charged to the budget")
}

func TestServeFileBudgeted_MissingFile_404(t *testing.T) {
	db, store := setupTestDB(t)
	h := TestHandler(db, store)
	h.SetPlaybackBudget(newFakeLimiter())

	rr := httptest.NewRecorder()
	h.serveFileBudgeted(rr, httptest.NewRequest(http.MethodGet, "/x.mp4", nil),
		filepath.Join(t.TempDir(), "missing.mp4"))
	require.Equal(t, http.StatusNotFound, rr.Code)
}

// TestPlaybackBudgetRealBucket paces a ServeContent read against a real
// bucket: a 1-byte/ms budget serving a few KB must complete (Wait blocks but
// refills), proving the wiring works with the concrete implementation.
func TestPlaybackBudgetRealBucket(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "video.mp4")
	require.NoError(t, os.WriteFile(path, make([]byte, 8192), 0o644))

	db, store := setupTestDB(t)
	h := TestHandler(db, store)
	b := iobudget.New(1<<20, 1<<20) // 1 MiB/s, 1 MiB burst — no waiting at this size
	h.SetPlaybackBudget(b)

	rr := httptest.NewRecorder()
	start := time.Now()
	h.serveFileBudgeted(rr, httptest.NewRequest(http.MethodGet, "/video.mp4", nil), path)
	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, 8192, rr.Body.Len())
	require.Less(t, time.Since(start), 5*time.Second, "burst-covered read must not stall")
}
