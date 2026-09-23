package storage

// db_offload_test.go — offload outbox state machine (issue #874 batch 1).
// The outbox is the single source of truth for 段→对象 progression
// (pending → uploading → uploaded → evicted, plus terminal skipped);
// crash recovery and stale re-upload semantics live here.

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newOffloadTestDB opens an initialized DB in a temp dir.
func newOffloadTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := New(filepath.Join(t.TempDir(), "offload.db"))
	require.NoError(t, err)
	require.NoError(t, db.Init(context.Background()))
	t.Cleanup(func() { db.Close() })
	return db
}

// insertMergedRecording inserts a merged recording row (the upload candidate
// shape) and returns it. tier/mergePath override the merge_tier column when
// non-empty ("go" simulates a batch-merged row).
func insertMergedRecording(t *testing.T, db *DB, id, cameraID string, endedAgo time.Duration, size int64) *model.Recording {
	t.Helper()
	ended := time.Now().UTC().Add(-endedAgo)
	rec := &model.Recording{
		ID: id, CameraID: cameraID, FilePath: "/data/" + cameraID + "/" + id + ".mp4",
		Format: model.FormatH264, StartedAt: ended.Add(-time.Hour), EndedAt: ended,
		Duration: 3600, FileSize: size, MergeStatus: model.MergeStatusMerged,
		MergeTier: "rolling", FrameCount: 90000,
	}
	require.NoError(t, db.InsertRecording(context.Background(), rec))
	return rec
}

func TestOffloadOutboxLifecycle(t *testing.T) {
	db := newOffloadTestDB(t)
	ctx := context.Background()

	rec := insertMergedRecording(t, db, "merge-1", "camA", 2*time.Hour, 1024)

	// Enqueue is idempotent per recording: second call reports false.
	inserted, err := db.EnqueueOffload(ctx, OffloadItem{
		RecordingID: rec.ID, CameraID: rec.CameraID, ObjectKey: "recordings/camA/2026/merge-1.mp4",
		LocalPath: rec.FilePath, FileSize: rec.FileSize,
	})
	require.NoError(t, err)
	assert.True(t, inserted)
	inserted, err = db.EnqueueOffload(ctx, OffloadItem{
		RecordingID: rec.ID, CameraID: rec.CameraID, ObjectKey: "recordings/camA/2026/merge-1.mp4",
		LocalPath: rec.FilePath, FileSize: rec.FileSize,
	})
	require.NoError(t, err)
	assert.False(t, inserted, "duplicate enqueue is a no-op")

	// Claim transitions pending → uploading and returns the item.
	items, err := db.ClaimPendingOffload(ctx, 10)
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, OffloadStatusUploading, items[0].Status)
	assert.Equal(t, "recordings/camA/2026/merge-1.mp4", items[0].ObjectKey)

	// Nothing left to claim.
	again, err := db.ClaimPendingOffload(ctx, 10)
	require.NoError(t, err)
	assert.Empty(t, again)

	// Uploaded records etag + size + timestamp.
	require.NoError(t, db.MarkOffloadUploaded(ctx, items[0].ID, `"abc123"`, 1024))
	counts, err := db.CountOffloadByStatus(ctx)
	require.NoError(t, err)
	assert.Equal(t, map[string]int{OffloadStatusUploaded: 1}, counts)

	// Evictable once the confirmation timestamp has passed.
	evictable, err := db.ListOffloadEvictable(ctx, time.Now().UTC().Add(time.Minute), 10)
	require.NoError(t, err)
	require.Len(t, evictable, 1)
	assert.Equal(t, rec.ID, evictable[0].RecordingID)

	require.NoError(t, db.MarkOffloadEvicted(ctx, items[0].ID))
	counts, err = db.CountOffloadByStatus(ctx)
	require.NoError(t, err)
	assert.Equal(t, map[string]int{OffloadStatusEvicted: 1}, counts)
}

func TestOffloadClaimRespectsLimitAndOrder(t *testing.T) {
	db := newOffloadTestDB(t)
	ctx := context.Background()

	for i := range 5 {
		rec := insertMergedRecording(t, db, "m"+string(rune('a'+i)), "camA", time.Duration(i+1)*time.Hour, 100)
		_, err := db.EnqueueOffload(ctx, OffloadItem{
			RecordingID: rec.ID, CameraID: rec.CameraID, ObjectKey: "k" + rec.ID,
			LocalPath: rec.FilePath, FileSize: rec.FileSize,
		})
		require.NoError(t, err)
	}
	items, err := db.ClaimPendingOffload(ctx, 3)
	require.NoError(t, err)
	require.Len(t, items, 3)
	// FIFO by insertion id.
	assert.Equal(t, "ma", items[0].RecordingID)
	assert.Equal(t, "mb", items[1].RecordingID)
	assert.Equal(t, "mc", items[2].RecordingID)
}

func TestOffloadRetryAndRecovery(t *testing.T) {
	db := newOffloadTestDB(t)
	ctx := context.Background()

	rec := insertMergedRecording(t, db, "merge-r", "camA", 2*time.Hour, 512)
	_, err := db.EnqueueOffload(ctx, OffloadItem{
		RecordingID: rec.ID, CameraID: rec.CameraID, ObjectKey: "k1",
		LocalPath: rec.FilePath, FileSize: rec.FileSize,
	})
	require.NoError(t, err)
	items, err := db.ClaimPendingOffload(ctx, 1)
	require.NoError(t, err)
	require.Len(t, items, 1)

	// Failed upload returns the row to pending with attempts bumped.
	require.NoError(t, db.MarkOffloadRetry(ctx, items[0].ID, "connection reset"))
	pending, err := db.ClaimPendingOffload(ctx, 1)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Equal(t, 1, pending[0].Attempts)
	assert.Contains(t, pending[0].LastError, "connection reset")

	// Crash recovery: a row stranded in uploading goes back to pending.
	_, err = db.ClaimPendingOffload(ctx, 1)
	require.NoError(t, err)
	requeued, err := db.RequeueUploadingOffload(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), requeued)
	counts, err := db.CountOffloadByStatus(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, counts[OffloadStatusPending])
}

func TestOffloadStaleRequeueWhenFileGrew(t *testing.T) {
	db := newOffloadTestDB(t)
	ctx := context.Background()

	// Uploaded when the file was 512 bytes...
	rec := insertMergedRecording(t, db, "merge-s", "camA", 2*time.Hour, 512)
	_, err := db.EnqueueOffload(ctx, OffloadItem{
		RecordingID: rec.ID, CameraID: rec.CameraID, ObjectKey: "k2",
		LocalPath: rec.FilePath, FileSize: 512,
	})
	require.NoError(t, err)
	items, err := db.ClaimPendingOffload(ctx, 1)
	require.NoError(t, err)
	require.NoError(t, db.MarkOffloadUploaded(ctx, items[0].ID, `"e"`, 512))

	// ...then a late backfill append grew the recording row.
	grown, err := db.GetRecording(ctx, rec.ID)
	require.NoError(t, err)
	grown.FileSize = 2048
	require.NoError(t, db.UpdateRecording(ctx, grown))

	n, err := db.RequeueStaleUploadedOffload(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n, "grown file must be re-uploaded")
	counts, err := db.CountOffloadByStatus(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, counts[OffloadStatusPending])
}

func TestListOffloadCandidates(t *testing.T) {
	db := newOffloadTestDB(t)
	ctx := context.Background()
	cutoff := time.Now().UTC().Add(-30 * time.Minute)

	// Eligible: rolling-merged, closed 2h ago, not archived.
	eligible := insertMergedRecording(t, db, "cand-yes", "camA", 2*time.Hour, 10)
	// Too fresh: closed 5m ago.
	fresh := insertMergedRecording(t, db, "cand-fresh", "camA", 5*time.Minute, 10)
	// Pending raw segment (not merged).
	raw := &model.Recording{
		ID: "cand-raw", CameraID: "camA", FilePath: "/raw.mp4",
		Format: model.FormatH264, StartedAt: time.Now().UTC().Add(-3 * time.Hour), FileSize: 5,
	}
	require.NoError(t, db.InsertRecording(ctx, raw))
	// Batch-merged segment row (merge_tier set, but tier=go via SetMergeResult with path).
	batchMerged := insertMergedRecording(t, db, "cand-batch", "camA", 2*time.Hour, 10)
	require.NoError(t, db.SetMergeResult(ctx, batchMerged.ID, "/merged-out.mp4", "go"))
	// Already enqueued.
	done := insertMergedRecording(t, db, "cand-done", "camA", 2*time.Hour, 10)
	_, err := db.EnqueueOffload(ctx, OffloadItem{RecordingID: done.ID, CameraID: "camA", ObjectKey: "k", LocalPath: done.FilePath})
	require.NoError(t, err)

	cands, err := db.ListOffloadCandidates(ctx, cutoff, 100)
	require.NoError(t, err)
	ids := make([]string, 0, len(cands))
	for _, c := range cands {
		ids = append(ids, c.RecordingID)
	}
	assert.ElementsMatch(t, []string{eligible.ID}, ids,
		"only closed rolling-merged rows not yet in the outbox are candidates; fresh/pending/batch-merged(N:1 artifact)/already-enqueued excluded")
	for _, c := range cands {
		assert.Equal(t, "camA", c.CameraID)
		assert.NotEmpty(t, c.FilePath)
		assert.True(t, c.StartedAt.Year() > 2020)
	}
	_ = fresh
	_ = batchMerged
}

func TestOffloadBacklogCount(t *testing.T) {
	db := newOffloadTestDB(t)
	ctx := context.Background()

	// One row per terminal/transient shape: pending, uploaded, evicted.
	stayPending := insertMergedRecording(t, db, "bc-pending", "camA", time.Hour, 10)
	_, err := db.EnqueueOffload(ctx, OffloadItem{RecordingID: stayPending.ID, CameraID: "camA", ObjectKey: "bk-p", LocalPath: stayPending.FilePath})
	require.NoError(t, err) // never claimed → stays pending

	uploaded := insertMergedRecording(t, db, "bc-up", "camA", time.Hour, 10)
	_, err = db.EnqueueOffload(ctx, OffloadItem{RecordingID: uploaded.ID, CameraID: "camA", ObjectKey: "bk-u", LocalPath: uploaded.FilePath})
	require.NoError(t, err)
	items, err := db.ClaimPendingOffload(ctx, 1)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.NoError(t, db.MarkOffloadUploaded(ctx, items[0].ID, `"e"`, 10))

	evicted := insertMergedRecording(t, db, "bc-ev", "camA", time.Hour, 10)
	_, err = db.EnqueueOffload(ctx, OffloadItem{RecordingID: evicted.ID, CameraID: "camA", ObjectKey: "bk-e", LocalPath: evicted.FilePath})
	require.NoError(t, err)
	items, err = db.ClaimPendingOffload(ctx, 1)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.NoError(t, db.MarkOffloadUploaded(ctx, items[0].ID, `"e"`, 10))
	require.NoError(t, db.MarkOffloadEvicted(ctx, items[0].ID))

	backlog, err := db.CountOffloadBacklog(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, backlog, "only pending+uploading counts as backlog")
}

func TestOffloadItemMetadataPersisted(t *testing.T) {
	db := newOffloadTestDB(t)
	ctx := context.Background()

	rec := insertMergedRecording(t, db, "meta-1", "camA", 2*time.Hour, 42)
	inserted, err := db.EnqueueOffload(ctx, OffloadItem{
		RecordingID: rec.ID, CameraID: "camA", ObjectKey: "k-meta",
		LocalPath: rec.FilePath, FileSize: rec.FileSize,
		StartedAt: rec.StartedAt, EndedAt: rec.EndedAt,
		Duration: rec.Duration, Format: string(rec.Format),
	})
	require.NoError(t, err)
	require.True(t, inserted)

	items, err := db.ListOffloadRemote(ctx, OffloadRemoteFilter{Statuses: []string{OffloadStatusPending}}, 10, 0)
	require.NoError(t, err)
	// Listing is remote-status-scoped by default; pending not included under
	// the evicted-only default, so query explicitly.
	require.Len(t, items, 1)
	assert.Equal(t, rec.ID, items[0].RecordingID)
	assert.Equal(t, "h264", items[0].Format)
	assert.InDelta(t, 3600, items[0].Duration, 0.001)
	assert.WithinDuration(t, rec.StartedAt, items[0].StartedAt, time.Second)
	assert.WithinDuration(t, rec.EndedAt, items[0].EndedAt, time.Second)
}

func TestListOffloadRemoteEvictedOnly(t *testing.T) {
	db := newOffloadTestDB(t)
	ctx := context.Background()

	// One evicted (with metadata), one uploaded, one pending.
	evicted := insertMergedRecording(t, db, "r-ev", "camA", 3*time.Hour, 10)
	uploaded := insertMergedRecording(t, db, "r-up", "camA", 2*time.Hour, 10)
	pending := insertMergedRecording(t, db, "r-pend", "camB", 2*time.Hour, 10)
	for _, rec := range []*model.Recording{evicted, uploaded, pending} {
		_, err := db.EnqueueOffload(ctx, OffloadItem{
			RecordingID: rec.ID, CameraID: rec.CameraID, ObjectKey: "k-" + rec.ID,
			LocalPath: rec.FilePath, FileSize: rec.FileSize,
			StartedAt: rec.StartedAt, EndedAt: rec.EndedAt,
			Duration: rec.Duration, Format: string(rec.Format),
		})
		require.NoError(t, err)
	}
	evictedOutboxID := int64(-1)
	for _, rec := range []*model.Recording{evicted, uploaded} {
		items, err := db.ClaimPendingOffload(ctx, 1)
		require.NoError(t, err)
		require.Len(t, items, 1)
		require.NoError(t, db.MarkOffloadUploaded(ctx, items[0].ID, `"e"`, 10))
		if rec.ID == evicted.ID {
			evictedOutboxID = items[0].ID
		}
	}
	require.NotEqual(t, int64(-1), evictedOutboxID)
	require.NoError(t, db.MarkOffloadEvicted(ctx, evictedOutboxID))

	// Default listing: evicted only.
	remote, err := db.ListOffloadRemote(ctx, OffloadRemoteFilter{}, 10, 0)
	require.NoError(t, err)
	require.Len(t, remote, 1)
	assert.Equal(t, evicted.ID, remote[0].RecordingID)

	// Camera filter.
	remote, err = db.ListOffloadRemote(ctx, OffloadRemoteFilter{CameraID: "camB"}, 10, 0)
	require.NoError(t, err)
	assert.Empty(t, remote)

	// Metadata-less rows (pre-v41 leftovers) are excluded, not garbage rows.
	_, err = db.db.ExecContext(ctx, `UPDATE offload_outbox SET started_at='' WHERE recording_id=?`, evicted.ID)
	require.NoError(t, err)
	remote, err = db.ListOffloadRemote(ctx, OffloadRemoteFilter{}, 10, 0)
	require.NoError(t, err)
	assert.Empty(t, remote, "rows without metadata cannot be placed on a timeline — excluded")
}

func TestOffloadMetadataBackfill(t *testing.T) {
	db := newOffloadTestDB(t)
	ctx := context.Background()

	// A row enqueued WITHOUT metadata (batch-1 shape), recording row alive.
	rec := insertMergedRecording(t, db, "bf-1", "camA", 2*time.Hour, 10)
	_, err := db.EnqueueOffload(ctx, OffloadItem{
		RecordingID: rec.ID, CameraID: "camA", ObjectKey: "k-bf",
		LocalPath: rec.FilePath, FileSize: rec.FileSize,
	})
	require.NoError(t, err)

	n, err := db.BackfillOffloadMetadata(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)

	items, err := db.ListOffloadRemote(ctx, OffloadRemoteFilter{Statuses: []string{OffloadStatusPending}}, 10, 0)
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, "h264", items[0].Format)
}

func TestOffloadBucketColumn(t *testing.T) {
	db := newOffloadTestDB(t)
	ctx := context.Background()

	rec := insertMergedRecording(t, db, "bk-1", "camA", 2*time.Hour, 10)
	inserted, err := db.EnqueueOffload(ctx, OffloadItem{
		RecordingID: rec.ID, CameraID: "camA", ObjectKey: "k-bk", Bucket: "important",
		LocalPath: rec.FilePath, FileSize: rec.FileSize,
		StartedAt: rec.StartedAt, EndedAt: rec.EndedAt, Duration: rec.Duration, Format: "h264",
	})
	require.NoError(t, err)
	require.True(t, inserted)

	// Round-trips through claim, get, and evictable listing.
	items, err := db.ClaimPendingOffload(ctx, 1)
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, "important", items[0].Bucket)

	require.NoError(t, db.MarkOffloadUploaded(ctx, items[0].ID, `"e"`, 10))
	got, err := db.GetOffloadItem(ctx, items[0].ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "important", got.Bucket)

	evictable, err := db.ListOffloadEvictable(ctx, time.Now().UTC().Add(time.Minute), 10)
	require.NoError(t, err)
	require.Len(t, evictable, 1)
	assert.Equal(t, "important", evictable[0].Bucket)

	// '' (pre-v42 rows / default bucket) stays '' — resolution to the
	// default bucket happens at the consumer, not in the DB.
	rec2 := insertMergedRecording(t, db, "bk-2", "camA", 3*time.Hour, 10)
	_, err = db.EnqueueOffload(ctx, OffloadItem{
		RecordingID: rec2.ID, CameraID: "camA", ObjectKey: "k-bk2",
		LocalPath: rec2.FilePath, FileSize: rec2.FileSize,
		StartedAt: rec2.StartedAt, EndedAt: rec2.EndedAt, Duration: rec2.Duration, Format: "h264",
	})
	require.NoError(t, err)
	items, err = db.ClaimPendingOffload(ctx, 1)
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Empty(t, items[0].Bucket, "empty bucket = default (legacy rows)")
}
