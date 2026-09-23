package api

// handlers_offload_test.go — remote-archive listing + playback proxy
// endpoints (issue #874 batch 2). The proxy endpoint is anonymous (same
// exposure class as /api/recordings/{id}/download: <video> fetches carry no
// Authorization header) and speaks HTTP Range via the coalescing proxy.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/middleware"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakePlaybackProxy serves deterministic bytes through the handler seam.
type fakePlaybackProxy struct {
	content []byte
}

func (f *fakePlaybackProxy) PresignGet(context.Context, string, string, time.Duration) (string, error) {
	return "", fmt.Errorf("presign unsupported")
}

func (f *fakePlaybackProxy) ServeRange(_ context.Context, _, _ string, start, end, total int64) (io.ReadCloser, error) {
	if end < 0 || end > total-1 {
		end = total - 1
	}
	return io.NopCloser(strings.NewReader(string(f.content[start : end+1]))), nil
}

// seedRemoteItem builds an outbox row in the given terminal state and
// returns its outbox id.
func seedRemoteItem(t *testing.T, db *storage.DB, id, cameraID, status string) int64 {
	return seedRemoteItemSized(t, db, id, cameraID, status, 8)
}

func seedRemoteItemSized(t *testing.T, db *storage.DB, id, cameraID, status string, size int64) int64 {
	t.Helper()
	ctx := context.Background()
	ended := time.Now().UTC().Add(-2 * time.Hour)
	rec := &model.Recording{
		ID: id, CameraID: cameraID, FilePath: filepath.Join(t.TempDir(), id+".mp4"),
		Format: model.FormatH264, StartedAt: ended.Add(-time.Hour), EndedAt: ended,
		Duration: 3600, FileSize: size, MergeStatus: model.MergeStatusMerged, MergeTier: "rolling",
	}
	require.NoError(t, db.InsertRecording(ctx, rec))
	_, err := db.EnqueueOffload(ctx, storage.OffloadItem{
		RecordingID: rec.ID, CameraID: cameraID, ObjectKey: "k/" + id,
		LocalPath: rec.FilePath, FileSize: rec.FileSize,
		StartedAt: rec.StartedAt, EndedAt: rec.EndedAt, Duration: rec.Duration, Format: "h264",
	})
	require.NoError(t, err)
	items, err := db.ClaimPendingOffload(ctx, 1)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.NoError(t, db.MarkOffloadUploaded(ctx, items[0].ID, `"e"`, rec.FileSize))
	if status == storage.OffloadStatusEvicted {
		require.NoError(t, db.MarkOffloadEvicted(ctx, items[0].ID))
	}
	return items[0].ID
}

func TestOffloadRecordingsList(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	seedRemoteItem(t, db, "remote-1", "camA", storage.OffloadStatusEvicted)
	seedRemoteItem(t, db, "remote-2", "camB", storage.OffloadStatusEvicted)
	seedRemoteItem(t, db, "still-local", "camA", storage.OffloadStatusUploaded)

	rr := doRequest(t, h.Routes(), "GET", "/api/offload/recordings", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	var items []map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &items))
	require.Len(t, items, 2, "default listing = evicted (remote-only) items")

	rr = doRequest(t, h.Routes(), "GET", "/api/offload/recordings?camera_id=camA", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &items))
	require.Len(t, items, 1)
	assert.Equal(t, "remote-1", items[0]["recording_id"])
	assert.Equal(t, "camA", items[0]["camera_id"])
	assert.Equal(t, "h264", items[0]["format"])
	assert.NotEmpty(t, items[0]["started_at"])
}

func TestOffloadObjectPlayback(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	content := make([]byte, 100)
	for i := range content {
		content[i] = byte(i)
	}
	h.SetOffloadPlayback(&fakePlaybackProxy{content: content})

	id := seedRemoteItemSized(t, db, "remote-play", "camA", storage.OffloadStatusEvicted, 100)
	url := fmt.Sprintf("/api/offload/objects/%d", id)

	// Full GET: 200 + complete body + Content-Length.
	rr := doRequest(t, h.Routes(), "GET", url, nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "100", rr.Header().Get("Content-Length"))
	body, err := io.ReadAll(rr.Body)
	require.NoError(t, err)
	assert.Equal(t, content, body)

	// Ranged GET: 206 + Content-Range + correct slice.
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Range", "bytes=10-19")
	resp := doReq(t, h.Routes(), req)
	require.Equal(t, http.StatusPartialContent, resp.Code)
	assert.Equal(t, "bytes 10-19/100", resp.Header().Get("Content-Range"))
	assert.Equal(t, "10", resp.Header().Get("Content-Length"))
	body, _ = io.ReadAll(resp.Body)
	assert.Equal(t, content[10:20], body)

	// HEAD probe (browser <video> sizing).
	req, _ = http.NewRequest(http.MethodHead, url, nil)
	resp = doReq(t, h.Routes(), req)
	require.Equal(t, http.StatusOK, resp.Code)
	assert.Equal(t, "100", resp.Header().Get("Content-Length"))

	// Unsatisfiable range: 416 + Content-Range bytes */total.
	req, _ = http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Range", "bytes=500-600")
	resp = doReq(t, h.Routes(), req)
	assert.Equal(t, http.StatusRequestedRangeNotSatisfiable, resp.Code)
	assert.Equal(t, "bytes */100", resp.Header().Get("Content-Range"))

	// Open-ended range: bytes=50- → rest of the object.
	req, _ = http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Range", "bytes=50-")
	resp = doReq(t, h.Routes(), req)
	require.Equal(t, http.StatusPartialContent, resp.Code)
	body, _ = io.ReadAll(resp.Body)
	assert.Equal(t, content[50:], body)
}

func TestOffloadObjectPlaybackGuards(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)
	h.SetOffloadPlayback(&fakePlaybackProxy{content: []byte("0123456789")})

	// Unknown id.
	rr := doRequest(t, h.Routes(), "GET", "/api/offload/objects/999", nil, "", "")
	assert.Equal(t, http.StatusNotFound, rr.Code)

	// Non-evicted/local item: the local file still exists — the remote proxy
	// must not serve it (playback uses the normal local path).
	id := seedRemoteItem(t, db, "remote-local", "camA", storage.OffloadStatusUploaded)
	rr = doRequest(t, h.Routes(), "GET", fmt.Sprintf("/api/offload/objects/%d", id), nil, "", "")
	assert.Equal(t, http.StatusNotFound, rr.Code, "uploaded (still-local) items are not served by the remote proxy")

	// No proxy wired (offload disabled) → 404, not 500.
	db2, store2 := setupTestDB(t)
	defer db2.Close()
	h2 := TestHandler(db2, store2)
	id2 := seedRemoteItem(t, db2, "remote-noproxy", "camA", storage.OffloadStatusEvicted)
	rr = doRequest(t, h2.Routes(), "GET", fmt.Sprintf("/api/offload/objects/%d", id2), nil, "", "")
	assert.Equal(t, http.StatusNotFound, rr.Code)
}

// TestOffloadObject_Auth pins the auth contract of the remote-archive
// playback proxy (#893): the route sits in the protected media group with the
// recording downloads (outbox ids are predictable — archive playback must not
// leak to unauthenticated listeners). The SPA reaches it via ?token= <video>
// links; the HA integration via BasicAuth.
func TestOffloadObject_Auth(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	hash, err := middleware.HashPassword("secret")
	require.NoError(t, err)
	h := testHandlerWithAuth(db, store, "admin", hash)

	content := []byte("remote-archive-bytes")
	h.SetOffloadPlayback(&fakePlaybackProxy{content: content})
	id := seedRemoteItemSized(t, db, "remote-auth", "camA", storage.OffloadStatusEvicted, int64(len(content)))
	url := fmt.Sprintf("/api/offload/objects/%d", id)

	// GET without credentials — media must not leak anonymously.
	rr := doRequest(t, h.Routes(), http.MethodGet, url, nil, "", "")
	require.Equal(t, http.StatusUnauthorized, rr.Code)

	// HEAD without credentials — same contract for browser <video> probes.
	rr = doRequest(t, h.Routes(), http.MethodHead, url, nil, "", "")
	require.Equal(t, http.StatusUnauthorized, rr.Code)

	// GET with BasicAuth (HA integration path).
	rr = doRequest(t, h.Routes(), http.MethodGet, url, nil, "admin", "secret")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, content, rr.Body.Bytes())

	// GET with ?token= (SPA <video src>/fetch links carry no auth header).
	tok, _ := middleware.SignSessionToken("admin", hash, time.Now())
	req, _ := http.NewRequest(http.MethodGet, url+"?token="+tok, nil)
	resp := doReq(t, h.Routes(), req)
	require.Equal(t, http.StatusOK, resp.Code)
	require.Equal(t, content, resp.Body.Bytes())
}

// doReq executes a prepared request (headers set by the caller) against the router.
func doReq(t *testing.T, h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// seedRemoteItemBucket seeds an outbox row pinned to an explicit bucket.
func seedRemoteItemBucket(t *testing.T, db *storage.DB, id, cameraID, status, bucket string) int64 {
	t.Helper()
	ended := time.Now().UTC().Add(-2 * time.Hour)
	rec := &model.Recording{
		ID: id, CameraID: cameraID, FilePath: filepath.Join(t.TempDir(), id+".mp4"),
		Format: model.FormatH264, StartedAt: ended.Add(-time.Hour), EndedAt: ended,
		Duration: 3600, FileSize: 8, MergeStatus: model.MergeStatusMerged, MergeTier: "rolling",
	}
	require.NoError(t, db.InsertRecording(context.Background(), rec))
	_, err := db.EnqueueOffload(context.Background(), storage.OffloadItem{
		RecordingID: rec.ID, CameraID: cameraID, ObjectKey: "k/" + id, Bucket: bucket,
		LocalPath: rec.FilePath, FileSize: rec.FileSize,
		StartedAt: rec.StartedAt, EndedAt: rec.EndedAt, Duration: rec.Duration, Format: "h264",
	})
	require.NoError(t, err)
	items, err := db.ClaimPendingOffload(context.Background(), 1)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.NoError(t, db.MarkOffloadUploaded(context.Background(), items[0].ID, `"e"`, rec.FileSize))
	if status == storage.OffloadStatusEvicted {
		require.NoError(t, db.MarkOffloadEvicted(context.Background(), items[0].ID))
	}
	return items[0].ID
}

func TestOffloadStatusEndpoint(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	seedRemoteItem(t, db, "st-1", "camA", storage.OffloadStatusEvicted)
	rr := doRequest(t, h.Routes(), "GET", "/api/offload/status", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	var status struct {
		Counts  map[string]int `json:"counts"`
		Backlog int            `json:"backlog"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &status))
	assert.Equal(t, 1, status.Counts[storage.OffloadStatusEvicted])
	assert.Equal(t, 0, status.Backlog)
}

func TestSettingsRemoteRoundTrip(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "cfg.yaml")
	cfg := &config.Config{}
	cfg.ApplyDefaults()
	require.NoError(t, config.Save(cfgPath, cfg))
	cfg.Storage.Remote.Enabled = true
	cfg.Storage.Remote.EndpointURL = "http://minio:9000"
	cfg.Storage.Remote.Bucket = "b"
	cfg.Storage.Remote.AccessKeyID = "ak"
	cfg.Storage.Remote.SecretAccessKey = "sk"
	cfg.Storage.Remote.Upload.MinAgeS = 900

	h := newHandlerWithConfig(db, store, cfg)
	h.configPath = cfgPath

	// GET masks the secret.
	rr := doRequest(t, h.Routes(), "GET", "/api/settings", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	var get map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &get))
	remote := get["storage"].(map[string]any)["remote"].(map[string]any)
	assert.Equal(t, true, remote["enabled"])
	assert.Equal(t, "http://minio:9000", remote["endpoint_url"])
	assert.Equal(t, "ak", remote["access_key_id"])
	assert.Equal(t, true, remote["secret_configured"])
	_, hasSecret := remote["secret_access_key"]
	assert.False(t, hasSecret, "secret must never be returned")

	// PUT updates the bucket, keeps the blank secret, reports restart.
	body := `{"storage":{"remote":{"bucket":"new-bucket","secret_access_key":""}}}`
	rr = doRequest(t, h.Routes(), "PUT", "/api/settings", strings.NewReader(body), "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "new-bucket", cfg.Storage.Remote.Bucket)
	assert.Equal(t, "sk", cfg.Storage.Remote.SecretAccessKey, "blank secret keeps current value")
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Equal(t, true, resp["restart_required"])

	// PUT with an invalid section is rejected wholesale (#867 discipline).
	body = `{"storage":{"remote":{"enabled":true,"endpoint_url":"not a url","bucket":"","access_key_id":"","secret_access_key":""}}}`
	rr = doRequest(t, h.Routes(), "PUT", "/api/settings", strings.NewReader(body), "", "")
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Equal(t, "new-bucket", cfg.Storage.Remote.Bucket, "rejected save must not half-commit")
}

// presigningProxy fakes a proxy that can presign.
type presigningProxy struct {
	fakePlaybackProxy
	presigned map[string]string // bucket → URL
	fail      bool
}

func (p *presigningProxy) ServeRange(ctx context.Context, bucket, key string, start, end, total int64) (io.ReadCloser, error) {
	return p.fakePlaybackProxy.ServeRange(ctx, bucket, key, start, end, total)
}

func (p *presigningProxy) PresignGet(_ context.Context, bucket, _ string, _ time.Duration) (string, error) {
	if p.fail {
		return "", fmt.Errorf("presigner unavailable")
	}
	if u, ok := p.presigned[bucket]; ok {
		return u, nil
	}
	return "https://store.example.com/signed-default", nil
}

func TestOffloadObjectPresignedRedirect(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)
	if h.config == nil {
		h.config = &config.Config{}
	}
	h.config.ApplyDefaults()
	h.config.Storage.Remote.Playback.Presigned = true
	h.config.Storage.Remote.Playback.TTLS = 900
	h.SetOffloadPlayback(&presigningProxy{
		fakePlaybackProxy: fakePlaybackProxy{content: []byte("0123456789")},
		presigned:         map[string]string{"vip": "https://store.example.com/signed-vip"},
	})

	// Default bucket → 302 to the presigned URL; no body streamed.
	id := seedRemoteItem(t, db, "remote-ps", "camA", storage.OffloadStatusEvicted)
	url := fmt.Sprintf("/api/offload/objects/%d", id)
	rr := doRequest(t, h.Routes(), "GET", url, nil, "", "")
	require.Equal(t, http.StatusFound, rr.Code)
	assert.Equal(t, "https://store.example.com/signed-default", rr.Header().Get("Location"))

	// Routed bucket → bucket-specific presigned URL (the outbox row pins it).
	id2 := seedRemoteItemBucket(t, db, "remote-vip", "camA", storage.OffloadStatusEvicted, "vip")
	rr = doRequest(t, h.Routes(), "GET", fmt.Sprintf("/api/offload/objects/%d", id2), nil, "", "")
	require.Equal(t, http.StatusFound, rr.Code)
	assert.Equal(t, "https://store.example.com/signed-vip", rr.Header().Get("Location"))

	// Presigner failure degrades to proxying (206 path still works).
	h.SetOffloadPlayback(&presigningProxy{fakePlaybackProxy: fakePlaybackProxy{content: []byte("0123456789")}, fail: true})
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Range", "bytes=2-5")
	resp := doReq(t, h.Routes(), req)
	require.Equal(t, http.StatusPartialContent, resp.Code, "presign failure must fall back to the proxy")

	// Presigned disabled → plain proxy behavior (302 must NOT happen).
	h.config.Storage.Remote.Playback.Presigned = false
	h.SetOffloadPlayback(&presigningProxy{fakePlaybackProxy: fakePlaybackProxy{content: []byte("0123456789")}})
	rr = doRequest(t, h.Routes(), "GET", url, nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
}
