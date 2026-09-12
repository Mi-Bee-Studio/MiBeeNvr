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
