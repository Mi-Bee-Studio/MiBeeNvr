package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestDownloadMediaCacheHeaders: media responses carry a strong ETag plus
// Cache-Control: no-cache, and If-None-Match is answered with 304. Rationale:
// repair tooling rewrites recording files in place; without forced
// revalidation browsers kept serving the pre-repair bytes from their media
// cache and playback looked "still broken" after a fix (2026-09-25).
func TestDownloadMediaCacheHeaders(t *testing.T) {
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	rec := makeRecording("rec-etag", "cam-1", "h264", now, false)
	rec.FilePath = filepath.Join(store.RootDir(), "rec-etag.mp4")
	testData := []byte("fake-mp4-data-v1")
	if err := os.WriteFile(rec.FilePath, testData, 0o644); err != nil {
		t.Fatalf("create test file: %v", err)
	}
	seedRecording(t, db, rec)

	routes := h.Routes()

	// 1. First GET: 200 + cache headers.
	rr := doRequest(t, routes, "GET", "/api/recordings/rec-etag/download", nil, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	etag := rr.Header().Get("ETag")
	if etag == "" {
		t.Fatal("expected ETag on media response")
	}
	if cc := rr.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Fatalf("Cache-Control = %q, want no-cache", cc)
	}

	// 2. Revalidation with the current ETag: 304, no body.
	req := httptest.NewRequest(http.MethodGet, "/api/recordings/rec-etag/download", nil)
	req.Header.Set("If-None-Match", etag)
	w := httptest.NewRecorder()
	routes.ServeHTTP(w, req)
	if w.Code != http.StatusNotModified {
		t.Fatalf("If-None-Match with current ETag: got %d, want 304", w.Code)
	}
	if w.Body.Len() != 0 {
		t.Fatalf("304 must not carry a body, got %d bytes", w.Body.Len())
	}

	// 3. "Repair" rewrites the file in place → ETag changes → the OLD tag
	// must yield a fresh 200 (stale cache correctly invalidated).
	if err := os.WriteFile(rec.FilePath, []byte("fake-mp4-data-v2-repaired"), 0o644); err != nil {
		t.Fatalf("rewrite test file: %v", err)
	}
	future := now.Add(time.Minute)
	if err := os.Chtimes(rec.FilePath, future, future); err != nil {
		t.Fatalf("mtime bump: %v", err)
	}
	req2 := httptest.NewRequest(http.MethodGet, "/api/recordings/rec-etag/download", nil)
	req2.Header.Set("If-None-Match", etag)
	w2 := httptest.NewRecorder()
	routes.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("If-None-Match with stale ETag after rewrite: got %d, want 200", w2.Code)
	}
	body, _ := io.ReadAll(w2.Body)
	if string(body) != "fake-mp4-data-v2-repaired" {
		t.Fatalf("expected repaired bytes, got %q", string(body))
	}
	if w2.Header().Get("ETag") == etag {
		t.Fatal("ETag must change after in-place rewrite")
	}
}
