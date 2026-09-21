package storage

// db_offload.go — offload outbox (issue #874 batch 1): the SQLite state
// machine that maps merged recordings to remote objects.
//
// State machine: pending → uploading → uploaded → evicted, plus terminal
// skipped (local file vanished before upload — retention won the race).
// The outbox is the SINGLE SOURCE OF TRUTH for upload progress: after a
// kill -9, rows stranded in 'uploading' return to 'pending' on startup and
// are re-uploaded — PutObject to the same key is an idempotent overwrite, so
// recovery produces no duplicate objects and loses no segments.
//
// Rows are never deleted (except with the recording itself); 'evicted' rows
// keep the archive index (camera → object key → window) that batch 2's
// remote browsing builds on.

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Offload outbox status constants (bounded enum — these become DB values).
const (
	OffloadStatusPending   = "pending"
	OffloadStatusUploading = "uploading"
	OffloadStatusUploaded  = "uploaded"
	OffloadStatusEvicted   = "evicted"
	OffloadStatusSkipped   = "skipped"
)

// OffloadItem is one outbox row: a merged recording destined for one object.
type OffloadItem struct {
	ID           int64
	RecordingID  string
	CameraID     string
	ObjectKey    string
	LocalPath    string
	FileSize     int64
	Status       string
	ETag         string
	UploadedSize int64
	Attempts     int
	LastError    string
	CreatedAt    time.Time
	UploadedAt   time.Time // zero = never
}

// OffloadCandidate is a merged recording discovered eligible for upload.
type OffloadCandidate struct {
	RecordingID string
	CameraID    string
	FilePath    string
	FileSize    int64
	StartedAt   time.Time
}

// EnqueueOffload inserts a pending outbox row. Idempotent per recording:
// a recording already known to the outbox (any status) is left untouched and
// the call reports false — re-enqueue after eviction is impossible by design
// (the local file is gone; the archive copy lives remotely).
func (d *DB) EnqueueOffload(ctx context.Context, item OffloadItem) (bool, error) {
	now := time.Now().UTC()
	q := `INSERT OR IGNORE INTO offload_outbox
		(recording_id, camera_id, object_key, local_path, file_size, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?);`
	res, err := d.db.ExecContext(ctx, q,
		item.RecordingID, item.CameraID, item.ObjectKey, item.LocalPath,
		item.FileSize, OffloadStatusPending, timeToDB(now))
	if err != nil {
		return false, fmt.Errorf("enqueue offload: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ClaimPendingOffload atomically transitions up to limit pending rows to
// 'uploading' (FIFO by insertion order) and returns them. Runs on the
// serialized writer, so concurrent claimers cannot take the same row.
func (d *DB) ClaimPendingOffload(ctx context.Context, limit int) ([]OffloadItem, error) {
	defer d.observeTxn(ctx, "offload_claim", time.Now())
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// Claim ids first (guarded by status='pending' so a lost race row is
	// simply not claimed), then read the claimed rows back. The id query
	// lives in a helper so its rows are defer-closed BEFORE the UPDATE runs
	// on the same tx connection.
	ids, err := queryPendingOffloadIDs(ctx, tx, limit)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}

	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	q := `UPDATE offload_outbox SET status='uploading' WHERE id IN (` +
		strings.Join(placeholders, ",") + `) AND status='pending'`
	if _, err := tx.ExecContext(ctx, q, args...); err != nil {
		return nil, err
	}

	items, err := queryOffloadByIDs(ctx, tx, ids)
	if err != nil {
		return nil, err
	}
	return items, tx.Commit()
}

// queryPendingOffloadIDs selects up to limit pending row ids, FIFO.
func queryPendingOffloadIDs(ctx context.Context, tx *sql.Tx, limit int) ([]int64, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT id FROM offload_outbox WHERE status='pending' ORDER BY id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func queryOffloadByIDs(ctx context.Context, q *sql.Tx, ids []int64) ([]OffloadItem, error) {
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	rows, err := q.QueryContext(ctx,
		`SELECT id, recording_id, camera_id, object_key, local_path, file_size, status,
			etag, uploaded_size, attempts, last_error, created_at, uploaded_at
		 FROM offload_outbox WHERE id IN (`+strings.Join(placeholders, ",")+`) ORDER BY id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []OffloadItem
	for rows.Next() {
		it, err := scanOffloadItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, it)
	}
	return items, rows.Err()
}

func scanOffloadItem(s scanner) (OffloadItem, error) {
	var it OffloadItem
	var createdAt, uploadedAt *string
	if err := s.Scan(&it.ID, &it.RecordingID, &it.CameraID, &it.ObjectKey, &it.LocalPath,
		&it.FileSize, &it.Status, &it.ETag, &it.UploadedSize, &it.Attempts, &it.LastError,
		&createdAt, &uploadedAt); err != nil {
		return it, err
	}
	if createdAt != nil {
		if t, err := parseTime(*createdAt); err == nil {
			it.CreatedAt = t
		}
	}
	if uploadedAt != nil && *uploadedAt != "" {
		if t, err := parseTime(*uploadedAt); err == nil {
			it.UploadedAt = t
		}
	}
	return it, nil
}

// MarkOffloadUploaded records upload confirmation (etag + verified size).
func (d *DB) MarkOffloadUploaded(ctx context.Context, id int64, etag string, uploadedSize int64) error {
	_, err := d.db.ExecContext(ctx,
		`UPDATE offload_outbox SET status=?, etag=?, uploaded_size=?, uploaded_at=?, last_error='' WHERE id=?`,
		OffloadStatusUploaded, etag, uploadedSize, timeToDB(time.Now().UTC()), id)
	if err != nil {
		return fmt.Errorf("mark offload uploaded: %w", err)
	}
	return nil
}

// MarkOffloadRetry returns a failed upload to 'pending' with the error
// recorded and the attempt counter bumped. The next scan re-claims it.
func (d *DB) MarkOffloadRetry(ctx context.Context, id int64, errMsg string) error {
	_, err := d.db.ExecContext(ctx,
		`UPDATE offload_outbox SET status=?, attempts=attempts+1, last_error=? WHERE id=?`,
		OffloadStatusPending, errMsg, id)
	if err != nil {
		return fmt.Errorf("mark offload retry: %w", err)
	}
	return nil
}

// MarkOffloadSkipped terminally marks an item whose local file disappeared
// before upload (retention/disk-threshold cleanup won the race). The
// recording is NOT recoverable from this NVR — log loudly upstream.
func (d *DB) MarkOffloadSkipped(ctx context.Context, id int64, reason string) error {
	_, err := d.db.ExecContext(ctx,
		`UPDATE offload_outbox SET status=?, last_error=? WHERE id=?`,
		OffloadStatusSkipped, reason, id)
	if err != nil {
		return fmt.Errorf("mark offload skipped: %w", err)
	}
	return nil
}

// MarkOffloadEvicted marks a verified-evicted item (local file removed after
// remote confirmation).
func (d *DB) MarkOffloadEvicted(ctx context.Context, id int64) error {
	_, err := d.db.ExecContext(ctx,
		`UPDATE offload_outbox SET status=?, evicted_at=? WHERE id=?`,
		OffloadStatusEvicted, timeToDB(time.Now().UTC()), id)
	if err != nil {
		return fmt.Errorf("mark offload evicted: %w", err)
	}
	return nil
}

// RequeueUploadingOffload is the startup crash-recovery sweep: rows stranded
// in 'uploading' (process died mid-PUT) return to 'pending'. Re-upload is
// idempotent (same key overwrite), so this loses nothing and duplicates
// nothing. Returns the number of rows requeued.
func (d *DB) RequeueUploadingOffload(ctx context.Context) (int64, error) {
	res, err := d.db.ExecContext(ctx,
		`UPDATE offload_outbox SET status='pending' WHERE status='uploading'`)
	if err != nil {
		return 0, fmt.Errorf("requeue uploading offload: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// RequeueStaleUploadedOffload re-queues confirmed uploads whose local file
// has since GROWN (a late rolling-merge backfill appended to the window
// bucket after it was uploaded — window-end + min-age usually prevents this,
// the size comparison makes it airtight). The stale object is overwritten on
// re-upload. Returns the number of rows requeued.
func (d *DB) RequeueStaleUploadedOffload(ctx context.Context) (int64, error) {
	res, err := d.db.ExecContext(ctx,
		`UPDATE offload_outbox SET status='pending'
		 WHERE status='uploaded'
		   AND EXISTS (SELECT 1 FROM recordings r
		               WHERE r.id = offload_outbox.recording_id
		                 AND r.file_size != offload_outbox.uploaded_size)`)
	if err != nil {
		return 0, fmt.Errorf("requeue stale uploaded offload: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// ListOffloadCandidates returns up to limit merged recordings eligible for
// first-time upload: rolling-merge products (merge_tier='rolling', no
// merge_path — the row IS the artifact, 1:1), closed before the cutoff
// (window elapsed + grace), not archived, and not yet known to the outbox.
//
// Batch-merge products (merge_path set, N segment rows : 1 artifact) are
// deliberately excluded from batch 1: deduplicating by artifact path is a
// follow-up. Rolling merge is default-on, so the overwhelming majority of
// footage flows through this path.
func (d *DB) ListOffloadCandidates(ctx context.Context, cutoff time.Time, limit int) ([]OffloadCandidate, error) {
	rows, err := d.readConn().QueryContext(ctx, `
		SELECT r.id, r.camera_id, r.file_path, COALESCE(r.file_size,0), r.started_at
		FROM recordings r
		WHERE r.merge_status = 'merged'
		  AND r.merge_tier = 'rolling'
		  AND (r.merge_path IS NULL OR r.merge_path = '')
		  AND COALESCE(r.archived, 0) = 0
		  AND r.ended_at IS NOT NULL
		  AND r.ended_at < ?
		  AND NOT EXISTS (SELECT 1 FROM offload_outbox o WHERE o.recording_id = r.id)
		ORDER BY r.ended_at ASC
		LIMIT ?`, timeToDB(cutoff), limit)
	if err != nil {
		return nil, fmt.Errorf("list offload candidates: %w", err)
	}
	defer rows.Close()
	var out []OffloadCandidate
	for rows.Next() {
		var c OffloadCandidate
		var startedAt string
		if err := rows.Scan(&c.RecordingID, &c.CameraID, &c.FilePath, &c.FileSize, &startedAt); err != nil {
			return nil, err
		}
		if t, err := parseTime(startedAt); err == nil {
			c.StartedAt = t
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CountOffloadByStatus returns the row count per status (absent statuses are
// simply missing from the map).
func (d *DB) CountOffloadByStatus(ctx context.Context) (map[string]int, error) {
	rows, err := d.readConn().QueryContext(ctx,
		`SELECT status, COUNT(*) FROM offload_outbox GROUP BY status`)
	if err != nil {
		return nil, fmt.Errorf("count offload by status: %w", err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			return nil, err
		}
		out[status] = n
	}
	return out, rows.Err()
}

// CountOffloadBacklog returns pending+uploading rows — the "how far behind
// is the uplink" number that gates enqueueing when BacklogLimit is set.
func (d *DB) CountOffloadBacklog(ctx context.Context) (int, error) {
	var n int
	err := d.readConn().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM offload_outbox WHERE status IN ('pending','uploading')`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count offload backlog: %w", err)
	}
	return n, nil
}

// ListOffloadEvictable returns up to limit 'uploaded' items confirmed before
// the given timestamp — the evict candidate set.
func (d *DB) ListOffloadEvictable(ctx context.Context, confirmedBefore time.Time, limit int) ([]OffloadItem, error) {
	rows, err := d.readConn().QueryContext(ctx, `
		SELECT id, recording_id, camera_id, object_key, local_path, file_size, status,
			etag, uploaded_size, attempts, last_error, created_at, uploaded_at
		FROM offload_outbox
		WHERE status='uploaded' AND uploaded_at != '' AND uploaded_at < ?
		ORDER BY uploaded_at ASC
		LIMIT ?`, timeToDB(confirmedBefore), limit)
	if err != nil {
		return nil, fmt.Errorf("list offload evictable: %w", err)
	}
	defer rows.Close()
	var items []OffloadItem
	for rows.Next() {
		it, err := scanOffloadItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, it)
	}
	return items, rows.Err()
}
