package storage

// Long-tail DB coverage (#599): PTZ preset CRUD, batched recording queries,
// mergeable/rolling/pending listings, zero-duration repair family, expired
// and archive partitions, migration capacity queries, and the pure helpers
// (chunkIDs, TranscodeTask.MarshalJSON). All against per-test temp SQLite.

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/stretchr/testify/require"
)

func TestPTZPresetCRUDAndTokenAllocation(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := context.Background()

	// Insert is INSERT OR IGNORE: the first name for a token wins.
	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, db.InsertPTZPreset(ctx, PTZPreset{CameraID: "cam", Token: "1", Name: "first", CreatedAt: now}))
	require.NoError(t, db.InsertPTZPreset(ctx, PTZPreset{CameraID: "cam", Token: "1", Name: "ignored", CreatedAt: now}))

	// Upsert renames.
	require.NoError(t, db.UpsertPTZPreset(ctx, PTZPreset{CameraID: "cam", Token: "1", Name: "renamed", CreatedAt: now}))
	require.NoError(t, db.UpsertPTZPreset(ctx, PTZPreset{CameraID: "cam", Token: "10", Name: "ten", CreatedAt: now}))
	require.NoError(t, db.UpsertPTZPreset(ctx, PTZPreset{CameraID: "cam", Token: "2", Name: "two", CreatedAt: now}))
	require.NoError(t, db.UpsertPTZPreset(ctx, PTZPreset{CameraID: "other", Token: "1", Name: "othercam", CreatedAt: now}))

	// List is per-camera, numerically ordered.
	rows, err := db.ListPTZPresets(ctx, "cam")
	require.NoError(t, err)
	require.Len(t, rows, 3)
	require.Equal(t, []string{"1", "2", "10"}, []string{rows[0].Token, rows[1].Token, rows[2].Token})
	require.Equal(t, "renamed", rows[0].Name, "upsert must rename")
	require.False(t, rows[0].CreatedAt.IsZero(), "created_at round-trips")

	// Token allocation: lowest free slot, per camera.
	tok, ok := db.NextPTZPresetToken(ctx, "cam")
	require.True(t, ok)
	require.Equal(t, "3", tok)
	tok, ok = db.NextPTZPresetToken(ctx, "fresh")
	require.True(t, ok)
	require.Equal(t, "1", tok)

	// Exhausted: fill 3..255 except the already-taken 10 → false.
	for n := 3; n <= 255; n++ {
		if n == 10 {
			continue
		}
		require.NoError(t, db.UpsertPTZPreset(ctx, PTZPreset{CameraID: "cam", Token: itoa(n), Name: "x", CreatedAt: now}))
	}
	_, ok = db.NextPTZPresetToken(ctx, "cam")
	require.False(t, ok, "all 255 slots taken")

	// Delete is idempotent.
	require.NoError(t, db.DeletePTZPreset(ctx, "cam", "10"))
	require.NoError(t, db.DeletePTZPreset(ctx, "cam", "10"))
	rows, err = db.ListPTZPresets(ctx, "cam")
	require.NoError(t, err)
	require.Len(t, rows, 254, "255 slots minus the deleted token 10")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func TestRecordingsByIDBatchAndDeleteBatch(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	for _, id := range []string{"r1", "r2", "r3"} {
		require.NoError(t, db.InsertRecording(ctx, &model.Recording{
			ID: id, CameraID: "cam", Format: "h264", FilePath: "/tmp/" + id,
			StartedAt: now.Add(-time.Hour), EndedAt: now, MergeStatus: "pending",
		}))
	}

	// Batch get: mixed hit/miss (no ORDER BY — assert as a set), empty input → nil,nil.
	got, err := db.GetRecordingsByIDBatch(ctx, []string{"r2", "missing", "r1"})
	require.NoError(t, err)
	require.Len(t, got, 2)
	ids := []string{got[0].ID, got[1].ID}
	require.ElementsMatch(t, []string{"r1", "r2"}, ids)
	for _, g := range got {
		require.Equal(t, model.MergeStatusPending, g.MergeStatus, "empty merge_status normalizes to pending")
	}
	got, err = db.GetRecordingsByIDBatch(ctx, nil)
	require.NoError(t, err)
	require.Nil(t, got)

	// Batch delete: returns deleted ids; idempotent misses → nil.
	del, err := db.DeleteRecordingsBatch(ctx, []string{"r1", "r3"})
	require.NoError(t, err)
	require.Equal(t, []string{"r1", "r3"}, del)
	del, err = db.DeleteRecordingsBatch(ctx, []string{"r1"})
	require.NoError(t, err)
	require.Nil(t, del)
	del, err = db.DeleteRecordingsBatch(ctx, nil)
	require.NoError(t, err)
	require.Nil(t, del)
}

// seedRec inserts a completed recording with explicit fields for query tests.
func seedLongRec(t *testing.T, db *DB, id, cam, format, mergeStatus, path string, started time.Time) {
	t.Helper()
	require.NoError(t, db.InsertRecording(context.Background(), &model.Recording{
		ID: id, CameraID: cam, Format: model.Format(format), FilePath: path,
		StartedAt: started, EndedAt: started.Add(30 * time.Second),
		FileSize: 2048, MergeStatus: mergeStatus,
	}))
}

func TestMergeableAndRollingQueries(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := context.Background()
	base := time.Date(2025, 6, 1, 10, 0, 0, 0, time.UTC)

	seedLongRec(t, db, "in-win", "cam", "h264", "pending", "/a", base)
	seedLongRec(t, db, "merged", "cam", "h264", "merged", "/b", base.Add(time.Minute))
	seedLongRec(t, db, "failed", "cam", "h264", "failed", "/c", base.Add(2*time.Minute))
	seedLongRec(t, db, "out-win", "cam", "h264", "pending", "/d", base.Add(10*time.Minute))
	seedLongRec(t, db, "tl", "cam", "timelapse", "pending", "/e", base.Add(3*time.Minute))
	seedLongRec(t, db, "other-cam", "cam2", "h264", "pending", "/f", base.Add(4*time.Minute))

	// Mergeable window query: pending and inside [base, base+5m). The window
	// query itself carries no format filter — the timelapse row inside the
	// window is returned too (callers filter by format).
	segs, err := db.ListMergeableSegments(ctx, "cam", base, base.Add(5*time.Minute))
	require.NoError(t, err)
	require.Len(t, segs, 2)
	require.Equal(t, "in-win", segs[0].ID)

	// Rolling query: pending, non-timelapse; includeFailed adds 'failed';
	// camera filter; limit; since.
	segs, err = db.ListPendingSegmentsForRolling(ctx, "cam", false, 0, time.Time{})
	require.NoError(t, err)
	require.Len(t, segs, 2, "pending h264 only (timelapse excluded)")
	segs, err = db.ListPendingSegmentsForRolling(ctx, "cam", true, 0, time.Time{})
	require.NoError(t, err)
	require.Len(t, segs, 3, "includeFailed pulls the failed segment")
	segs, err = db.ListPendingSegmentsForRolling(ctx, "", false, 2, base.Add(90*time.Second))
	require.NoError(t, err)
	require.Len(t, segs, 2, "all cameras, bounded by limit and since")
	for _, s := range segs {
		require.False(t, s.StartedAt.Before(base.Add(90*time.Second)))
	}
}

func TestZeroDurationRepairFamily(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// Repair candidate: duration 0, >1MB, non-mjpeg, pending, ended.
	require.NoError(t, db.InsertRecording(ctx, &model.Recording{
		ID: "broken", CameraID: "cam", Format: "h264", FilePath: "/tmp/broken",
		StartedAt: now.Add(-time.Hour), EndedAt: now, FileSize: 2 << 20, MergeStatus: "pending",
	}))
	// Not candidates: tiny file / merged status.
	require.NoError(t, db.InsertRecording(ctx, &model.Recording{
		ID: "tiny", CameraID: "cam", Format: "h264", FilePath: "/tmp/tiny",
		StartedAt: now.Add(-time.Hour), EndedAt: now, MergeStatus: "pending",
	}))
	require.NoError(t, db.InsertRecording(ctx, &model.Recording{
		ID: "merged", CameraID: "cam", Format: "h264", FilePath: "/tmp/m",
		StartedAt: now.Add(-time.Hour), EndedAt: now, FileSize: 2 << 20, MergeStatus: "merged",
	}))

	broken, err := db.RepairZeroDurationRecordings(ctx)
	require.NoError(t, err)
	require.Len(t, broken, 1)
	require.Equal(t, "broken", broken[0].ID)

	zero, err := db.ListZeroDurationRecordings(ctx, "cam", 10)
	require.NoError(t, err)
	require.Len(t, zero, 3, "List is broader than Repair (tiny + merged-status rows count)")
	zeroAll, err := db.ListZeroDurationRecordings(ctx, "", 0)
	require.NoError(t, err)
	require.Len(t, zeroAll, 3)

	// UpdateRecordingDuration fixes it.
	require.NoError(t, db.UpdateRecordingDuration(ctx, "broken", 3600, now))
	fixed, err := db.ListZeroDurationRecordings(ctx, "cam", 0)
	require.NoError(t, err)
	require.Len(t, fixed, 2)
	for _, f := range fixed {
		require.NotEqual(t, "broken", f.ID)
	}
}

func TestExpiredRecordingsPartitions(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	old := now.Add(-48 * time.Hour)

	ins := func(id, cam string, ended time.Time, archived bool, size int64) {
		r := &model.Recording{
			ID: id, CameraID: cam, Format: "h264", FilePath: "/tmp/" + id,
			StartedAt: ended.Add(-time.Minute), EndedAt: ended, FileSize: size,
		}
		require.NoError(t, db.InsertRecording(ctx, r))
		if archived {
			_, err := db.db.ExecContext(ctx, `UPDATE recordings SET archived=1 WHERE id=?`, id)
			require.NoError(t, err)
		}
	}
	ins("old-a", "camA", old, false, 100)
	ins("old-arc", "camA", old, true, 100)
	ins("new-a", "camA", now, false, 100)
	ins("old-b", "camB", old, false, 100)

	// Per-camera expired, unarchived partition.
	rows, err := db.ListExpiredRecordingsByCamera(ctx, "camA", 1)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "old-a", rows[0].ID)

	// Archived partition.
	rows, err = db.ListExpiredArchivedRecordingsByCamera(ctx, "camA", 1)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "old-arc", rows[0].ID)
}

func TestMigrationCapacityQueries(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	seedLongRec(t, db, "a", "camA", "h264", "merged", "/mnt/data/nvr/a.mp4", now)
	seedLongRec(t, db, "b", "camA", "h264", "merged", "/mnt/data/nvr/b.mp4", now)
	seedLongRec(t, db, "c", "camB", "h264", "merged", "/other/c.mp4", now)

	ids, err := db.ListCameraIDs(ctx)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"camA", "camB"}, ids)

	// Bytes outside the keep-under prefix only.
	n, err := db.SumMigratableBytes(ctx, "camA", "/mnt/data/nvr")
	require.NoError(t, err)
	require.Zero(t, n)
	n, err = db.SumMigratableBytes(ctx, "camB", "/mnt/data/nvr")
	require.NoError(t, err)
	require.EqualValues(t, 2048, n)

	// Path set for one camera.
	paths, err := db.GetRecordingPathsByCamera(ctx, "camA")
	require.NoError(t, err)
	require.Len(t, paths, 2)
	require.True(t, paths["/mnt/data/nvr/a.mp4"])
}

func TestPendingMJPEGRecordings(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	seedLongRec(t, db, "mj", "cam", "mjpeg", "pending", "/p/mj", now)
	seedLongRec(t, db, "jp", "cam", "jpeg", "pending", "/p/jp", now)
	seedLongRec(t, db, "h", "cam", "h264", "pending", "/p/h", now)
	seedLongRec(t, db, "mj-done", "cam", "mjpeg", "merged", "/p/md", now)
	seedLongRec(t, db, "mj-other", "cam2", "mjpeg", "pending", "/p/mo", now)

	rows, err := db.ListPendingMJPEGRecordings(ctx, "cam")
	require.NoError(t, err)
	require.Len(t, rows, 2)
}

func TestInsertRecordingWithRetryFastFail(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// Non-busy errors surface immediately (no retry sleep).
	err := db.InsertRecordingWithRetry(ctx, &model.Recording{ID: "", CameraID: "cam"}, 3, time.Millisecond)
	require.Error(t, err)

	// Success path on the first attempt.
	require.NoError(t, db.InsertRecordingWithRetry(ctx, &model.Recording{
		ID: "ok", CameraID: "cam", Format: "h264", FilePath: "/p",
		StartedAt: now.Add(-time.Minute), EndedAt: now,
	}, 3, time.Millisecond))
}

func TestCameraExistsByOnvifEndpointBranches(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := context.Background()

	// Neither endpoint nor serial → false without querying.
	ok, err := db.CameraExistsByOnvifEndpoint(ctx, "", "")
	require.NoError(t, err)
	require.False(t, ok)

	// Endpoint match.
	require.NoError(t, db.UpsertCamera(ctx, "c1", "C1", "onvif", "", "", "", "", "http://10.0.0.5/onvif/device_service", "", "", ""))
	ok, err = db.CameraExistsByOnvifEndpoint(ctx, "http://10.0.0.5/onvif/device_service", "")
	require.NoError(t, err)
	require.True(t, ok)

	// Serial match via serial_number / stable_id columns.
	require.NoError(t, db.UpsertCamera(ctx, "c2", "C2", "onvif", "", "", "", "", "", "", "", "SN-ZZZ"))
	ok, err = db.CameraExistsByOnvifEndpoint(ctx, "", "SN-ZZZ")
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = db.CameraExistsByOnvifEndpoint(ctx, "", "SN-NOPE")
	require.NoError(t, err)
	require.False(t, ok)
}

func TestChunkIDs(t *testing.T) {
	t.Parallel()
	require.Nil(t, chunkIDs(nil, 10))
	require.Nil(t, chunkIDs([]string{"a"}, 0))
	require.Equal(t, [][]string{{"a"}}, chunkIDs([]string{"a"}, 10))
	require.Equal(t,
		[][]string{{"a", "b"}, {"c"}},
		chunkIDs([]string{"a", "b", "c"}, 2))
}

func TestTranscodeTaskMarshalJSON(t *testing.T) {
	t.Parallel()
	valid := sql.NullString{String: "boom", Valid: true}
	task := TranscodeTask{
		ID: 7, CameraID: "cam", RecordingID: "rec", Status: "failed",
		Error: valid, StartedAt: sql.NullString{String: "2025-01-01", Valid: true},
	}
	b, err := json.Marshal(task)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))
	require.Equal(t, "boom", m["error"], "valid NullString marshals as the raw string")
	require.Equal(t, "2025-01-01", m["started_at"])
	require.NotContains(t, string(b), "String", "sql.NullString struct shape must not leak")

	b, err = json.Marshal(TranscodeTask{ID: 1})
	require.NoError(t, err)
	require.Contains(t, string(b), `"error":null`, "invalid NullString marshals as null")
}
