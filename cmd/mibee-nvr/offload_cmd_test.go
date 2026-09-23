package main

// offload_cmd_test.go — `mibee-nvr offload` CLI: status reporting and the
// evict flow (dry-run default, HeadObject-verified, evict.after_days window
// honored, --all-uploaded override). The S3 store is injected via a seam.

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/objectstore"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cliFakeStore mirrors the offload tests' fake store at the CLI layer.
type cliFakeStore struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func newCLIFakeStore() *cliFakeStore { return &cliFakeStore{objects: map[string][]byte{}} }

func (f *cliFakeStore) Put(_ context.Context, key string, body io.Reader, _ int64) (string, error) {
	b, _ := io.ReadAll(body)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[key] = b
	return `"cli-etag"`, nil
}

func (f *cliFakeStore) GetRange(_ context.Context, key string, start, end int64) (io.ReadCloser, objectstore.ObjectInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.objects[key]
	if !ok {
		return nil, objectstore.ObjectInfo{}, objectstore.ErrObjectNotFound
	}
	if end < 0 || end >= int64(len(b)) {
		end = int64(len(b)) - 1
	}
	info := objectstore.ObjectInfo{Key: key, Size: int64(len(b))}
	return io.NopCloser(bytes.NewReader(b[start : end+1])), info, nil
}

func (f *cliFakeStore) Head(_ context.Context, key string) (objectstore.ObjectInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.objects[key]
	if !ok {
		return objectstore.ObjectInfo{}, objectstore.ErrObjectNotFound
	}
	return objectstore.ObjectInfo{Key: key, Size: int64(len(b))}, nil
}

// newOffloadCLITestDB opens an initialized DB with one uploaded item whose
// local file exists and whose object exists in the fake store.
func newOffloadCLITestDB(t *testing.T, store *cliFakeStore) *storage.DB {
	t.Helper()
	db, err := storage.New(filepath.Join(t.TempDir(), "cli.db"))
	require.NoError(t, err)
	require.NoError(t, db.Init(context.Background()))
	t.Cleanup(func() { db.Close() })

	dir := t.TempDir()
	path := filepath.Join(dir, "merged.mp4")
	content := "cli-archive-content"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	ended := time.Now().UTC().Add(-2 * time.Hour)
	rec := &model.Recording{
		ID: "cli-1", CameraID: "camA", FilePath: path, Format: model.FormatH264,
		StartedAt: ended.Add(-time.Hour), EndedAt: ended, Duration: 3600,
		FileSize: int64(len(content)), MergeStatus: model.MergeStatusMerged, MergeTier: "rolling",
	}
	require.NoError(t, db.InsertRecording(context.Background(), rec))

	key := "recordings/camA/" + rec.StartedAt.UTC().Format("2006/01/02") + "/cli-1.mp4"
	_, err = db.EnqueueOffload(context.Background(), storage.OffloadItem{
		RecordingID: rec.ID, CameraID: "camA", ObjectKey: key, LocalPath: path, FileSize: rec.FileSize,
	})
	require.NoError(t, err)
	items, err := db.ClaimPendingOffload(context.Background(), 1)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.NoError(t, db.MarkOffloadUploaded(context.Background(), items[0].ID, `"cli-etag"`, rec.FileSize))

	_, err = store.Put(context.Background(), key, strings.NewReader(content), rec.FileSize)
	require.NoError(t, err)
	return db
}

func TestRunOffloadStatus(t *testing.T) {
	store := newCLIFakeStore()
	db := newOffloadCLITestDB(t, store)

	var out bytes.Buffer
	require.NoError(t, runOffloadStatus(db, &out))
	s := out.String()
	assert.Contains(t, s, "uploaded:")
	assert.Contains(t, s, "1")
	assert.Contains(t, s, "pending:")
}

func TestRunOffloadEvictDryRunByDefault(t *testing.T) {
	store := newCLIFakeStore()
	db := newOffloadCLITestDB(t, store)

	var out bytes.Buffer
	err := runOffloadEvict(db, store, config.RemoteStorageConfig{}, offloadEvictFlags{AllUploaded: true}, &out)
	require.NoError(t, err)
	assert.Contains(t, out.String(), "DRY-RUN")
	// The full CLI output rides along in the failure message: an empty
	// eligible count prints its REFUSED reasons on the next lines, which is
	// the difference between "window bug" and "remote verify refused"
	// (#891 Windows triage).
	assert.Contains(t, out.String(), "eligible 1 item(s)", "full evict output:\n%s", out.String())

	// Dry-run changes nothing.
	counts, err := db.CountOffloadByStatus(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, counts[storage.OffloadStatusUploaded])
}

func TestRunOffloadEvictExecuteAllUploaded(t *testing.T) {
	store := newCLIFakeStore()
	db := newOffloadCLITestDB(t, store)

	var out bytes.Buffer
	err := runOffloadEvict(db, store, config.RemoteStorageConfig{}, offloadEvictFlags{AllUploaded: true, Execute: true}, &out)
	require.NoError(t, err)
	assert.Contains(t, out.String(), "evicted 1", "full evict output:\n%s", out.String())

	counts, err := db.CountOffloadByStatus(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, counts[storage.OffloadStatusEvicted])
}

func TestRunOffloadEvictAfterDaysWindow(t *testing.T) {
	store := newCLIFakeStore()
	db := newOffloadCLITestDB(t, store)

	// after_days=7 but the upload was confirmed moments ago → nothing yet.
	var out bytes.Buffer
	err := runOffloadEvict(db, store, config.RemoteStorageConfig{Evict: config.RemoteEvictConfig{AfterDays: 7}}, offloadEvictFlags{Execute: true}, &out)
	require.NoError(t, err)
	assert.Contains(t, out.String(), "evicted 0 item(s)", "nothing inside the retention window")

	counts, _ := db.CountOffloadByStatus(context.Background())
	assert.Equal(t, 1, counts[storage.OffloadStatusUploaded])
}

func TestRunOffloadEvictNeedsWindowOrFlag(t *testing.T) {
	store := newCLIFakeStore()
	db := newOffloadCLITestDB(t, store)

	// after_days=0 (upload-only) and no --all-uploaded → hard error.
	var out bytes.Buffer
	err := runOffloadEvict(db, store, config.RemoteStorageConfig{}, offloadEvictFlags{Execute: true}, &out)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--all-uploaded")
}
