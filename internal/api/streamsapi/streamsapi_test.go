package streamsapi

// Package-level tests for the extracted streaming domain: the Register route
// surface and the nil-deps degradation contracts. Handler-level route tests
// (through the assembled api.Handler) stay in internal/api.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"
)

// setupStreamsTestDB mirrors internal/api's setupTestDB for the few tests
// that need a real camera manager.
func setupStreamsTestDB(t *testing.T) (*storage.DB, *storage.Manager) {
	t.Helper()
	dir := t.TempDir()
	db, err := storage.New(filepath.Join(dir, "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.Init(context.Background()))
	store, err := storage.NewManager(filepath.Join(dir, "storage"))
	require.NoError(t, err)
	return db, store
}

func TestRegister_RoutesAreMounted(t *testing.T) {
	t.Parallel()
	r := chi.NewRouter()
	Register(r, Deps{})

	// Request-level assertions cannot distinguish "route missing" from
	// "handler degraded to 404" under nil deps — verify the mount surface
	// through chi.Walk instead (same mechanism as the api golden snapshot).
	got := map[string]bool{}
	require.NoError(t, chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		got[method+" "+route] = true
		return nil
	}))
	for _, want := range []string{
		"GET /api/streams",
		"GET /api/cameras/{id}/stream/ws",
		"GET /api/cameras/{id}/stream/*",
		"DELETE /api/cameras/{id}/stream",
		"POST /api/cameras/{id}/stream/webrtc",
		"DELETE /api/cameras/{id}/stream/webrtc/{session}",
		"GET /api/cameras/{id}/stream.flv",
		"GET /api/cameras/{id}/stream.mjpeg",
		"GET /api/cameras/{id}/latest-frame",
		"GET /api/cameras/{id}/snapshot",
		"GET /api/cameras/{id}/protocols",
		"GET /api/cameras/{id}/flow",
	} {
		require.True(t, got[want], "route %s must be mounted", want)
	}
}

// TestNilDeps_DegradeNotPanic walks the endpoints that answer with degraded
// semantics when their manager is absent (mirrors the pre-extraction nil
// checks — 404/503, never a panic).
func TestNilDeps_DegradeNotPanic(t *testing.T) {
	t.Parallel()
	// DB is structural (always wired by NewHandler in production); managers
	// and optional capabilities are the nil-degraded surface.
	db, _ := setupStreamsTestDB(t)
	r := chi.NewRouter()
	Register(r, Deps{DB: db})

	cases := []struct {
		method, path string
		want         int
	}{
		{"GET", "/api/cameras/c1/stream.ws.does-not-exist", http.StatusNotFound}, // wildcard miss
		{"GET", "/api/cameras/c1/stream.ws", http.StatusNotFound},
		{"GET", "/api/cameras/c1/flow", http.StatusNotFound},                          // no cam manager → no hub
		{"GET", "/api/cameras/c1/latest-frame", http.StatusNotFound},                  // no capturer
		{"GET", "/api/cameras/c1/snapshot", http.StatusNotFound},                      // no snapshot URL configured
		{"GET", "/api/cameras/c1/stream.mjpeg", http.StatusNotFound},                  // no recorder
		{"GET", "/api/cameras/c1/protocols", http.StatusNotFound},                     // db has no camera
		{"GET", "/api/streams", http.StatusOK},                                        // nil cam → empty streams list
		{"DELETE", "/api/cameras/c1/stream/webrtc/s1", http.StatusServiceUnavailable}, // no webrtc manager
	}
	for _, tc := range cases {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		require.Equal(t, tc.want, rr.Code, "%s %s", tc.method, tc.path)
	}
}

func TestSnapshotStore_TTLAndBackdate(t *testing.T) {
	t.Parallel()
	st := NewSnapshotStore()
	st.Seed("cam-1", []byte("jpeg"), time.Now())
	_, ok := st.get("cam-1")
	require.True(t, ok)

	st.Backdate("cam-1", -LatestFrameCacheTTL-time.Second)
	c, ok := st.get("cam-1")
	require.True(t, ok)
	require.True(t, time.Since(c.timestamp) > LatestFrameCacheTTL, "entry must be stale now")
}
