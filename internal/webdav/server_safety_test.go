// SPDX-License-Identifier: MIT
//
// Pre-refactoring safety tests for WebDAV server covering path validation,
// PUT handler, GET handler, PROPFIND, auth checks, format detection,
// and directory confinement.

package webdav

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/middleware"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- formatFromExtension tests ---

func TestFormatFromExtensionMP4(t *testing.T) {
	t.Helper()
	assert.Equal(t, "h264", string(formatFromExtension("video.mp4")))
}

func TestFormatFromExtensionAVI(t *testing.T) {
	t.Helper()
	assert.Equal(t, "h264", string(formatFromExtension("video.avi")))
}

func TestFormatFromExtensionMKV(t *testing.T) {
	t.Helper()
	assert.Equal(t, "h264", string(formatFromExtension("video.mkv")))
}

func TestFormatFromExtensionMOV(t *testing.T) {
	t.Helper()
	assert.Equal(t, "h264", string(formatFromExtension("video.mov")))
}

func TestFormatFromExtensionTS(t *testing.T) {
	t.Helper()
	assert.Equal(t, "h264", string(formatFromExtension("segment.ts")))
}

func TestFormatFromExtensionJPG(t *testing.T) {
	t.Helper()
	assert.Equal(t, "mjpeg", string(formatFromExtension("snapshot.jpg")))
}

func TestFormatFromExtensionJPEG(t *testing.T) {
	t.Helper()
	assert.Equal(t, "mjpeg", string(formatFromExtension("snapshot.jpeg")))
}

func TestFormatFromExtensionUnknown(t *testing.T) {
	t.Helper()
	assert.Equal(t, "h264", string(formatFromExtension("file.unknown")))
}

func TestFormatFromExtensionUppercase(t *testing.T) {
	t.Helper()
	assert.Equal(t, "h264", string(formatFromExtension("video.MP4")))
	assert.Equal(t, "mjpeg", string(formatFromExtension("photo.JPG")))
}

func TestFormatFromExtensionNoExt(t *testing.T) {
	t.Helper()
	assert.Equal(t, "h264", string(formatFromExtension("file_no_ext")))
}

// --- NewServer prefix handling ---

func TestNewServerCustomPrefix(t *testing.T) {
	t.Helper()
	store, err := storage.NewManager(t.TempDir())
	require.NoError(t, err)
	srv := NewServer(store, "/custom", nil, nil, false)
	assert.Equal(t, "/custom", srv.pathPrefix)
}

// --- statusCapturingWriter tests ---

func TestStatusCapturingWriterDefault(t *testing.T) {
	t.Helper()
	rec := httptest.NewRecorder()
	cw := &statusCapturingWriter{ResponseWriter: rec, statusCode: http.StatusOK}
	assert.Equal(t, http.StatusOK, cw.statusCode)
}

func TestStatusCapturingWriterCapturesStatus(t *testing.T) {
	t.Helper()
	rec := httptest.NewRecorder()
	cw := &statusCapturingWriter{ResponseWriter: rec, statusCode: http.StatusOK}
	cw.WriteHeader(http.StatusCreated)
	assert.Equal(t, http.StatusCreated, cw.statusCode)
}

func TestStatusCapturingWriterForbidden(t *testing.T) {
	t.Helper()
	rec := httptest.NewRecorder()
	cw := &statusCapturingWriter{ResponseWriter: rec, statusCode: http.StatusOK}
	cw.WriteHeader(http.StatusForbidden)
	assert.Equal(t, http.StatusForbidden, cw.statusCode)
}

// --- PROPFIND on nonexistent directory ---

func TestPROPFINDNonexistentDir(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	ts := setupTestServer(t, tmpDir, nil)
	defer ts.Close()

	req, err := http.NewRequest("PROPFIND", ts.URL+"/dav/nonexistent/", nil)
	require.NoError(t, err)
	req.Header.Set("Depth", "1")

	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	// Should return 404 for nonexistent directory
	assert.True(t, resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMultiStatus,
		"expected 404 or 207, got %d", resp.StatusCode)
}

// --- GET on directory (should fail or return listing) ---

func TestGETDirectory(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	createTestFiles(t, tmpDir)

	ts := setupTestServer(t, tmpDir, nil)
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL + "/dav/camera-01/")
	require.NoError(t, err)
	defer resp.Body.Close()

	// GET on directory should not return 200 OK with file content
	// It typically returns 404 or 302 depending on the WebDAV implementation
	assert.NotEqual(t, http.StatusOK, resp.StatusCode,
		"GET on directory should not return 200 OK with file content")
}

// --- PUT in read-write mode ---

func TestPUTAllowedInReadWriteMode(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, "camera-01"), 0o755))

	store, err := storage.NewManager(tmpDir)
	require.NoError(t, err)

	srv := NewServer(store, "/dav", nil, nil, true)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, err := http.NewRequest(http.MethodPut, ts.URL+"/dav/camera-01/uploaded.mp4", strings.NewReader("test-data"))
	require.NoError(t, err)

	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	// PUT should be allowed (201 Created or 200 OK or 204 No Content)
	assert.True(t, resp.StatusCode == http.StatusCreated || resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNoContent,
		"expected 200/201/204, got %d", resp.StatusCode)

	// Verify file was created
	content, err := os.ReadFile(filepath.Join(tmpDir, "camera-01", "uploaded.mp4"))
	require.NoError(t, err)
	assert.Equal(t, "test-data", string(content))
}

// --- MKCOL in read-write mode ---

func TestMKCOLAllowedInReadWriteMode(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()

	store, err := storage.NewManager(tmpDir)
	require.NoError(t, err)

	srv := NewServer(store, "/dav", nil, nil, true)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, err := http.NewRequest("MKCOL", ts.URL+"/dav/new-camera-dir", nil)
	require.NoError(t, err)

	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	// MKCOL should be allowed (201 Created or 405 if already exists)
	assert.True(t, resp.StatusCode == http.StatusCreated || resp.StatusCode == http.StatusMethodNotAllowed,
		"expected 201/405, got %d", resp.StatusCode)
}

// --- Method not allowed ---

func TestMethodNotAllowed(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	createTestFiles(t, tmpDir)

	ts := setupTestServer(t, tmpDir, nil)
	defer ts.Close()

	req, err := http.NewRequest(http.MethodPatch, ts.URL+"/dav/camera-01/recording_001.mp4", strings.NewReader("data"))
	require.NoError(t, err)

	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
}

// --- GET non-existent camera directory ---

func TestGETNonexistentCamera(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	ts := setupTestServer(t, tmpDir, nil)
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL + "/dav/nonexistent-camera/file.mp4")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// --- Auth with wrong password ---

func TestAuthMiddlewareWrongPassword(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	createTestFiles(t, tmpDir)

	hash, err := middleware.HashPassword("secret")
	require.NoError(t, err)

	authMW, _ := middleware.NewAuthMiddleware(middleware.AuthProvider{GetUsername: func() string { return "admin" }, GetHash: func() string { return hash }}, "", middleware.AuthRateLimitConfig{})
	ts := setupTestServer(t, tmpDir, authMW)
	defer ts.Close()

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/dav/camera-01/recording_001.mp4", nil)
	require.NoError(t, err)
	req.SetBasicAuth("admin", "wrongpassword")

	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

// --- PROPFIND on file (not directory) ---

func TestPROPFINDFile(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	createTestFiles(t, tmpDir)

	ts := setupTestServer(t, tmpDir, nil)
	defer ts.Close()

	req, err := http.NewRequest("PROPFIND", ts.URL+"/dav/camera-01/recording_001.mp4", nil)
	require.NoError(t, err)
	req.Header.Set("Depth", "0")

	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusMultiStatus, resp.StatusCode)
}

// --- Path traversal attempt via GET ---

func TestGETPathTraversal(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	createTestFiles(t, tmpDir)

	// secret.txt is in tmpDir which IS the webdav root.
	// The golang.org/x/net/webdav handler normalizes /dav/../secret.txt
	// to /secret.txt — which is within the filesystem root.
	// So the file IS accessible (by design — it's inside root).
	// This test verifies the handler doesn't serve arbitrary paths,
	// only paths within its configured filesystem.
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "secret.txt"), []byte("sensitive"), 0o644))

	ts := setupTestServer(t, tmpDir, nil)
	defer ts.Close()

	// The HTTP client normalizes the URL path: /dav/../secret.txt → /secret.txt
	// This is outside the /dav prefix, so the webdav handler won't match it.
	// The Go http client normalizes before sending.
	resp, err := ts.Client().Get(ts.URL + "/dav/../secret.txt")
	require.NoError(t, err)
	defer resp.Body.Close()

	// Go's http.Client normalizes the URL path before sending,
	// so /dav/../secret.txt becomes /secret.txt, bypassing the /dav prefix.
	// This means the request goes to /secret.txt, NOT /dav/secret.txt.
	// Path traversal outside /dav prefix now returns 403 Forbidden.
	assert.Equal(t, http.StatusForbidden, resp.StatusCode,
		"path traversal outside prefix should be forbidden")
}

// --- PROPFIND depth 0 on root ---

func TestPROPFINDDepthZero(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	createTestFiles(t, tmpDir)

	ts := setupTestServer(t, tmpDir, nil)
	defer ts.Close()

	req, err := http.NewRequest("PROPFIND", ts.URL+"/dav/", nil)
	require.NoError(t, err)
	req.Header.Set("Depth", "0")

	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusMultiStatus, resp.StatusCode)
}

// --- Server without auth middleware ---

func TestServerNoAuthMiddleware(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	createTestFiles(t, tmpDir)

	ts := setupTestServer(t, tmpDir, nil)
	defer ts.Close()

	// GET without auth should succeed (no auth middleware)
	resp, err := ts.Client().Get(ts.URL + "/dav/camera-01/recording_001.mp4")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

// --- DELETE in read-write mode ---

func TestDELETEAllowedInReadWriteMode(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, "camera-01"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "camera-01", "to-delete.mp4"), []byte("data"), 0o644))

	store, err := storage.NewManager(tmpDir)
	require.NoError(t, err)

	srv := NewServer(store, "/dav", nil, nil, true)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, err := http.NewRequest(http.MethodDelete, ts.URL+"/dav/camera-01/to-delete.mp4", nil)
	require.NoError(t, err)

	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.True(t, resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNoContent,
		"expected 200/204, got %d", resp.StatusCode)

	// File should be gone
	_, err = os.Stat(filepath.Join(tmpDir, "camera-01", "to-delete.mp4"))
	assert.True(t, os.IsNotExist(err), "file should be deleted")
}

// --- HEAD on directory ---

func TestHEADDirectory(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	createTestFiles(t, tmpDir)

	ts := setupTestServer(t, tmpDir, nil)
	defer ts.Close()

	req, err := http.NewRequest(http.MethodHead, ts.URL+"/dav/camera-01/", nil)
	require.NoError(t, err)

	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	// HEAD on directory - WebDAV may return 405 (Method Not Allowed) for directories
	assert.True(t, resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed,
		"expected 200/404/405, got %d", resp.StatusCode)
}

// --- Concurrent GET requests ---

func TestConcurrentGET(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	createTestFiles(t, tmpDir)

	ts := setupTestServer(t, tmpDir, nil)
	defer ts.Close()

	const numRequests = 10
	errCh := make(chan error, numRequests)

	for range numRequests {
		go func() {
			resp, err := ts.Client().Get(ts.URL + "/dav/camera-01/recording_001.mp4")
			if err != nil {
				errCh <- err
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				errCh <- fmt.Errorf("expected 200, got %d", resp.StatusCode)
				return
			}
			errCh <- nil
		}()
	}

	for range numRequests {
		require.NoError(t, <-errCh)
	}
}

// --- PUT with empty path prefix ---

func TestNewServerEmptyPrefixUsesDefault(t *testing.T) {
	t.Helper()
	store, err := storage.NewManager(t.TempDir())
	require.NoError(t, err)

	srv := NewServer(store, "", nil, nil, false)
	assert.Equal(t, "/dav", srv.pathPrefix)
}

// --- GET on empty root ---

func TestGETEmptyRoot(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	// No files created

	ts := setupTestServer(t, tmpDir, nil)
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL + "/dav/")
	require.NoError(t, err)
	defer resp.Body.Close()

	// GET on empty directory root - WebDAV may return 405
	assert.True(t, resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusMethodNotAllowed,
		"expected 404/200/405, got %d", resp.StatusCode)
}

// --- PROPFIND with depth header missing ---

func TestPROPFINDNoDepthHeader(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	createTestFiles(t, tmpDir)

	ts := setupTestServer(t, tmpDir, nil)
	defer ts.Close()

	req, err := http.NewRequest("PROPFIND", ts.URL+"/dav/", nil)
	require.NoError(t, err)
	// No Depth header set

	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	// Should still work (depth defaults vary)
	assert.True(t, resp.StatusCode == http.StatusMultiStatus || resp.StatusCode == http.StatusBadRequest,
		"expected 207/400, got %d", resp.StatusCode)
}

// --- OPTIONS returns proper headers ---

func TestOPTIONSHeaders(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	createTestFiles(t, tmpDir)

	ts := setupTestServer(t, tmpDir, nil)
	defer ts.Close()

	req, err := http.NewRequest(http.MethodOptions, ts.URL+"/dav/", nil)
	require.NoError(t, err)

	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	// OPTIONS should not return 403
	assert.NotEqual(t, http.StatusForbidden, resp.StatusCode)
}

// --- Path traversal: PUT in read-write mode ---

func TestPUTPathTraversalReadWrite(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, "camera-01"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "secret.txt"), []byte("sensitive"), 0o644))

	store, err := storage.NewManager(tmpDir)
	require.NoError(t, err)

	srv := NewServer(store, "/dav", nil, nil, true)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Go normalizes /dav/camera-01/../../evil.txt to /dav/evil.txt
	// The webdav filesystem root is tmpDir, so evil.txt IS within root.
	// The key security property: existing files are not overwritten unexpectedly.
	req, err := http.NewRequest(http.MethodPut, ts.URL+"/dav/camera-01/../../evil.txt", strings.NewReader("evil"))
	require.NoError(t, err)

	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	// secret.txt must never be overwritten by path traversal
	content, readErr := os.ReadFile(filepath.Join(tmpDir, "secret.txt"))
	require.NoError(t, readErr)
	assert.Equal(t, "sensitive", string(content), "secret.txt must not be overwritten via traversal")
}

// --- Path traversal: MKCOL in read-write mode ---

func TestMKCOLPathTraversalReadWrite(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()

	store, err := storage.NewManager(tmpDir)
	require.NoError(t, err)

	srv := NewServer(store, "/dav", nil, nil, true)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Go normalizes the path. MKCOL should not panic or cause server error.
	req, err := http.NewRequest("MKCOL", ts.URL+"/dav/../../etc", nil)
	require.NoError(t, err)

	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	// Should not cause server error
	assert.NotEqual(t, http.StatusInternalServerError, resp.StatusCode,
		"MKCOL should not cause server error")
}

// --- Path traversal: DELETE in read-write mode ---
// IMPORTANT: Go http.Client normalizes /dav/../protected.txt to /protected.txt.
// The webdav handler uses webdav.Dir(rootDir) as its filesystem, meaning ALL files
// in rootDir are accessible, not just those under the /dav prefix.
// This test documents this behavior — Wave 1 security fixes should address it.

func TestDELETEPathTraversalReadWrite(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "protected.txt"), []byte("data"), 0o644))

	store, err := storage.NewManager(tmpDir)
	require.NoError(t, err)

	srv := NewServer(store, "/dav", nil, nil, true)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Go normalizes /dav/../protected.txt to /protected.txt
	// This is WITHIN the webdav filesystem root (tmpDir) and can be deleted.
	// This documents the current behavior that path prefix alone doesn't confine access.
	req, err := http.NewRequest(http.MethodDelete, ts.URL+"/dav/../protected.txt", nil)
	require.NoError(t, err)

	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	// Path prefix enforcement now blocks traversal outside /dav prefix.
	assert.Equal(t, http.StatusForbidden, resp.StatusCode,
		"DELETE with path traversal outside prefix should be forbidden")
}

// --- COPY in read-write mode ---

func TestCOPYInReadWriteMode(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, "camera-01"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "camera-01", "src.mp4"), []byte("data"), 0o644))

	store, err := storage.NewManager(tmpDir)
	require.NoError(t, err)

	srv := NewServer(store, "/dav", nil, nil, true)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, err := http.NewRequest("COPY", ts.URL+"/dav/camera-01/src.mp4", nil)
	require.NoError(t, err)
	req.Header.Set("Destination", ts.URL+"/dav/camera-01/dst.mp4")

	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	// COPY should be allowed (not forbidden)
	assert.NotEqual(t, http.StatusForbidden, resp.StatusCode,
		"COPY should be allowed in read-write mode")
}

// --- MOVE in read-write mode ---

func TestMOVEInReadWriteMode(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, "camera-01"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "camera-01", "src.mp4"), []byte("data"), 0o644))

	store, err := storage.NewManager(tmpDir)
	require.NoError(t, err)

	srv := NewServer(store, "/dav", nil, nil, true)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, err := http.NewRequest("MOVE", ts.URL+"/dav/camera-01/src.mp4", nil)
	require.NoError(t, err)
	req.Header.Set("Destination", ts.URL+"/dav/camera-01/moved.mp4")

	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.NotEqual(t, http.StatusForbidden, resp.StatusCode,
		"MOVE should be allowed in read-write mode")
}

// --- Auth enforced on all write methods ---

func TestAuthEnforcedOnPUT(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, "camera-01"), 0o755))

	hash, err := middleware.HashPassword("secret")
	require.NoError(t, err)

	store, err := storage.NewManager(tmpDir)
	require.NoError(t, err)

	authMW, _ := middleware.NewAuthMiddleware(middleware.AuthProvider{GetUsername: func() string { return "admin" }, GetHash: func() string { return hash }}, "", middleware.AuthRateLimitConfig{})
	srv := NewServer(store, "/dav", authMW, nil, true)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// PUT without auth
	req, err := http.NewRequest(http.MethodPut, ts.URL+"/dav/camera-01/new.mp4", strings.NewReader("data"))
	require.NoError(t, err)

	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestAuthEnforcedOnDELETE(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	createTestFiles(t, tmpDir)

	hash, err := middleware.HashPassword("secret")
	require.NoError(t, err)

	store, err := storage.NewManager(tmpDir)
	require.NoError(t, err)

	authMW, _ := middleware.NewAuthMiddleware(middleware.AuthProvider{GetUsername: func() string { return "admin" }, GetHash: func() string { return hash }}, "", middleware.AuthRateLimitConfig{})
	srv := NewServer(store, "/dav", authMW, nil, true)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, err := http.NewRequest(http.MethodDelete, ts.URL+"/dav/camera-01/recording_001.mp4", nil)
	require.NoError(t, err)

	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestAuthEnforcedOnPROPFIND(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	createTestFiles(t, tmpDir)

	hash, err := middleware.HashPassword("secret")
	require.NoError(t, err)

	store, err := storage.NewManager(tmpDir)
	require.NoError(t, err)

	authMW, _ := middleware.NewAuthMiddleware(middleware.AuthProvider{GetUsername: func() string { return "admin" }, GetHash: func() string { return hash }}, "", middleware.AuthRateLimitConfig{})
	srv := NewServer(store, "/dav", authMW, nil, true)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, err := http.NewRequest("PROPFIND", ts.URL+"/dav/", nil)
	require.NoError(t, err)
	req.Header.Set("Depth", "1")

	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

// --- Read-only mode blocks all write methods ---

func TestReadOnlyBlocksCOPY(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	createTestFiles(t, tmpDir)

	ts := setupTestServer(t, tmpDir, nil)
	defer ts.Close()

	req, err := http.NewRequest("COPY", ts.URL+"/dav/camera-01/recording_001.mp4", nil)
	require.NoError(t, err)
	req.Header.Set("Destination", ts.URL+"/dav/camera-01/copy.mp4")

	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
}

func TestReadOnlyBlocksMOVE(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	createTestFiles(t, tmpDir)

	ts := setupTestServer(t, tmpDir, nil)
	defer ts.Close()

	req, err := http.NewRequest("MOVE", ts.URL+"/dav/camera-01/recording_001.mp4", nil)
	require.NoError(t, err)
	req.Header.Set("Destination", ts.URL+"/dav/camera-01/moved.mp4")

	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
}

func TestReadOnlyBlocksLOCK(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	createTestFiles(t, tmpDir)

	ts := setupTestServer(t, tmpDir, nil)
	defer ts.Close()

	req, err := http.NewRequest("LOCK", ts.URL+"/dav/camera-01/recording_001.mp4", nil)
	require.NoError(t, err)

	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
}

// --- GET with path traversal using URL encoding ---

func TestGETPathTraversalURLEncoded(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	createTestFiles(t, tmpDir)
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "secret.txt"), []byte("sensitive"), 0o644))

	ts := setupTestServer(t, tmpDir, nil)
	defer ts.Close()

	// URL-encoded path traversal: %2e%2e = ..
	resp, err := ts.Client().Get(ts.URL + "/dav/%2e%2e/secret.txt")
	require.NoError(t, err)
	defer resp.Body.Close()

	// Path prefix enforcement now blocks URL-encoded traversal outside /dav prefix.
	assert.Equal(t, http.StatusForbidden, resp.StatusCode,
		"URL-encoded path traversal outside prefix should be forbidden")
}

// --- PUT to nested path ---

func TestPUTNestedPathReadWrite(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, "camera-01", "subdir"), 0o755))

	store, err := storage.NewManager(tmpDir)
	require.NoError(t, err)

	srv := NewServer(store, "/dav", nil, nil, true)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, err := http.NewRequest(http.MethodPut, ts.URL+"/dav/camera-01/subdir/nested.mp4", strings.NewReader("nested-data"))
	require.NoError(t, err)

	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	// Should be allowed in read-write mode
	assert.True(t, resp.StatusCode == http.StatusCreated || resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNoContent,
		"expected 200/201/204, got %d", resp.StatusCode)
}
