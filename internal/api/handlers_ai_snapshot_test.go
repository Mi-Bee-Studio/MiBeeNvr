package api

// Tests for GET /api/ai/events/{id}/snapshot — serving the event snapshot
// image recorded by MiBeeVision (sidecar deployments write JPEGs into
// <storage root>/ai-snapshots/ and report the storage-root-relative path in
// the event's snapshot_path). Covers the happy path, every 404 variant,
// traversal containment, and cache headers for list-view thumbnails.

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/stretchr/testify/require"
)

// snapshotEnv builds a Handler with a real temp storage root so snapshot
// files can be materialized on disk, plus direct DB access for seeding
// events. Auth is noop'd (the route rides the authenticated group); the
// API-key wrapper is applied for upload-path tests.
func snapshotEnv(t *testing.T) (*storage.DB, http.Handler, string) {
	t.Helper()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	cfg := &config.Config{Storage: config.StorageConfig{RootDir: store.RootDir()}}
	h := NewHandler(db, store, noopAuthMW(), cfg, nil, nil, "", nil, nil, nil, nil, nil)
	return db, h.Routes(), store.RootDir()
}

// seedEvent inserts an AI event with the given snapshot path and returns its id.
func seedEvent(t *testing.T, db *storage.DB, snapshotPath string) int64 {
	t.Helper()
	id, err := db.InsertAIEvent(context.Background(), &storage.AIEvent{
		CameraID:     "cam-1",
		EventType:    "loitering",
		SnapshotPath: snapshotPath,
	})
	require.NoError(t, err)
	return id
}

func TestAI_EventSnapshot_ServesFile(t *testing.T) {
	t.Parallel()
	db, routes, root := snapshotEnv(t)

	jpeg := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0xFF, 0xD9}
	dir := filepath.Join(root, "ai-snapshots")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cam1_e1_f2.jpg"), jpeg, 0o644))

	id := seedEvent(t, db, "ai-snapshots/cam1_e1_f2.jpg")

	rr := doRequest(t, routes, http.MethodGet,
		"/api/ai/events/"+strconv.FormatInt(id, 10)+"/snapshot", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code, "body=%s", rr.Body.String())
	require.Equal(t, jpeg, rr.Body.Bytes())
	require.Equal(t, "image/jpeg", rr.Header().Get("Content-Type"))
	require.NotEmpty(t, rr.Header().Get("Cache-Control"), "list views fetch thumbnails repeatedly; caching headers required")
}

func TestAI_EventSnapshot_AbsolutePathInsideRoot(t *testing.T) {
	t.Parallel()
	db, routes, root := snapshotEnv(t)

	// Same-host integrators may report the absolute path — accept it as long
	// as it resolves inside the storage root.
	jpeg := []byte{0xFF, 0xD8, 0xFF, 0xD9}
	require.NoError(t, os.WriteFile(filepath.Join(root, "abs-snap.jpg"), jpeg, 0o644))
	seedEvent(t, db, filepath.Join(root, "abs-snap.jpg"))

	rr := doRequest(t, routes, http.MethodGet, "/api/ai/events/1/snapshot", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, jpeg, rr.Body.Bytes())
}

func TestAI_EventSnapshot_NoSnapshotPath(t *testing.T) {
	t.Parallel()
	db, routes, _ := snapshotEnv(t)
	seedEvent(t, db, "") // remote Vision deployments leave it empty

	rr := doRequest(t, routes, http.MethodGet, "/api/ai/events/1/snapshot", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestAI_EventSnapshot_MissingFileOnDisk(t *testing.T) {
	t.Parallel()
	db, routes, _ := snapshotEnv(t)
	seedEvent(t, db, "ai-snapshots/never-written.jpg")

	rr := doRequest(t, routes, http.MethodGet, "/api/ai/events/1/snapshot", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestAI_EventSnapshot_RejectsTraversal(t *testing.T) {
	t.Parallel()
	db, routes, root := snapshotEnv(t)

	// Plant a readable file OUTSIDE the storage root.
	outside := filepath.Join(filepath.Dir(root), "secret.txt")
	require.NoError(t, os.WriteFile(outside, []byte("top-secret"), 0o644))
	t.Cleanup(func() { _ = os.Remove(outside) })

	for _, rel := range []string{
		"../secret.txt",
		"../../secret.txt",
		"ai-snapshots/../../../secret.txt",
	} {
		id := seedEvent(t, db, rel)
		rr := doRequest(t, routes, http.MethodGet,
			"/api/ai/events/"+strconv.FormatInt(id, 10)+"/snapshot", nil, "", "")
		require.Equal(t, http.StatusNotFound, rr.Code, "snapshot_path=%q must not serve %s", rel, outside)
	}
}

func TestAI_EventSnapshot_AbsolutePathOutsideRoot(t *testing.T) {
	t.Parallel()
	db, routes, _ := snapshotEnv(t)
	// Remote-deployed Vision reporting its own host's absolute path — the
	// file may coincidentally exist on the NVR host; it must not be served.
	seedEvent(t, db, "/etc/hostname")

	rr := doRequest(t, routes, http.MethodGet, "/api/ai/events/1/snapshot", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestAI_EventSnapshot_UnknownOrInvalidID(t *testing.T) {
	t.Parallel()
	_, routes, _ := snapshotEnv(t)

	rr := doRequest(t, routes, http.MethodGet, "/api/ai/events/abc/snapshot", nil, "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)

	rr = doRequest(t, routes, http.MethodGet, "/api/ai/events/424242/snapshot", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
}

// ---- POST /api/ai/events/{id}/snapshot（上传，远程部署的 Vision 推图）----

func uploadEnv(t *testing.T) (*storage.DB, http.Handler, string) {
	t.Helper()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	cfg := &config.Config{Storage: config.StorageConfig{RootDir: store.RootDir()}}
	h := NewHandler(db, store, noopAuthMW(), cfg, nil, nil, "", nil, nil, nil, nil, nil)
	// 上传路径要求 API Key（与 POST /api/ai/events 同模型）。
	return db, withAPIKey("vision", h.Routes()), store.RootDir()
}

var jpegHeader = []byte{0xFF, 0xD8, 0xFF, 0xE0}

func TestAI_EventSnapshot_UploadAndServe(t *testing.T) {
	t.Parallel()
	db, routes, _ := uploadEnv(t)

	id := seedEvent(t, db, "") // 事件先落库（无快照）
	jpeg := append(append([]byte{}, jpegHeader...), []byte("vision-frame")...)

	rr := doUpload(t, routes, "/api/ai/events/"+strconv.FormatInt(id, 10)+"/snapshot", jpeg)
	require.Equal(t, http.StatusOK, rr.Code, "body=%s", rr.Body.String())

	// DB 行回填相对路径。
	evt, err := db.GetAIEvent(context.Background(), id)
	require.NoError(t, err)
	require.NotEmpty(t, evt.SnapshotPath)
	require.Contains(t, evt.SnapshotPath, "ai-snapshots/")

	// GET 端点随即可服务同一份字节。
	rr = doRequest(t, routes, http.MethodGet,
		"/api/ai/events/"+strconv.FormatInt(id, 10)+"/snapshot", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, jpeg, rr.Body.Bytes())
}

func TestAI_EventSnapshot_UploadRequiresAPIKey(t *testing.T) {
	t.Parallel()
	db, routes, _ := snapshotEnv(t) // 无 API-key 包装
	id := seedEvent(t, db, "")

	rr := doUpload(t, routes, "/api/ai/events/"+strconv.FormatInt(id, 10)+"/snapshot", jpegHeader)
	require.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestAI_EventSnapshot_UploadUnknownEvent(t *testing.T) {
	t.Parallel()
	_, routes, _ := uploadEnv(t)
	rr := doUpload(t, routes, "/api/ai/events/424242/snapshot", jpegHeader)
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestAI_EventSnapshot_UploadRejectsNonJPEG(t *testing.T) {
	t.Parallel()
	db, routes, _ := uploadEnv(t)
	id := seedEvent(t, db, "")

	rr := doUpload(t, routes, "/api/ai/events/"+strconv.FormatInt(id, 10)+"/snapshot",
		[]byte("this is definitely not a jpeg"))
	require.Equal(t, http.StatusBadRequest, rr.Code)

	// 失败上传不落任何文件、不改 DB。
	evt, err := db.GetAIEvent(context.Background(), id)
	require.NoError(t, err)
	require.Empty(t, evt.SnapshotPath)
}

func TestAI_EventSnapshot_UploadSizeCap(t *testing.T) {
	t.Parallel()
	db, routes, _ := uploadEnv(t)
	id := seedEvent(t, db, "")

	big := append(append([]byte{}, jpegHeader...), make([]byte, maxAIEventSnapshotBytes)...)
	rr := doUpload(t, routes, "/api/ai/events/"+strconv.FormatInt(id, 10)+"/snapshot", big)
	require.Equal(t, http.StatusRequestEntityTooLarge, rr.Code)
}

// doUpload：POST 原始字节（无 BasicAuth——API-key 语境由路由包装注入）。
func doUpload(t *testing.T, handler http.Handler, path string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "image/jpeg")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}
