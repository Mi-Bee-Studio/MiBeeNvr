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

	// Timeline metadata (v41, batch 2): survives eviction (which deletes the
	// recordings row) so remote items stay placeable on the playback
	// timeline. Empty on rows enqueued before v41 — excluded from listings.
	StartedAt time.Time
	EndedAt   time.Time
	Duration  float64
	Format    string

	// Bucket pins the object's bucket for per-camera routing (v42, batch 3).
	// '' = the configured default bucket — resolved at the consumer, never
	// rewritten in place.
	Bucket string
}

// OffloadCandidate is a merged recording discovered eligible for upload.
type OffloadCandidate struct {
	RecordingID string
	CameraID    string
	FilePath    string
	FileSize    int64
	StartedAt   time.Time
	EndedAt     time.Time
	Duration    float64
	Format      string
}

// EnqueueOffload inserts a pending outbox row. Idempotent per recording:
// a recording already known to the outbox (any status) is left untouched and
// the call reports false — re-enqueue after eviction is impossible by design
// (the local file is gone; the archive copy lives remotely).
func (d *DB) EnqueueOffload(ctx context.Context, item OffloadItem) (bool, error) {
	now := time.Now().UTC()
	q := `INSERT OR IGNORE INTO offload_outbox
		(recording_id, camera_id, object_key, local_path, file_size, status, created_at,
		 started_at, ended_at, duration, format, bucket)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);`
	startedAt, endedAt := any(""), any("")
	if !item.StartedAt.IsZero() {
		startedAt = timeToDB(item.StartedAt)
	}
	if !item.EndedAt.IsZero() {
		endedAt = timeToDB(item.EndedAt)
	}
	res, err := d.db.ExecContext(ctx, q,
		item.RecordingID, item.CameraID, item.ObjectKey, item.LocalPath,
		item.FileSize, OffloadStatusPending, timeToDB(now),
		startedAt, endedAt, item.Duration, item.Format, item.Bucket)
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
			etag, bucket, uploaded_size, attempts, last_error, created_at, uploaded_at
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
		&it.FileSize, &it.Status, &it.ETag, &it.Bucket, &it.UploadedSize, &it.Attempts, &it.LastError,
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
		SELECT r.id, r.camera_id, r.file_path, COALESCE(r.file_size,0), r.started_at,
			COALESCE(r.ended_at,''), COALESCE(r.duration,0), r.format
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
		var startedAt, endedAt string
		if err := rows.Scan(&c.RecordingID, &c.CameraID, &c.FilePath, &c.FileSize,
			&startedAt, &endedAt, &c.Duration, &c.Format); err != nil {
			return nil, err
		}
		if t, err := parseTime(startedAt); err == nil {
			c.StartedAt = t
		}
		if t, err := parseTime(endedAt); err == nil {
			c.EndedAt = t
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
			etag, bucket, uploaded_size, attempts, last_error, created_at, uploaded_at
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

// ensureOffloadOutboxMetadataColumns adds the v41 timeline-metadata columns
// (started_at/ended_at/duration/format) to pre-v41 outbox tables and
// backfills them from the recordings join where the recording row still
// exists. Idempotent.
func (d *DB) ensureOffloadOutboxMetadataColumns(ctx context.Context) error {
	for _, col := range []struct{ name, ddl string }{
		{"started_at", "ALTER TABLE offload_outbox ADD COLUMN started_at TEXT DEFAULT ''"},
		{"ended_at", "ALTER TABLE offload_outbox ADD COLUMN ended_at TEXT DEFAULT ''"},
		{"duration", "ALTER TABLE offload_outbox ADD COLUMN duration REAL DEFAULT 0"},
		{"format", "ALTER TABLE offload_outbox ADD COLUMN format TEXT DEFAULT ''"},
	} {
		var exists int
		if err := d.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM pragma_table_info('offload_outbox') WHERE name=?`,
			col.name).Scan(&exists); err != nil {
			return fmt.Errorf("check offload_outbox.%s column: %w", col.name, err)
		}
		if exists == 0 {
			if _, err := d.db.ExecContext(ctx, col.ddl); err != nil {
				return fmt.Errorf("add offload_outbox.%s column: %w", col.name, err)
			}
		}
	}
	// Baseline CREATE for NEW databases carries the columns inline, so the
	// backfill below is the only writer for upgraded ones; safe to run every
	// boot (0 rows once filled).
	_, err := d.db.ExecContext(ctx, `UPDATE offload_outbox SET
		started_at = COALESCE((SELECT r.started_at FROM recordings r WHERE r.id = offload_outbox.recording_id), ''),
		ended_at   = COALESCE((SELECT r.ended_at   FROM recordings r WHERE r.id = offload_outbox.recording_id), ''),
		duration   = COALESCE((SELECT r.duration   FROM recordings r WHERE r.id = offload_outbox.recording_id), 0),
		format     = COALESCE((SELECT r.format     FROM recordings r WHERE r.id = offload_outbox.recording_id), '')
		WHERE started_at = ''`)
	if err != nil {
		return fmt.Errorf("backfill offload_outbox metadata: %w", err)
	}
	return nil
}

// BackfillOffloadMetadata is the explicit one-shot form of the v41 backfill
// (CLI/diagnostics): fills metadata columns from the recordings join. Rows
// whose recording row is gone (already evicted under a pre-v41 build) remain
// empty — they cannot be placed on a timeline and are excluded from remote
// listings. Returns the number of rows filled.
func (d *DB) BackfillOffloadMetadata(ctx context.Context) (int64, error) {
	res, err := d.db.ExecContext(ctx, `UPDATE offload_outbox SET
		started_at = COALESCE((SELECT r.started_at FROM recordings r WHERE r.id = offload_outbox.recording_id), ''),
		ended_at   = COALESCE((SELECT r.ended_at   FROM recordings r WHERE r.id = offload_outbox.recording_id), ''),
		duration   = COALESCE((SELECT r.duration   FROM recordings r WHERE r.id = offload_outbox.recording_id), 0),
		format     = COALESCE((SELECT r.format     FROM recordings r WHERE r.id = offload_outbox.recording_id), '')
		WHERE started_at = ''
		  AND EXISTS (SELECT 1 FROM recordings r WHERE r.id = offload_outbox.recording_id)`)
	if err != nil {
		return 0, fmt.Errorf("backfill offload metadata: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// OffloadRemoteFilter scopes ListOffloadRemote.
type OffloadRemoteFilter struct {
	// CameraID restricts to one camera ("" = all).
	CameraID string
	// Statuses selects outbox statuses (default: evicted only — the only
	// rows whose content lives EXCLUSIVELY in the remote store).
	Statuses []string
	// StartAt/EndAt bound started_at (zero = unbounded on that side).
	StartAt time.Time
	EndAt   time.Time
}

// OffloadRemoteItem is one remote-playable archive item.
type OffloadRemoteItem struct {
	ID           int64     `json:"id"`
	RecordingID  string    `json:"recording_id"`
	CameraID     string    `json:"camera_id"`
	ObjectKey    string    `json:"object_key"`
	FileSize     int64     `json:"file_size"`
	UploadedSize int64     `json:"uploaded_size"`
	Status       string    `json:"status"`
	Format       string    `json:"format"`
	Duration     float64   `json:"duration"`
	StartedAt    time.Time `json:"started_at"`
	EndedAt      time.Time `json:"ended_at"`
	UploadedAt   time.Time `json:"uploaded_at"`
}

// ListOffloadRemote returns outbox rows that are playable from the remote
// store (default: evicted). Metadata-less rows (pre-v41) are excluded — a
// timeline entry without a time window would be a garbage card.
func (d *DB) ListOffloadRemote(ctx context.Context, f OffloadRemoteFilter, limit, offset int) ([]OffloadRemoteItem, error) {
	statuses := f.Statuses
	if len(statuses) == 0 {
		statuses = []string{OffloadStatusEvicted}
	}
	placeholders := make([]string, len(statuses))
	args := make([]any, 0, len(statuses)+4)
	for i, st := range statuses {
		placeholders[i] = "?"
		args = append(args, st)
	}
	q := `SELECT id, recording_id, camera_id, object_key, file_size, uploaded_size,
			status, format, duration, started_at, ended_at, uploaded_at
		FROM offload_outbox
		WHERE status IN (` + strings.Join(placeholders, ",") + `)
		  AND started_at != ''`
	if f.CameraID != "" {
		q += ` AND camera_id = ?`
		args = append(args, f.CameraID)
	}
	if !f.StartAt.IsZero() {
		q += ` AND started_at >= ?`
		args = append(args, timeToDB(f.StartAt))
	}
	if !f.EndAt.IsZero() {
		q += ` AND started_at < ?`
		args = append(args, timeToDB(f.EndAt))
	}
	q += ` ORDER BY started_at DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)

	rows, err := d.readConn().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list offload remote: %w", err)
	}
	defer rows.Close()
	var out []OffloadRemoteItem
	for rows.Next() {
		var it OffloadRemoteItem
		var startedAt, endedAt, uploadedAt string
		if err := rows.Scan(&it.ID, &it.RecordingID, &it.CameraID, &it.ObjectKey,
			&it.FileSize, &it.UploadedSize, &it.Status, &it.Format, &it.Duration,
			&startedAt, &endedAt, &uploadedAt); err != nil {
			return nil, err
		}
		if t, err := parseTime(startedAt); err == nil {
			it.StartedAt = t
		}
		if t, err := parseTime(endedAt); err == nil {
			it.EndedAt = t
		}
		if t, err := parseTime(uploadedAt); err == nil {
			it.UploadedAt = t
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// GetOffloadItem fetches one outbox row by id (nil when absent). Backs the
// playback-proxy endpoint's id→object-key/size resolution.
func (d *DB) GetOffloadItem(ctx context.Context, id int64) (*OffloadItem, error) {
	rows, err := d.readConn().QueryContext(ctx, `
		SELECT id, recording_id, camera_id, object_key, local_path, file_size, status,
			etag, bucket, uploaded_size, attempts, last_error, created_at, uploaded_at
		FROM offload_outbox WHERE id = ?`, id)
	if err != nil {
		return nil, fmt.Errorf("get offload item: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	it, err := scanOffloadItem(rows)
	if err != nil {
		return nil, err
	}
	return &it, nil
}
