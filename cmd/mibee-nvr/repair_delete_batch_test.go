package main

// Tests for #753 — chunked batch deletion in `repair delete-by-format`:
// the execute loop must ride DeleteRecordingsBatch (one transaction per
// chunk) with a per-row fallback when the batch statement fails, instead of
// one transaction per row (measured 1-3 rows/s against a live server).

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/stretchr/testify/require"

	_ "modernc.org/sqlite"
)

// swapRepairDeleteSeams replaces the batch/single delete seams, returning a
// restore func. Counters observe call patterns; failBatch makes the batch
// statement fail so the fallback path runs.
func swapRepairDeleteSeams(t *testing.T, failBatch bool) (batchCalls, singleCalls *atomic.Int64) {
	t.Helper()
	batchCalls = &atomic.Int64{}
	singleCalls = &atomic.Int64{}
	oldBatch, oldSingle := repairDeleteBatchFn, repairDeleteOneFn
	repairDeleteBatchFn = func(ctx context.Context, db *storage.DB, ids []string) ([]string, error) {
		batchCalls.Add(1)
		if failBatch {
			return nil, errors.New("injected batch failure")
		}
		return db.DeleteRecordingsBatch(ctx, ids)
	}
	repairDeleteOneFn = func(ctx context.Context, db *storage.DB, id string) error {
		singleCalls.Add(1)
		return db.DeleteRecording(ctx, id)
	}
	t.Cleanup(func() {
		repairDeleteBatchFn = oldBatch
		repairDeleteOneFn = oldSingle
	})
	return batchCalls, singleCalls
}

// seedChunkTestDB seeds count h264 recordings with on-disk files and returns
// the opened storage DB, a raw SQL handle for assertions, and the records.
func seedChunkTestDB(t *testing.T, count int) (*storage.DB, *sql.DB, []model.Recording) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "mibee-nvr.db")
	db, err := storage.New(dbPath)
	require.NoError(t, err)
	require.NoError(t, db.Init(context.Background()))
	raw, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)

	old := time.Now().UTC().Add(-48 * time.Hour)
	recs := make([]model.Recording, count)
	ctx := context.Background()
	for i := range count {
		f := filepath.Join(dir, fmt.Sprintf("seg_%04d.mp4", i))
		require.NoError(t, os.WriteFile(f, []byte("payload"), 0o644))
		r := repairRec(fmt.Sprintf("chunk-%04d", i), "cam1", "h264", model.MergeStatusPending, old)
		r.FilePath = f
		require.NoError(t, db.InsertRecording(ctx, r))
		recs[i] = *r
	}
	return db, raw, recs
}

// countRowsLeft returns the number of remaining recordings for the camera.
func countRowsLeft(t *testing.T, raw *sql.DB) int {
	t.Helper()
	var n int
	require.NoError(t, raw.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM recordings WHERE camera_id='cam1'").Scan(&n))
	return n
}

// TestDeleteCandidatesInChunks_BatchPathIsUsed: with the batch statement
// healthy, ALL deletes ride chunked batch calls (one transaction per chunk)
// and the end state matches the per-row implementation exactly — rows gone,
// files gone (incl. merge_path siblings), freed bytes accounted.
func TestDeleteCandidatesInChunks_BatchPathIsUsed(t *testing.T) {
	db, raw, recs := seedChunkTestDB(t, 5)
	defer db.Close()
	defer raw.Close()
	// Give two rows a distinct merge_path sibling to reclaim alongside. The
	// DB row and the in-memory candidate must agree — production candidates
	// arrive from ListRecordings with merge_path populated.
	for i := range 2 {
		mp := recs[i].FilePath + ".merged.mp4"
		require.NoError(t, os.WriteFile(mp, []byte("m"), 0o644))
		_, err := raw.ExecContext(context.Background(),
			"UPDATE recordings SET merge_path=? WHERE id=?", mp, recs[i].ID)
		require.NoError(t, err)
		recs[i].MergePath = mp
	}

	batchCalls, singleCalls := swapRepairDeleteSeams(t, false)
	deleted, failed, freed := deleteCandidatesInChunks(context.Background(), db, recs,
		func(d, total int) {})
	require.Equal(t, 5, deleted)
	require.Zero(t, failed)
	require.Equal(t, int64(5*1024), freed)
	require.Equal(t, int64(1), batchCalls.Load(), "5 candidates ≤ chunk size → exactly ONE batch statement")
	require.Zero(t, singleCalls.Load(), "healthy batch path must not fall back to per-row deletes")

	require.Zero(t, countRowsLeft(t, raw))
	for _, r := range recs {
		require.NoFileExists(t, r.FilePath)
		require.NoFileExists(t, r.FilePath+".merged.mp4")
	}
}

// TestDeleteCandidatesInChunks_ChunksLargeSets: more than one chunk's worth
// of candidates splits into ceil(n/chunk) batch calls with correct end state.
func TestDeleteCandidatesInChunks_ChunksLargeSets(t *testing.T) {
	const n = repairDeleteChunkSize*2 + 50
	db, raw, recs := seedChunkTestDB(t, n)
	defer db.Close()
	defer raw.Close()

	batchCalls, singleCalls := swapRepairDeleteSeams(t, false)
	deleted, failed, _ := deleteCandidatesInChunks(context.Background(), db, recs,
		func(d, total int) {})
	require.Equal(t, n, deleted)
	require.Zero(t, failed)
	require.Equal(t, int64(3), batchCalls.Load(), "%d candidates / chunk %d → 3 batch calls", n, repairDeleteChunkSize)
	require.Zero(t, singleCalls.Load())
	require.Zero(t, countRowsLeft(t, raw))
}

// TestDeleteCandidatesInChunks_FallbackOnBatchFailure: a failing batch
// statement degrades to per-row deletes so a single bad row cannot poison
// the chunk — everything still gets deleted.
func TestDeleteCandidatesInChunks_FallbackOnBatchFailure(t *testing.T) {
	db, raw, recs := seedChunkTestDB(t, 5)
	defer db.Close()
	defer raw.Close()

	batchCalls, singleCalls := swapRepairDeleteSeams(t, true)
	deleted, failed, _ := deleteCandidatesInChunks(context.Background(), db, recs,
		func(d, total int) {})
	require.Equal(t, 5, deleted)
	require.Zero(t, failed)
	require.Equal(t, int64(1), batchCalls.Load())
	require.Equal(t, int64(5), singleCalls.Load(), "failed batch must retry the chunk row by row")
	require.Zero(t, countRowsLeft(t, raw))
}

// TestRunRepairDeleteByFormat_ExecuteLargeSetEndState: end-to-end — the CLI
// execute path over a multi-chunk candidate set leaves the same end state as
// the historical per-row implementation (kept formats untouched, files gone).
func TestRunRepairDeleteByFormat_ExecuteLargeSetEndState(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeRepairConfig(t, dir)
	dbPath := filepath.Join(dir, "mibee-nvr.db")

	old := time.Now().UTC().Add(-48 * time.Hour)
	raw, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	db, err := storage.New(dbPath)
	require.NoError(t, err)
	require.NoError(t, db.Init(context.Background()))
	ctx := context.Background()

	keepFile := filepath.Join(dir, "keep.mp4")
	require.NoError(t, os.WriteFile(keepFile, []byte("tl"), 0o644))
	keep := repairRec("big-keep", "cam1", "timelapse", model.MergeStatusPending, old)
	keep.FilePath = keepFile
	require.NoError(t, db.InsertRecording(ctx, keep))

	var wantFreed int64
	for i := range repairDeleteChunkSize + 10 {
		f := filepath.Join(dir, fmt.Sprintf("del_%04d.mp4", i))
		require.NoError(t, os.WriteFile(f, []byte("x"), 0o644))
		r := repairRec(fmt.Sprintf("big-%04d", i), "cam1", "h264", model.MergeStatusPending, old)
		r.FilePath = f
		require.NoError(t, db.InsertRecording(ctx, r))
		wantFreed += r.FileSize
	}
	require.NoError(t, db.Close())
	require.NoError(t, raw.Close())

	var rc int
	withArgs([]string{
		"bin", "repair", "delete-by-format", "--camera", "cam1",
		"--keep-format", "timelapse", "--execute", "--config", cfgPath,
	},
		func() { rc = runRepairDeleteByFormat() })
	require.Zero(t, rc)

	// Everything h264 gone (rows + files); timelapse untouched.
	raw2, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	var h264Left, tlLeft int
	require.NoError(t, raw2.QueryRow("SELECT COUNT(*) FROM recordings WHERE camera_id='cam1' AND format='h264'").Scan(&h264Left))
	require.NoError(t, raw2.QueryRow("SELECT COUNT(*) FROM recordings WHERE camera_id='cam1' AND format='timelapse'").Scan(&tlLeft))
	require.NoError(t, raw2.Close())
	require.Zero(t, h264Left)
	require.Equal(t, 1, tlLeft)
	require.FileExists(t, keepFile)
	for i := range repairDeleteChunkSize + 10 {
		require.NoFileExists(t, filepath.Join(dir, fmt.Sprintf("del_%04d.mp4", i)))
	}
}

// --- I/O budget billing (#751) ---

// fakeRepairBudget records Wait calls without blocking.
type fakeRepairBudget struct {
	mu     sync.Mutex
	calls  []int64
	consum []string
}

func (f *fakeRepairBudget) Wait(_ context.Context, consumer string, n int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, n)
	f.consum = append(f.consum, consumer)
	return nil
}

func (f *fakeRepairBudget) total() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	var total int64
	for _, n := range f.calls {
		total += n
	}
	return total
}

// TestDeleteCandidatesInChunks_BudgetBillsFileSize: with a budget installed,
// each reclaimed row bills its file size under the repair consumer.
func TestDeleteCandidatesInChunks_BudgetBillsFileSize(t *testing.T) {
	db, raw, recs := seedChunkTestDB(t, 5)
	defer raw.Close()
	defer db.Close()
	// Give the seeded rows a nonzero size so the bill is observable.
	var wantBilled int64
	for i := range recs {
		recs[i].FileSize = int64(1000 + i)
		wantBilled += recs[i].FileSize
	}

	fb := &fakeRepairBudget{}
	prev := repairIOBudget
	repairIOBudget = fb
	t.Cleanup(func() { repairIOBudget = prev })

	deleted, failed, freed := deleteCandidatesInChunks(t.Context(), db, recs, func(int, int) {})
	require.Equal(t, 5, deleted)
	require.Zero(t, failed)
	require.Equal(t, wantBilled, freed)
	require.Equal(t, int64(wantBilled), fb.total(), "every reclaimed row's size billed to the budget")
	require.Equal(t, "repair", fb.consum[0])
}

// TestDeleteCandidatesInChunks_NoBudgetUnchanged: nil budget (default) —
// identical end state, zero billing.
func TestDeleteCandidatesInChunks_NoBudgetUnchanged(t *testing.T) {
	db, raw, recs := seedChunkTestDB(t, 3)
	defer raw.Close()
	defer db.Close()

	require.Nil(t, repairIOBudget)
	deleted, failed, _ := deleteCandidatesInChunks(t.Context(), db, recs, func(int, int) {})
	require.Equal(t, 3, deleted)
	require.Zero(t, failed)
	require.Zero(t, countRowsLeft(t, raw))
}

// --- #755: fixed time-slice → budget replacement ---

// TestDeleteCandidatesInChunks_SleepSkippedWhenBudgetOn: with a budget
// installed the fixed inter-chunk sleep must be skipped (billing paces the
// loop); without one (default) the legacy sleep stays.
func TestDeleteCandidatesInChunks_SleepSkippedWhenBudgetOn(t *testing.T) {
	sleepCalls := &atomic.Int64{}
	prevSleep := repairChunkSleepFn
	repairChunkSleepFn = func(ctx context.Context) {
		sleepCalls.Add(1)
		select {
		case <-ctx.Done():
		default:
		}
	}
	t.Cleanup(func() { repairChunkSleepFn = prevSleep })

	// Budget OFF (default): legacy sleep runs per chunk.
	db1, raw1, recs1 := seedChunkTestDB(t, repairDeleteChunkSize+5)
	prevBudget := repairIOBudget
	repairIOBudget = nil
	t.Cleanup(func() { repairIOBudget = prevBudget })
	deleteCandidatesInChunks(t.Context(), db1, recs1, func(int, int) {})
	require.Equal(t, int64(2), sleepCalls.Load(), "legacy pacing: one sleep per chunk")
	db1.Close()
	raw1.Close()

	// Budget ON: no fixed sleep — billing paces instead.
	sleepCalls.Store(0)
	db2, raw2, recs2 := seedChunkTestDB(t, repairDeleteChunkSize+5)
	fb := &fakeRepairBudget{}
	repairIOBudget = fb
	deleteCandidatesInChunks(t.Context(), db2, recs2, func(int, int) {})
	require.Zero(t, sleepCalls.Load(), "fixed sleep must not run when budget paces")
	require.Greater(t, fb.total(), int64(0), "budget billed in its place")
	db2.Close()
	raw2.Close()
}

// TestDeleteCandidatesInChunks_DirSortedReclaims: file reclaims visit
// recordings grouped by directory (observable through billed FileSize
// sequence — sizes unique per recording).
func TestDeleteCandidatesInChunks_DirSortedReclaims(t *testing.T) {
	// Build 3 recordings in interleaved dirs with unique sizes.
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "mibee-nvr.db")
	db, err := storage.New(dbPath)
	require.NoError(t, err)
	require.NoError(t, db.Init(context.Background()))
	raw, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)

	old := time.Now().UTC().Add(-48 * time.Hour)
	ctx := context.Background()
	input := []struct {
		id, rel string
		size    int64
	}{
		{"a1", "cam1/h10/a1.mp4", 1001},
		{"b1", "cam1/h11/b1.mp4", 2002},
		{"a2", "cam1/h10/a2.mp4", 3003},
	}
	var recs []model.Recording
	for _, in := range input {
		f := filepath.Join(dir, in.rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(f), 0o755))
		require.NoError(t, os.WriteFile(f, make([]byte, in.size), 0o644))
		r := repairRec(in.id, "cam1", "h264", model.MergeStatusPending, old)
		r.FilePath = f
		r.FileSize = in.size
		require.NoError(t, db.InsertRecording(ctx, r))
		recs = append(recs, *r)
	}

	fb := &fakeRepairBudget{}
	prev := repairIOBudget
	repairIOBudget = fb
	t.Cleanup(func() { repairIOBudget = prev })

	deleteCandidatesInChunks(t.Context(), db, recs, func(int, int) {})

	billed := fb.amounts()
	require.Len(t, billed, 3)
	pair := func(a, b int64) bool {
		return (billed[0] == a && billed[1] == b) || (billed[0] == b && billed[1] == a)
	}
	require.True(t, pair(1001, 3003),
		"reclaim order %v not directory-grouped (h10 pair must be adjacent)", billed)
	db.Close()
	raw.Close()
}

// amounts returns billed amounts in call order.
func (f *fakeRepairBudget) amounts() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]int64, len(f.calls))
	copy(out, f.calls)
	return out
}
