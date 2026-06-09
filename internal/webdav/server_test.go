package webdav

import (
	"io"
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

func setupTestServer(t *testing.T, rootDir string, authMW func(http.Handler) http.Handler) *httptest.Server {
	t.Helper()
	store, err := storage.NewManager(rootDir)
	require.NoError(t, err)

	srv := NewServer(store, "/dav", authMW, nil, false)
	return httptest.NewServer(srv.Handler())
}

func createTestFiles(t *testing.T, rootDir string) {
	t.Helper()
	// camera-01 dir with recording files
	cam1 := filepath.Join(rootDir, "camera-01")
	require.NoError(t, os.MkdirAll(cam1, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(cam1, "recording_001.mp4"), []byte("fake-mp4-data"), 0644))

	// camera-02 dir with recording files
	cam2 := filepath.Join(rootDir, "camera-02")
	require.NoError(t, os.MkdirAll(cam2, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(cam2, "recording_002.mp4"), []byte("fake-mp4-data-2"), 0644))
}

func TestPROPFINDRoot(t *testing.T) {
	tmpDir := t.TempDir()
	createTestFiles(t, tmpDir)

	ts := setupTestServer(t, tmpDir, nil)
	defer ts.Close()

	req, err := http.NewRequest("PROPFIND", ts.URL+"/dav/", nil)
	require.NoError(t, err)
	req.Header.Set("Depth", "1")

	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusMultiStatus, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	bodyStr := string(body)
	assert.Contains(t, bodyStr, "camera-01")
	assert.Contains(t, bodyStr, "camera-02")
}

func TestPROPFINDSubdirectory(t *testing.T) {
	tmpDir := t.TempDir()
	createTestFiles(t, tmpDir)

	ts := setupTestServer(t, tmpDir, nil)
	defer ts.Close()

	req, err := http.NewRequest("PROPFIND", ts.URL+"/dav/camera-01/", nil)
	require.NoError(t, err)
	req.Header.Set("Depth", "1")

	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusMultiStatus, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Contains(t, string(body), "recording_001.mp4")
}

func TestGETFile(t *testing.T) {
	tmpDir := t.TempDir()
	createTestFiles(t, tmpDir)

	ts := setupTestServer(t, tmpDir, nil)
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL + "/dav/camera-01/recording_001.mp4")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, "fake-mp4-data", string(body))
}

func TestHEADFile(t *testing.T) {
	tmpDir := t.TempDir()
	createTestFiles(t, tmpDir)

	ts := setupTestServer(t, tmpDir, nil)
	defer ts.Close()

	req, err := http.NewRequest(http.MethodHead, ts.URL+"/dav/camera-01/recording_001.mp4", nil)
	require.NoError(t, err)

	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.NotEmpty(t, resp.Header.Get("Content-Length"))
}

func TestOPTIONS(t *testing.T) {
	tmpDir := t.TempDir()
	createTestFiles(t, tmpDir)

	ts := setupTestServer(t, tmpDir, nil)
	defer ts.Close()

	req, err := http.NewRequest(http.MethodOptions, ts.URL+"/dav/", nil)
	require.NoError(t, err)

	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	// OPTIONS should be handled (not 403)
	assert.NotEqual(t, http.StatusForbidden, resp.StatusCode)
}

func TestWriteMethodsForbidden(t *testing.T) {
	tmpDir := t.TempDir()
	createTestFiles(t, tmpDir)

	ts := setupTestServer(t, tmpDir, nil)
	defer ts.Close()

	methods := []string{"PUT", "DELETE", "MKCOL", "COPY", "MOVE", "LOCK", "UNLOCK"}
	for _, method := range methods {
		t.Run(method, func(t *testing.T) {
			req, err := http.NewRequest(method, ts.URL+"/dav/", nil)
			require.NoError(t, err)

			resp, err := ts.Client().Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()

			assert.Equal(t, http.StatusForbidden, resp.StatusCode)
		})
	}
}

func TestPUTForbidden(t *testing.T) {
	tmpDir := t.TempDir()
	createTestFiles(t, tmpDir)

	ts := setupTestServer(t, tmpDir, nil)
	defer ts.Close()

	req, err := http.NewRequest(http.MethodPut, ts.URL+"/dav/camera-01/newfile.mp4", strings.NewReader("data"))
	require.NoError(t, err)

	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusForbidden, resp.StatusCode)

	// Verify file was NOT created
	_, err = os.Stat(filepath.Join(tmpDir, "camera-01", "newfile.mp4"))
	assert.True(t, os.IsNotExist(err))
}

func TestDELETEForbidden(t *testing.T) {
	tmpDir := t.TempDir()
	createTestFiles(t, tmpDir)

	ts := setupTestServer(t, tmpDir, nil)
	defer ts.Close()

	req, err := http.NewRequest(http.MethodDelete, ts.URL+"/dav/camera-01/recording_001.mp4", nil)
	require.NoError(t, err)

	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusForbidden, resp.StatusCode)

	// Verify file still exists
	_, err = os.Stat(filepath.Join(tmpDir, "camera-01", "recording_001.mp4"))
	assert.NoError(t, err)
}

func TestAuthMiddlewareBlocksUnauthenticated(t *testing.T) {
	tmpDir := t.TempDir()
	createTestFiles(t, tmpDir)

	hash, err := middleware.HashPassword("secret")
	require.NoError(t, err)

	authMW, _ := middleware.NewAuthMiddleware(middleware.AuthProvider{GetUsername: func() string { return "admin" }, GetHash: func() string { return hash }}, "", middleware.AuthRateLimitConfig{})
	ts := setupTestServer(t, tmpDir, authMW)
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL + "/dav/")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestAuthMiddlewareAllowsAuthenticated(t *testing.T) {
	tmpDir := t.TempDir()
	createTestFiles(t, tmpDir)

	hash, err := middleware.HashPassword("secret")
	require.NoError(t, err)

	authMW, _ := middleware.NewAuthMiddleware(middleware.AuthProvider{GetUsername: func() string { return "admin" }, GetHash: func() string { return hash }}, "", middleware.AuthRateLimitConfig{})
	ts := setupTestServer(t, tmpDir, authMW)
	defer ts.Close()

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/dav/camera-01/recording_001.mp4", nil)
	require.NoError(t, err)
	req.SetBasicAuth("admin", "secret")

	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestAuthMiddlewareSetupModeWhenNoPassword(t *testing.T) {
	tmpDir := t.TempDir()
	createTestFiles(t, tmpDir)

	// No password hash = setup required (auth returns 503)
	authMW, _ := middleware.NewAuthMiddleware(middleware.AuthProvider{GetUsername: func() string { return "admin" }, GetHash: func() string { return "" }}, "", middleware.AuthRateLimitConfig{})
	ts := setupTestServer(t, tmpDir, authMW)
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL + "/dav/camera-01/recording_001.mp4")
	require.NoError(t, err)
	defer resp.Body.Close()

	// Setup mode: request should be rejected with 503
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
}

func TestGETNonexistentFile(t *testing.T) {
	tmpDir := t.TempDir()
	createTestFiles(t, tmpDir)

	ts := setupTestServer(t, tmpDir, nil)
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL + "/dav/camera-01/nonexistent.mp4")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestNewServerDefaultPrefix(t *testing.T) {
	store, err := storage.NewManager(t.TempDir())
	require.NoError(t, err)

	srv := NewServer(store, "", nil, nil, false)
	assert.Equal(t, "/dav", srv.pathPrefix)
}
