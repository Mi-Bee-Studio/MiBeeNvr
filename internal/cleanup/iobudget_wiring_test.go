package cleanup

import (
	"context"
	"fmt"
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

// amounts returns the billed amounts in call order.
func (f *fakeBudget) amounts() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]int64, len(f.calls))
	copy(out, f.calls)
	return out
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

// --- #755: byte-rate billing refinements ---

// TestBatchDelete_DirectoryLocalityGrouping: with a budget installed, the
// file-reclaim loop must visit recordings grouped by directory (all of dir A,
// then all of dir B), not in the input (temporal) order. Observable through
// the per-recording billing sequence — each recording's file size is unique,
// so the billed amounts identify the visit order.
func TestBatchDelete_DirectoryLocalityGrouping(t *testing.T) {
	env := newTestEnv(t)
	defer env.close(t)

	now := time.Now().UTC()
	// Interleaved input order: dirA, dirB, dirA, dirB — with distinct sizes
	// r1=1001, r2=2002, r3=3003, r4=4004 so billing calls identify rows.
	input := []struct {
		id, rel string
		size    int
	}{
		{"r1", "cam1/20260910/10/r1.mp4", 1001},
		{"r2", "cam1/20260910/11/r2.mp4", 2002},
		{"r3", "cam1/20260910/10/r3.mp4", 3003},
		{"r4", "cam1/20260910/11/r4.mp4", 4004},
	}
	for _, in := range input {
		env.insertTestRecording(t, in.id, "cam1", in.rel, now.Add(-48*time.Hour), false)
		full := filepath.Join(env.store.RootDir(), in.rel)
		require.NoError(t, os.WriteFile(full, make([]byte, in.size), 0o644))
	}

	cm, err := NewCleanupManager(env.db, env.store, defaultCleanupConfig())
	require.NoError(t, err)

	fb := &fakeBudget{}
	cm.SetIOBudget(fb)

	recs, err := env.listAllRecordings(t)
	require.NoError(t, err)
	byID := map[string]model.Recording{}
	for _, r := range recs {
		byID[r.ID] = r
	}
	ordered := []model.Recording{byID["r1"], byID["r2"], byID["r3"], byID["r4"]}

	_, err = cm.BatchDeleteRecordingsWithFiles(t.Context(), ordered, "test")
	require.NoError(t, err)

	billed := fb.amounts()
	require.Len(t, billed, 4)
	// Grouped visit: {r1,r3} (dir …/10) adjacently, {r2,r4} (dir …/11)
	// adjacently — in either group order.
	firstGroup := [2]int64{billed[0], billed[1]}
	secondGroup := [2]int64{billed[2], billed[3]}
	isPair := func(p [2]int64, a, b int64) bool {
		return (p[0] == a && p[1] == b) || (p[0] == b && p[1] == a)
	}
	grouped := (isPair(firstGroup, 1001, 3003) && isPair(secondGroup, 2002, 4004)) ||
		(isPair(firstGroup, 2002, 4004) && isPair(secondGroup, 1001, 3003))
	require.True(t, grouped,
		"billing order %v is not directory-grouped (want {r1,r3} and {r2,r4} adjacent)", billed)
}

// TestDeleteRecordingFile_UnlinkGuardrail: with an unlink budget installed,
// directory-form deletion paces per FILE against the guardrail (one Wait per
// file) instead of the legacy fixed chunk sleep.
func TestDeleteRecordingFile_UnlinkGuardrail(t *testing.T) {
	env := newTestEnv(t)
	defer env.close(t)

	// Frame tree with 5 files.
	dirPath := filepath.Join(env.store.RootDir(), "cam1", "frames_r1")
	require.NoError(t, os.MkdirAll(dirPath, 0o755))
	for i := range 5 {
		require.NoError(t, os.WriteFile(filepath.Join(dirPath, fmt.Sprintf("f_%d.jpg", i)), []byte("x"), 0o644))
	}

	cm, err := NewCleanupManager(env.db, env.store, defaultCleanupConfig())
	require.NoError(t, err)
	// Legacy pacing configured — must be IGNORED once the guardrail is set
	// (guardrail replaces, not stacks on, the fixed time-slice).
	cm.SetDirectoryDeleteThrottle(100000, time.Hour)

	guard := &fakeBudget{}
	cm.SetUnlinkBudget(guard)

	require.NoError(t, cm.deleteRecordingFile(t.Context(), dirPath))
	require.NoDirExists(t, dirPath)
	require.Equal(t, 5, guard.count(), "one guardrail Wait per unlinked file")
	require.True(t, guard.allConsumersCleanup())
}

// TestDeleteRecordingFile_LegacyPacingWhenNoGuardrail: without an unlink
// budget the fixed chunk pacing still applies (default config = unchanged
// behavior).
func TestDeleteRecordingFile_LegacyPacingWhenNoGuardrail(t *testing.T) {
	env := newTestEnv(t)
	defer env.close(t)

	dirPath := filepath.Join(env.store.RootDir(), "cam1", "frames_r1")
	require.NoError(t, os.MkdirAll(dirPath, 0o755))
	for i := range 3 {
		require.NoError(t, os.WriteFile(filepath.Join(dirPath, fmt.Sprintf("f_%d.jpg", i)), []byte("x"), 0o644))
	}

	cm, err := NewCleanupManager(env.db, env.store, defaultCleanupConfig())
	require.NoError(t, err)
	// Tiny chunk + tiny sleep proves the legacy path executes (the deletion
	// still completes and would have paused between chunks).
	cm.SetDirectoryDeleteThrottle(2, time.Millisecond)

	require.NoError(t, cm.deleteRecordingFile(t.Context(), dirPath))
	require.NoDirExists(t, dirPath)
}
