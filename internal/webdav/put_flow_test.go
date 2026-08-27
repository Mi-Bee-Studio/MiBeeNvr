package webdav

// Read-write PUT flow tests (#570): auto-registration of cameras from
// uploaded paths and the DB-backed resolveOrCreateCamera path.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/stretchr/testify/require"
)

func setupRWServer(t *testing.T, rootDir string, db *storage.DB) *httptest.Server {
	t.Helper()
	store, err := storage.NewManager(rootDir)
	require.NoError(t, err)
	srv := NewServer(store, "/dav", nil, db, true)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func newDavDB(t *testing.T) *storage.DB {
	t.Helper()
	db, err := storage.New(filepath.Join(t.TempDir(), "dav.db"))
	require.NoError(t, err)
	require.NoError(t, db.Init(context.Background()))
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// PUT into a per-camera directory auto-registers the camera (name → row) on
// first upload and reuses the existing row afterwards.
func TestPUTAutoRegistersCamera(t *testing.T) {
	root := t.TempDir()
	db := newDavDB(t)
	ts := setupRWServer(t, root, db)

	put := func(path string) int {
		req, err := http.NewRequest(http.MethodPut, ts.URL+path, strings.NewReader("clip-data"))
		require.NoError(t, err)
		resp, err := ts.Client().Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode
	}

	// WebDAV semantics: the parent collection must exist before PUT.
	mkcol := func(path string) int {
		req, err := http.NewRequest("MKCOL", ts.URL+path, nil)
		require.NoError(t, err)
		resp, err := ts.Client().Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode
	}
	require.Contains(t, []int{http.StatusCreated, http.StatusNoContent, http.StatusOK},
		mkcol("/dav/Front%20Door"))

	code := put("/dav/Front%20Door/clip.mp4")
	require.Equal(t, http.StatusCreated, code)

	cams, err := db.ListCameras(context.Background())
	require.NoError(t, err)
	require.Len(t, cams, 1, "camera auto-registered by name")
	require.Equal(t, "Front Door", cams[0].Name)

	// Second upload to the same camera reuses the row.
	code = put("/dav/Front%20Door/clip2.mp4")
	require.Equal(t, http.StatusCreated, code)
	cams, err = db.ListCameras(context.Background())
	require.NoError(t, err)
	require.Len(t, cams, 1, "same camera reused — no duplicate rows")
}
