package cleanup

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/iobudget"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/stretchr/testify/require"
)

// fakeBudget records Wait calls without blocking (#751 wiring).
type fakeBudget struct {
	mu     sync.Mutex
	calls  []int64
	consum []string
	err    error // when set, Wait returns it (simulates ctx cancel)
}

func (f *fakeBudget) Wait(_ context.Context, consumer string, n int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, n)
	f.consum = append(f.consum, consumer)
	return f.err
}

func (f *fakeBudget) total() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	var total int64
	for _, n := range f.calls {
		total += n
	}
	return total
}

func (f *fakeBudget) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeBudget) allConsumersCleanup() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.consum {
		if c != iobudget.ConsumerCleanup {
			return false
		}
	}
	return true
}

func TestBatchDelete_BudgetBillsReclaimBytes(t *testing.T) {
	env := newTestEnv(t)
	defer env.close(t)

	now := time.Now().UTC()
	env.insertTestRecording(t, "r1", "cam1", "cam1/seg1.mp4", now.Add(-48*time.Hour), false)
	env.insertTestRecording(t, "r2", "cam1", "cam1/seg2.mp4", now.Add(-48*time.Hour), false)

	// Grow the placeholder files the helper wrote: billed bytes must reflect
	// the actual reclaim size.
	path1 := filepath.Join(env.store.RootDir(), "cam1", "seg1.mp4")
	path2 := filepath.Join(env.store.RootDir(), "cam1", "seg2.mp4")
	for _, p := range []string{path1, path2} {
		require.NoError(t, os.WriteFile(p, make([]byte, 4096), 0o644))
	}

	cm, err := NewCleanupManager(env.db, env.store, defaultCleanupConfig())
	require.NoError(t, err)

	fb := &fakeBudget{}
	cm.SetIOBudget(fb)

	recs, err := env.listAllRecordings(t)
	require.NoError(t, err)
	require.Len(t, recs, 2)

	deleted, err := cm.BatchDeleteRecordingsWithFiles(t.Context(), recs, "test")
	require.NoError(t, err)
	require.Len(t, deleted, 2)

	require.Equal(t, fb.count(), 2, "one billing per recording")
	require.True(t, fb.allConsumersCleanup(), "billed under the cleanup consumer label")
	require.GreaterOrEqual(t, fb.total(), int64(2*4096), "billed bytes should cover the reclaimed files")

	_, err1 := os.Stat(path1)
	_, err2 := os.Stat(path2)
	require.True(t, os.IsNotExist(err1) && os.IsNotExist(err2), "files still removed")
}

func TestBatchDelete_BudgetDirFormBillsRecursiveSize(t *testing.T) {
	env := newTestEnv(t)
	defer env.close(t)

	// A frame directory (MJPEG-style): 3 JPEGs of 1KB each.
	dirPath := filepath.Join(env.store.RootDir(), "cam1", "frames_r1")
	require.NoError(t, os.MkdirAll(dirPath, 0o755))
	for i := range 3 {
		require.NoError(t, os.WriteFile(filepath.Join(dirPath, string(rune('a'+i))+".jpg"), make([]byte, 1024), 0o644))
	}

	// Directory-form recording row (MJPEG) pointing at the frame tree.
	rec := &model.Recording{
		ID:          "r1",
		CameraID:    "cam1",
		FilePath:    dirPath,
		Format:      model.FormatMJPEG,
		StartedAt:   time.Now().UTC().Add(-49 * time.Hour),
		EndedAt:     time.Now().UTC().Add(-48 * time.Hour),
		Duration:    3600.0,
		FileSize:    3072,
		MergeStatus: model.MergeStatusPending,
	}
	require.NoError(t, env.db.InsertRecording(context.Background(), rec))

	cm, err := NewCleanupManager(env.db, env.store, defaultCleanupConfig())
	require.NoError(t, err)

	fb := &fakeBudget{}
	cm.SetIOBudget(fb)

	recs, err := env.listAllRecordings(t)
	require.NoError(t, err)
	require.Len(t, recs, 1)

	_, err = cm.BatchDeleteRecordingsWithFiles(t.Context(), recs, "test")
	require.NoError(t, err)

	require.Equal(t, fb.count(), 1)
	require.GreaterOrEqual(t, fb.total(), int64(3*1024), "dir-form recording billed by recursive size estimate")
	require.NoDirExists(t, dirPath)
}

func TestBatchDelete_BudgetWaitErrorStillDeletesFiles(t *testing.T) {
	env := newTestEnv(t)
	defer env.close(t)

	path := filepath.Join(env.store.RootDir(), "cam1", "seg1.mp4")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, make([]byte, 2048), 0o644))

	now := time.Now().UTC()
	env.insertTestRecording(t, "r1", "cam1", "cam1/seg1.mp4", now.Add(-48*time.Hour), false)

	cm, err := NewCleanupManager(env.db, env.store, defaultCleanupConfig())
	require.NoError(t, err)

	fb := &fakeBudget{err: context.Canceled}
	cm.SetIOBudget(fb)

	recs, err := env.listAllRecordings(t)
	require.NoError(t, err)

	deleted, err := cm.BatchDeleteRecordingsWithFiles(t.Context(), recs, "test")
	require.NoError(t, err, "budget wait error must not fail the batch delete")
	require.Len(t, deleted, 1)
	require.NoFileExists(t, path, "file removal continues unthrottled after budget abort")
}

func TestBatchDelete_NoBudgetUnchanged(t *testing.T) {
	env := newTestEnv(t)
	defer env.close(t)

	path := filepath.Join(env.store.RootDir(), "cam1", "seg1.mp4")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, make([]byte, 512), 0o644))

	now := time.Now().UTC()
	env.insertTestRecording(t, "r1", "cam1", "cam1/seg1.mp4", now.Add(-48*time.Hour), false)

	cm, err := NewCleanupManager(env.db, env.store, defaultCleanupConfig())
	require.NoError(t, err)

	recs, err := env.listAllRecordings(t)
	require.NoError(t, err)
	require.Len(t, recs, 1)

	deleted, err := cm.BatchDeleteRecordingsWithFiles(t.Context(), recs, "test")
	require.NoError(t, err)
	require.Len(t, deleted, 1)
	require.NoFileExists(t, path)
}

// listAllRecordings fetches every recording row for the test camera.
func (e *testEnv) listAllRecordings(t *testing.T) ([]model.Recording, error) {
	t.Helper()
	return e.db.ListRecordings(context.Background(), model.RecordingFilter{CameraID: "cam1"})
}
