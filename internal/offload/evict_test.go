package offload

// evict_test.go — the pre-eviction safety line (issue #874): local deletion
// happens ONLY after a fresh HeadObject confirms the remote object exists
// with the exact uploaded size. A failed or mismatched verification refuses
// eviction — that check is the only defense against "upload lied, recording
// lost".

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/objectstore"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedUploadedItem builds a recording + outbox row in 'uploaded' state with
// the object present in the fake store. Returns (rec, outboxID, key).
func seedUploadedItem(t *testing.T, db *storage.DB, store *fakeStore, id, cameraID string) (*model.Recording, int64, string) {
	t.Helper()
	rec := seedMergedRecording(t, db, id, cameraID, 2*time.Hour, "archived-content")
	key := ObjectKey("recordings", cameraID, rec.StartedAt, rec.ID, ".mp4")
	ctx := context.Background()
	_, err := db.EnqueueOffload(ctx, storage.OffloadItem{
		RecordingID: rec.ID, CameraID: cameraID, ObjectKey: key,
		LocalPath: rec.FilePath, FileSize: rec.FileSize,
	})
	require.NoError(t, err)
	items, err := db.ClaimPendingOffload(ctx, 1)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.NoError(t, db.MarkOffloadUploaded(ctx, items[0].ID, `"e"`, rec.FileSize))
	// The fake store must actually hold the object.
	_, err = store.Put(ctx, key, strings.NewReader("archived-content"), rec.FileSize)
	require.NoError(t, err)
	return rec, items[0].ID, key
}

func TestEvictDryRunDefault(t *testing.T) {
	dir := t.TempDir()
	db, err := storage.New(filepath.Join(dir, "e.db"))
	require.NoError(t, err)
	require.NoError(t, db.Init(context.Background()))
	defer db.Close()
	store := newFakeStore()
	rec, _, _ := seedUploadedItem(t, db, store, "e1", "camA")

	sum, err := RunEvict(context.Background(), db, EvictOptions{
		StoreFor:        func(string) (objectstore.Store, error) { return store, nil },
		ConfirmedBefore: time.Now().UTC().Add(time.Minute),
		Execute:         false, // dry-run is the default posture
	})
	require.NoError(t, err)
	assert.Equal(t, 1, sum.Eligible)
	assert.Equal(t, 0, sum.Evicted)

	// Nothing changed on disk or in the DB.
	_, statErr := os.Stat(rec.FilePath)
	require.NoError(t, statErr, "dry-run must not delete the local file")
	row, err := db.GetRecording(context.Background(), rec.ID)
	require.NoError(t, err)
	require.NotNil(t, row)
	counts, _ := db.CountOffloadByStatus(context.Background())
	assert.Equal(t, 1, counts[storage.OffloadStatusUploaded])
}

func TestEvictExecuteVerified(t *testing.T) {
	dir := t.TempDir()
	db, err := storage.New(filepath.Join(dir, "e.db"))
	require.NoError(t, err)
	require.NoError(t, db.Init(context.Background()))
	defer db.Close()
	store := newFakeStore()
	rec, _, _ := seedUploadedItem(t, db, store, "e2", "camA")

	sum, err := RunEvict(context.Background(), db, EvictOptions{
		StoreFor:        func(string) (objectstore.Store, error) { return store, nil },
		ConfirmedBefore: time.Now().UTC().Add(time.Minute),
		Execute:         true,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, sum.Eligible)
	assert.Equal(t, 1, sum.Evicted)
	assert.Equal(t, int64(len("archived-content")), sum.ReclaimedBytes)

	_, statErr := os.Stat(rec.FilePath)
	require.True(t, os.IsNotExist(statErr), "local file must be gone")
	row, err := db.GetRecording(context.Background(), rec.ID)
	require.NoError(t, err)
	require.Nil(t, row, "recording row must be gone")
	counts, _ := db.CountOffloadByStatus(context.Background())
	assert.Equal(t, 1, counts[storage.OffloadStatusEvicted])
}

func TestEvictRefusesWhenRemoteMissing(t *testing.T) {
	dir := t.TempDir()
	db, err := storage.New(filepath.Join(dir, "e.db"))
	require.NoError(t, err)
	require.NoError(t, db.Init(context.Background()))
	defer db.Close()
	store := newFakeStore()
	rec, _, key := seedUploadedItem(t, db, store, "e3", "camA")

	// Someone deleted the object between upload and evict.
	store.mu.Lock()
	delete(store.objects, key)
	store.mu.Unlock()

	sum, err := RunEvict(context.Background(), db, EvictOptions{
		StoreFor:        func(string) (objectstore.Store, error) { return store, nil },
		ConfirmedBefore: time.Now().UTC().Add(time.Minute),
		Execute:         true,
	})
	require.NoError(t, err)
	assert.Equal(t, 0, sum.Eligible)
	assert.Equal(t, 0, sum.Evicted)
	require.Len(t, sum.Refused, 1)
	assert.Contains(t, sum.Refused[0].Reason, objectstore.ErrObjectNotFound.Error())

	// Local file and row survive.
	_, statErr := os.Stat(rec.FilePath)
	require.NoError(t, statErr, "eviction must be refused when the remote object is gone")
	counts, _ := db.CountOffloadByStatus(context.Background())
	assert.Equal(t, 1, counts[storage.OffloadStatusUploaded])
}

func TestEvictRefusesSizeMismatch(t *testing.T) {
	dir := t.TempDir()
	db, err := storage.New(filepath.Join(dir, "e.db"))
	require.NoError(t, err)
	require.NoError(t, db.Init(context.Background()))
	defer db.Close()
	store := newFakeStore()
	rec, _, key := seedUploadedItem(t, db, store, "e4", "camA")

	// The remote object was replaced by something of a different size.
	store.mu.Lock()
	store.objects[key] = []byte("different-length-content!")
	store.mu.Unlock()

	sum, err := RunEvict(context.Background(), db, EvictOptions{
		StoreFor:        func(string) (objectstore.Store, error) { return store, nil },
		ConfirmedBefore: time.Now().UTC().Add(time.Minute),
		Execute:         true,
	})
	require.NoError(t, err)
	assert.Equal(t, 0, sum.Evicted)
	require.Len(t, sum.Refused, 1)
	assert.Contains(t, sum.Refused[0].Reason, "size")

	_, statErr := os.Stat(rec.FilePath)
	require.NoError(t, statErr)
}

func TestEvictCameraFilter(t *testing.T) {
	dir := t.TempDir()
	db, err := storage.New(filepath.Join(dir, "e.db"))
	require.NoError(t, err)
	require.NoError(t, db.Init(context.Background()))
	defer db.Close()
	store := newFakeStore()
	seedUploadedItem(t, db, store, "e5a", "camA")
	seedUploadedItem(t, db, store, "e5b", "camB")

	sum, err := RunEvict(context.Background(), db, EvictOptions{
		StoreFor:        func(string) (objectstore.Store, error) { return store, nil },
		ConfirmedBefore: time.Now().UTC().Add(time.Minute),
		CameraID:        "camA",
		Execute:         true,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, sum.Evicted, "only camA's item evicted")
}

func TestEvictHonorsConfirmationCutoff(t *testing.T) {
	dir := t.TempDir()
	db, err := storage.New(filepath.Join(dir, "e.db"))
	require.NoError(t, err)
	require.NoError(t, db.Init(context.Background()))
	defer db.Close()
	store := newFakeStore()
	seedUploadedItem(t, db, store, "e6", "camA")

	// Cutoff in the past: the item was confirmed "now", so it is NOT yet
	// evictable (the after_days retention window has not elapsed).
	sum, err := RunEvict(context.Background(), db, EvictOptions{
		StoreFor:        func(string) (objectstore.Store, error) { return store, nil },
		ConfirmedBefore: time.Now().UTC().Add(-time.Hour),
		Execute:         true,
	})
	require.NoError(t, err)
	assert.Equal(t, 0, sum.Eligible)
	assert.Equal(t, 0, sum.Evicted)
}
