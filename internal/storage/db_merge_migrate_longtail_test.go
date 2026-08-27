package storage

// Long-tail coverage for merge validation/rollback, path migration, and the
// DB maintenance + retry surface (#580). Includes regression tests for the
// UTC cutoff fix in ListCameraMergeWindows / ListSingletonPendingRecordings
// (same class as #565: local-time cutoff vs UTC-stored ended_at).

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// setMergeState backdates/overrides a recording's merge columns directly —
// the SQL-backdating pattern from #571 (never rely on the test running fast).
func setMergeState(t *testing.T, db *DB, id, status, mergePath string, endedAgo time.Duration) {
	t.Helper()
	_, err := db.db.ExecContext(context.Background(),
		`UPDATE recordings SET merge_status=?, merge_path=?, ended_at=? WHERE id=?`,
		status, mergePath, time.Now().UTC().Add(-endedAgo).Format("2006-01-02 15:04:05.999999999"), id)
	require.NoError(t, err)
}

func TestMergedValidationAndReset(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	seedRec(t, db, &recSeed{id: "mv-1", camera: "cam-1", format: "h264", started: now.Add(-2 * time.Hour)})
	seedRec(t, db, &recSeed{id: "mv-2", camera: "cam-1", format: "h264", started: now.Add(-1 * time.Hour)})
	seedRec(t, db, &recSeed{id: "mv-3", camera: "cam-1", format: "h264", started: now})

	setMergeState(t, db, "mv-1", "merged", "/merged/mv-1.mp4", time.Hour)
	setMergeState(t, db, "mv-2", "merged", "", time.Hour) // no merge_path → excluded
	setMergeState(t, db, "mv-3", "pending", "", time.Hour)

	rows, err := db.ListMergedRecordingsForValidation(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "mv-1", rows[0].ID)
	require.Equal(t, "/merged/mv-1.mp4", rows[0].MergePath)

	require.NoError(t, db.ResetMergeStatus(ctx, "mv-1"))
	rows, err = db.ListMergedRecordingsForValidation(ctx)
	require.NoError(t, err)
	require.Empty(t, rows)
}

func TestMergeProgressBatchAndClearMergePath(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	var ids []string
	for i := range 5 {
		id := "bp-" + string(rune('a'+i))
		ids = append(ids, id)
		seedRec(t, db, &recSeed{id: id, camera: "cam-1", format: "h264", started: now.Add(-time.Duration(i+1) * time.Hour)})
	}

	require.NoError(t, db.UpdateMergeProgressBatch(ctx, ids, 42))
	require.NoError(t, db.ClearMergePathBatch(ctx, ids))

	// Failed-status reset round trip.
	require.NoError(t, db.SetMergeError(ctx, ids, "boom"))
	n, err := db.ResetFailedMergeStatus(ctx, ids)
	require.NoError(t, err)
	require.Equal(t, int64(len(ids)), n)
}

func TestShortMergedAndSingletonAndWindows(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// Three pending segments in the same hour, older than minAge.
	base := now.Add(-3 * time.Hour).Truncate(time.Hour)
	for i := range 3 {
		seedRec(t, db, &recSeed{
			id:      "sw-" + string(rune('a'+i)),
			camera:  "cam-1",
			format:  "h264",
			started: base.Add(time.Duration(i) * 10 * time.Minute),
			ended:   base.Add(time.Duration(i)*10*time.Minute + 5*time.Minute),
		})
	}
	// A merged-but-short recording (merge_quality='short' drives the listing).
	seedRec(t, db, &recSeed{id: "sw-short", camera: "cam-1", format: "h264", started: base.Add(-time.Hour)})
	setMergeState(t, db, "sw-short", "merged", "/merged/sw-short.mp4", 2*time.Hour)
	_, err := db.db.ExecContext(ctx, "UPDATE recordings SET merge_quality='short' WHERE id='sw-short'")
	require.NoError(t, err)

	windows, err := db.ListCameraMergeWindows(ctx, "cam-1", time.Hour)
	require.NoError(t, err)
	require.Len(t, windows, 1)
	require.Equal(t, 3, windows[0].SegmentCount)

	singles, err := db.ListSingletonPendingRecordings(ctx, "cam-1", time.Hour)
	require.NoError(t, err)
	require.Empty(t, singles, "three same-hour segments form a window, none is a singleton")

	shorts, err := db.ListShortMergedRecordings(ctx, "cam-1", 300)
	require.NoError(t, err)
	require.Len(t, shorts, 1)
	require.Equal(t, "sw-short", shorts[0].ID)
}

func TestMergeWindowMinAgeRespectsUTC(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	// Hour-boundary-proof fixtures: anchor on the current UTC hour so the two
	// segments always land in the SAME hour bucket and their ages are
	// deterministic regardless of when in the hour the test runs.
	// endB = anchor-2h is 2-3h old; endA = anchor-2h30m is 2.5-3.5h old.
	anchor := time.Now().UTC().Truncate(time.Hour)
	endA := anchor.Add(-150 * time.Minute)
	endB := anchor.Add(-120 * time.Minute)
	seedRec(t, db, &recSeed{id: "fresh-1", camera: "cam-1", format: "h264", started: endA.Add(-10 * time.Minute), ended: endA})
	seedRec(t, db, &recSeed{id: "fresh-2", camera: "cam-1", format: "h264", started: endB.Add(-10 * time.Minute), ended: endB})

	// minAge=3h: both segments are younger than the gate — must NOT be
	// eligible. On a UTC+N host a local-time cutoff reads as up to N hours
	// later and would wrongly include them (regression test for the
	// #565-class cutoff bug in ListCameraMergeWindows/ListSingletonPendingRecordings).
	windows, err := db.ListCameraMergeWindows(ctx, "cam-1", 3*time.Hour)
	require.NoError(t, err)
	require.Empty(t, windows, "window younger than minAge must not be eligible regardless of host timezone")

	// Same segments with minAge=1h become eligible.
	windows, err = db.ListCameraMergeWindows(ctx, "cam-1", time.Hour)
	require.NoError(t, err)
	require.Len(t, windows, 1)
}

func TestPathMigrationOps(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	oldRoot := "/mnt/old"
	newRoot := "/mnt/new"
	seedRec(t, db, &recSeed{id: "pm-1", camera: "cam-1", format: "h264", started: now})
	seedRec(t, db, &recSeed{id: "pm-2", camera: "cam-1", format: "h264", started: now})
	_, err := db.db.ExecContext(ctx, "UPDATE recordings SET file_path=?, merge_path=? WHERE id='pm-1'",
		oldRoot+"/cam-1/pm-1.mp4", oldRoot+"/cam-1/pm-1-merged.mp4")
	require.NoError(t, err)
	_, err = db.db.ExecContext(ctx, "UPDATE recordings SET file_path=? WHERE id='pm-2'",
		newRoot+"/cam-1/pm-2.mp4")
	require.NoError(t, err)

	// Migratable = rows with any path still under the old root.
	migs, err := db.ListMigratableRecordings(ctx, "cam-1", newRoot)
	require.NoError(t, err)
	require.Len(t, migs, 1)
	require.Equal(t, "pm-1", migs[0].ID)
	require.Equal(t, int64(0), migs[0].FileSize)

	// Prefix counting and distinct top segments under a root.
	n, err := db.CountPathPrefix(ctx, oldRoot)
	require.NoError(t, err)
	require.Equal(t, int64(1), n)

	tops, err := db.DistinctTopSegments(ctx, oldRoot)
	require.NoError(t, err)
	require.Len(t, tops, 1)

	// Rewrite prefix moves the rows.
	moved, err := db.RewritePathPrefix(ctx, oldRoot, newRoot)
	require.NoError(t, err)
	require.Equal(t, int64(2), moved)

	n, err = db.CountPathPrefix(ctx, oldRoot)
	require.NoError(t, err)
	require.Equal(t, int64(0), n)

	// Single-row rewrite helper.
	require.NoError(t, db.RewriteRecordingPaths(ctx, "pm-2", "/mnt/third/cam-1/pm-2.mp4", sql.NullString{String: "/mnt/third/cam-1/pm-2-merged.mp4", Valid: true}))
}

func TestDBMaintenanceSurface(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	// Path / pools / metrics wiring are trivial but uncovered.
	require.NotEmpty(t, db.Path())
	db.SetReadPoolSize(2)
	_, ok := db.ReadPoolStats()
	require.True(t, ok)
	db.SetMetrics(nil) // must not panic

	// VACUUM-based backup produces a usable second file.
	dest := filepath.Join(t.TempDir(), "backup.db")
	require.NoError(t, db.Backup(ctx, dest))
	require.FileExists(t, dest)

	require.NoError(t, db.Optimize(ctx))

	// Online compaction returns the size delta and leaves the DB usable.
	seedRec(t, db, &recSeed{id: "cp-1", camera: "cam-1", format: "h264", started: time.Now().UTC()})
	_, err := db.CompactOnline(ctx)
	require.NoError(t, err) // size delta can be negative on tiny DBs (page rounding)
	rec, err := db.GetRecording(ctx, "cp-1")
	require.NoError(t, err)
	require.NotNil(t, rec)
}

func TestRetryOnBusy(t *testing.T) {
	t.Parallel()

	require.False(t, IsBusyError(nil))
	require.False(t, IsBusyError(sql.ErrNoRows))
	require.True(t, IsBusyError(errBusyForTest))

	var calls atomic.Int32
	hookFired := atomic.Bool{}
	SetBusyErrorHook(func() { hookFired.Store(true) })
	t.Cleanup(func() { SetBusyErrorHook(nil) })

	// Non-busy error surfaces immediately after the configured attempts.
	err := RetryOnBusy(context.Background(), func() error {
		calls.Add(1)
		return sql.ErrConnDone
	})
	require.ErrorIs(t, err, sql.ErrConnDone)
	require.Equal(t, int32(1), calls.Load())
	require.False(t, hookFired.Load())

	// Busy error retries until it clears.
	calls.Store(0)
	require.NoError(t, RetryOnBusy(context.Background(), func() error {
		if calls.Add(1) < 3 {
			return errBusyForTest
		}
		return nil
	}))
	require.Equal(t, int32(3), calls.Load())
	require.True(t, hookFired.Load())

	// Cancelled context aborts the retry loop.
	require.Error(t, RetryOnBusy(t.Context(), func() error { return errBusyForTest }))
}

// errBusyForTest satisfies IsBusyError via the sqlite-busy message match.
var errBusyForTest = &busyFakeError{msg: "database is busy"}

type busyFakeError struct{ msg string }

func (e *busyFakeError) Error() string { return e.msg }
