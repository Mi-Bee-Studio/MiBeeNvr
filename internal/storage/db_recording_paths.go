package storage

// Path-oriented and retention queries for recordings: path-set lookups,
// orphan insertion, deletion by path, expiry scans. Split out of
// db_recording.go.

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// GetRecordingsByPathSet returns a set of file paths that exist in the recordings table.
// Used for orphan file reconciliation to determine which files are already registered.
//
// Note: this query does NOT scope by camera_id and relies on a full scan of the
// recordings table (file_path is not indexed). For orphan reconciliation where
// the caller already knows the camera_id, prefer GetRecordingPathsByCamera which
// uses the idx_recordings_camera_time index for a bounded range scan.
func (d *DB) GetRecordingsByPathSet(ctx context.Context, paths []string) (map[string]bool, error) {
	result := make(map[string]bool)
	if len(paths) == 0 {
		return result, nil
	}
	// SQLite's SQLITE_MAX_VARIABLE_NUMBER defaults to 999 (older) or 32766
	// (3.32+). modernc.org/sqlite sets 32766, but to stay portable and avoid
	// pathological planner behavior on huge IN lists, chunk at 500.
	const chunkSize = 500
	for start := 0; start < len(paths); start += chunkSize {
		end := start + chunkSize
		if end > len(paths) {
			end = len(paths)
		}
		chunk := paths[start:end]
		placeholders := make([]string, len(chunk))
		args := make([]interface{}, len(chunk))
		for i, p := range chunk {
			placeholders[i] = "?"
			args[i] = p
		}
		q := "SELECT file_path FROM recordings WHERE file_path IN (" + strings.Join(placeholders, ",") + ")"
		if err := func() error {
			rows, err := d.readConn().QueryContext(ctx, q, args...)
			if err != nil {
				return fmt.Errorf("get recordings by path set: %w", err)
			}
			defer rows.Close()
			for rows.Next() {
				var p string
				if err := rows.Scan(&p); err != nil {
					return fmt.Errorf("get recordings by path set: %w", err)
				}
				result[p] = true
			}
			return rows.Err()
		}(); err != nil {
			return nil, err
		}
	}
	return result, nil
}

// GetRecordingPathsByCamera returns the set of file_path values stored for a
// single camera. Used by orphan reconciliation as a much cheaper replacement
// for GetRecordingsByPathSet when the caller knows the camera_id: it uses the
// idx_recordings_camera_time(camera_id, ...) index for a bounded range scan
// (only that camera's rows) instead of scanning the entire recordings table.
//
// The returned set is the complete path list for the camera; callers intersect
// it with their disk scan locally (O(n) hashset lookup) rather than via a
// potentially huge IN (?, ?, ...) list. On a production tree with ~15k
// recordings, this reduces per-camera reconciliation IO from a full table scan
// to an index range read of just that camera's rows.
func (d *DB) GetRecordingPathsByCamera(ctx context.Context, cameraID string) (map[string]bool, error) {
	result := make(map[string]bool)
	rows, err := d.readConn().QueryContext(ctx,
		`SELECT file_path FROM recordings WHERE camera_id = ?`, cameraID)
	if err != nil {
		return nil, fmt.Errorf("get recording paths by camera: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, fmt.Errorf("get recording paths by camera: %w", err)
		}
		result[p] = true
	}
	// A partial set here is worse than an error: the orphan cleanup treats
	// DB-known files missing from this set as deletable orphans (#1000).
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("get recording paths by camera: %w", err)
	}
	return result, nil
}

// orphanBatchSize controls how many orphans are inserted per transaction.
// Larger batches reduce transaction count but increase lock duration.
// 500 strikes a balance between throughput and write lock hold time.
const orphanBatchSize = 500

// InsertOrphanRecordings batch-inserts orphan recording metadata using INSERT OR IGNORE.
// Returns the number of actually inserted rows (skips duplicates).
// Inserts are performed in batches of orphanBatchSize to avoid holding the
// write lock for too long. Context timeout is checked between batches.
func (d *DB) InsertOrphanRecordings(ctx context.Context, recordings []*model.Recording) (int, error) {
	defer d.observeTxn(ctx, "recording_insert", time.Now())
	if len(recordings) == 0 {
		return 0, nil
	}
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}

	q := `INSERT OR IGNORE INTO recordings(id, camera_id, file_path, format, started_at, ended_at, duration, file_size, frame_count, merge_status) VALUES(?,?,?,?,?,?,?,?,?,?);`
	totalInserted := 0

	for i := 0; i < len(recordings); i += orphanBatchSize {
		if ctx.Err() != nil {
			return totalInserted, ctx.Err()
		}

		end := i + orphanBatchSize
		if end > len(recordings) {
			end = len(recordings)
		}

		tx, err := d.db.BeginTx(ctx, nil)
		if err != nil {
			return totalInserted, err
		}

		batchInserted := 0
		for _, r := range recordings[i:end] {
			result, err := tx.ExecContext(ctx, q, r.ID, r.CameraID, r.FilePath, r.Format, timeToDB(r.StartedAt), timeToDB(r.EndedAt), r.Duration, r.FileSize, r.FrameCount, r.MergeStatus)
			if err != nil {
				tx.Rollback()
				return totalInserted, err
			}
			n, _ := result.RowsAffected()
			batchInserted += int(n)
		}

		if err := tx.Commit(); err != nil {
			return totalInserted, err
		}
		totalInserted += batchInserted
	}

	return totalInserted, nil
}

func (d *DB) DeleteRecording(ctx context.Context, id string) error {
	defer d.observeTxn(ctx, "api_write", time.Now())
	_, err := d.db.ExecContext(ctx, `DELETE FROM recordings WHERE id=?;`, id)
	return err
}

// pathBound matches rows whose stored path equals path or sits underneath it
// (path + "/…"), without LIKE so path characters need no escaping and a
// directory path never matches a sibling with a longer name ("/cam" must not
// match "/cam-2").
func pathBound(col, param string) string {
	return col + ` = ` + param + `
		OR (substr(` + col + `, 1, length(` + param + `)) = ` + param + `
			AND substr(` + col + `, length(` + param + `) + 1, 1) = '/')`
}

// DeleteRecordingRowsByPath removes every recordings row referencing the exact
// path — as source file_path or merged merge_path. Used by out-of-band file
// removal (FTP) so the table keeps no permanent-404 rows (#1004). Returns the
// number of rows removed.
func (d *DB) DeleteRecordingRowsByPath(ctx context.Context, path string) (int64, error) {
	defer d.observeTxn(ctx, "api_write", time.Now())
	res, err := d.db.ExecContext(ctx,
		`DELETE FROM recordings WHERE `+pathBound("file_path", "?1")+`
			OR `+pathBound("merge_path", "?1"), path)
	if err != nil {
		return 0, fmt.Errorf("delete recording rows by path: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// RenameRecordingPaths rewrites recordings rows after an out-of-band rename
// (FTP): a row whose file_path/merge_path equals oldPath or sits underneath it
// is rewritten to the corresponding newPath. Returns the number of rows
// touched.
func (d *DB) RenameRecordingPaths(ctx context.Context, oldPath, newPath string) (int64, error) {
	defer d.observeTxn(ctx, "api_write", time.Now())
	res, err := d.db.ExecContext(ctx,
		`UPDATE recordings SET
			file_path = ?2 || substr(file_path, length(?1) + 1),
			merge_path = CASE WHEN merge_path IS NULL THEN NULL
				ELSE ?2 || substr(merge_path, length(?1) + 1) END
		WHERE `+pathBound("file_path", "?1")+`
			OR `+pathBound("merge_path", "?1"), oldPath, newPath)
	if err != nil {
		return 0, fmt.Errorf("rename recording paths: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// DeleteRecordingsBatch deletes multiple recordings by ID using a single batch DELETE.
// Returns the slice of IDs requested for deletion on success (nil on failure).
// Uses a single IN clause to minimize transaction duration and SQLITE_BUSY contention.
func (d *DB) DeleteRecordingsBatch(ctx context.Context, ids []string) ([]string, error) {
	defer d.observeTxn(ctx, "api_write", time.Now())
	if len(ids) == 0 {
		return nil, nil
	}

	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	q := "DELETE FROM recordings WHERE id IN (" + strings.Join(placeholders, ",") + ")"
	res, err := d.db.ExecContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("delete recordings batch: %w", err)
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		// The deleted rows may be merge-lineage targets; drop their lineage
		// so stale-ID lookups can't resolve to a vanished recording (#903).
		if perr := d.purgeLineageByTargetIDs(ctx, ids); perr != nil {
			return nil, perr
		}
		return ids, nil
	}
	return nil, nil
}

func (d *DB) SetMerged(ctx context.Context, id string, merged bool) error {
	defer d.observeTxn(ctx, "recording_close", time.Now())
	status := model.MergeStatusPending
	if merged {
		status = model.MergeStatusMerged
	}
	_, err := d.db.ExecContext(ctx, `UPDATE recordings SET merge_status=? WHERE id=?;`, status, id)
	return err
}

func (d *DB) CleanupIncomplete(ctx context.Context) error {
	defer d.observeTxn(ctx, "api_write", time.Now())
	_, err := d.db.ExecContext(ctx, `DELETE FROM recordings WHERE ended_at IS NULL;`)
	return err
}

// listExpiredRecordings is the shared core of the three ListExpired* methods
// below (#237). cameraID=="" means "all cameras"; archived selects the
// archived=0/1 partition. retentionDays is always applied.
func (d *DB) listExpiredRecordings(ctx context.Context, cameraID string, archived, retentionDays int) ([]model.Recording, error) {
	sqlstr := selectRecordingColumns + ` WHERE ended_at IS NOT NULL AND archived=?`
	args := []any{archived}
	if cameraID != "" {
		sqlstr += ` AND camera_id=?`
		args = append(args, cameraID)
	}
	sqlstr += ` AND ended_at < datetime('now', '-' || ? || ' days') ORDER BY ended_at ASC;`
	args = append(args, retentionDays)

	rows, err := d.readConn().QueryContext(ctx, sqlstr, args...)
	if err != nil {
		return nil, fmt.Errorf("list expired recordings: %w", err)
	}
	defer rows.Close()
	var res []model.Recording
	for rows.Next() {
		r, err := scanRecordingRow(rows)
		if err != nil {
			return nil, fmt.Errorf("list expired recordings: %w", err)
		}
		res = append(res, *r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list expired recordings: %w", err)
	}
	return res, nil
}

func (d *DB) ListExpiredRecordings(ctx context.Context, retentionDays int) ([]model.Recording, error) {
	return d.listExpiredRecordings(ctx, "", 0, retentionDays)
}

// ListExpiredRecordingsByCamera returns expired recordings for a specific camera
func (d *DB) ListExpiredRecordingsByCamera(ctx context.Context, cameraID string, retentionDays int) ([]model.Recording, error) {
	return d.listExpiredRecordings(ctx, cameraID, 0, retentionDays)
}

// ListExpiredArchivedRecordingsByCamera returns expired archived recordings for a specific camera.
func (d *DB) ListExpiredArchivedRecordingsByCamera(ctx context.Context, cameraID string, retentionDays int) ([]model.Recording, error) {
	return d.listExpiredRecordings(ctx, cameraID, 1, retentionDays)
}

func (d *DB) ListOldestRecordings(ctx context.Context, limit int) ([]model.Recording, error) {
	sqlstr := selectRecordingColumns + ` WHERE ended_at IS NOT NULL AND archived=0 ORDER BY ended_at ASC LIMIT ?;`
	rows, err := d.readConn().QueryContext(ctx, sqlstr, limit)
	if err != nil {
		return nil, fmt.Errorf("list oldest recordings: %w", err)
	}
	defer rows.Close()
	var res []model.Recording
	for rows.Next() {
		r, err := scanRecordingRow(rows)
		if err != nil {
			return nil, fmt.Errorf("list oldest recordings: %w", err)
		}
		res = append(res, *r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list oldest recordings: %w", err)
	}
	return res, nil
}

// ListOldestRecordingsMotionAware is ListOldestRecordings with boring-first
// ordering for the disk-threshold cleanup path (issue #435): static segments
// (motion_score≈0) are deleted first, active segments (score→1) last, and
// unanalyzed segments (-1) rank neutrally in between at 0.5. Age remains the
// tiebreaker within the same score band. Issue #634: the score is discounted
// by motion_confidence — a bitrate-starved segment's relative score is
// rate-control jitter, not activity, so it must not be protected from
// eviction. Rows analyzed before the confidence column (-1) keep full weight.
func (d *DB) ListOldestRecordingsMotionAware(ctx context.Context, limit int) ([]model.Recording, error) {
	sqlstr := selectRecordingColumns + ` WHERE ended_at IS NOT NULL AND archived=0
		ORDER BY (CASE WHEN motion_score < 0 THEN 0.5
			ELSE motion_score * (CASE WHEN motion_confidence < 0 THEN 1.0 ELSE motion_confidence END) END) ASC, ended_at ASC LIMIT ?;`
	rows, err := d.readConn().QueryContext(ctx, sqlstr, limit)
	if err != nil {
		return nil, fmt.Errorf("list oldest recordings motion-aware: %w", err)
	}
	defer rows.Close()
	var res []model.Recording
	for rows.Next() {
		r, err := scanRecordingRow(rows)
		if err != nil {
			return nil, fmt.Errorf("list oldest recordings motion-aware: %w", err)
		}
		res = append(res, *r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list oldest recordings motion-aware: %w", err)
	}
	return res, nil
}

// ListRecordingPathsByCamera returns the basenames of all file_path values for a camera's recordings.
func (d *DB) ListRecordingPathsByCamera(ctx context.Context, cameraID string) (map[string]bool, error) {
	rows, err := d.readConn().QueryContext(ctx, `SELECT file_path FROM recordings WHERE camera_id=?`, cameraID)
	if err != nil {
		return nil, fmt.Errorf("list recording paths by camera: %w", err)
	}
	defer rows.Close()
	result := make(map[string]bool)
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, fmt.Errorf("list recording paths by camera: %w", err)
		}
		result[filepath.Base(p)] = true
	}
	// Partial sets feed the orphan deleter — propagate iteration errors (#1000).
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list recording paths by camera: %w", err)
	}
	return result, nil
}

// ListRecordingArtifactPathsByCamera returns the FULL paths of every on-disk
// artifact a camera's recordings still reference — file_path (source) and
// merge_path (merged output) alike. The deep orphan scan (#117 leak class)
// walks the nested trees once and needs the whole reference set up front;
// a per-file query (PathIsRecordingFile) would be one SQL round-trip per
// walked file. Empty/NULL paths are skipped.
func (d *DB) ListRecordingArtifactPathsByCamera(ctx context.Context, cameraID string) (map[string]bool, error) {
	rows, err := d.readConn().QueryContext(ctx,
		`SELECT file_path, merge_path FROM recordings WHERE camera_id=?`, cameraID)
	if err != nil {
		return nil, fmt.Errorf("list recording artifact paths by camera: %w", err)
	}
	defer rows.Close()
	result := make(map[string]bool)
	for rows.Next() {
		var fp, mp *string
		if err := rows.Scan(&fp, &mp); err != nil {
			return nil, fmt.Errorf("list recording artifact paths by camera: %w", err)
		}
		if fp != nil && *fp != "" {
			result[*fp] = true
		}
		if mp != nil && *mp != "" {
			result[*mp] = true
		}
	}
	// Partial sets feed the deep orphan deleter — propagate iteration errors (#1000).
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list recording artifact paths by camera: %w", err)
	}
	return result, nil
}

// PathIsRecordingFile reports whether the given on-disk path is still
// referenced by any recording row for the camera — either as the source
// file_path or the merged-output merge_path. The comparison is on the full
// path, which is how both columns are stored (absolute paths under the storage
// root). Used by the orphan scanner (repair reclaim-orphan-merges) to avoid
// reclaiming files that still belong to a live recording.
func (d *DB) PathIsRecordingFile(ctx context.Context, cameraID, fullpath string) (bool, error) {
	var exists int
	err := d.readConn().QueryRowContext(ctx, `
		SELECT 1 FROM recordings
		WHERE camera_id=?
		  AND (file_path=? OR merge_path=?)
		LIMIT 1;`, cameraID, fullpath, fullpath).Scan(&exists)
	if err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// ListStalePendingRecordings returns up to limit sweep-candidate recordings
// (any format, any camera) whose started_at is older than the cutoff,
// excluding archived rows and rows with an active AI status (protected from
// cleanup like retention). The caller stats each row's file and deletes the
// ones that never existed — the ghost-row sweep for the 2026-09-08 incident
// class (candidate row + missing file = permanent 404 entry).
//
// Candidates are layer=0 'pending' rows PLUS terminal 'sublayer' rows
// (#763): a sub-layer archive whose file vanished is just as much a ghost.
// Pre-migration layer=1 'pending' zombies are NOT candidates — they are
// healthy archives pending the v39 rewrite, and each one returned would
// starve a real ghost of the sweep's LIMIT slot.
func (d *DB) ListStalePendingRecordings(ctx context.Context, cutoff time.Time, limit int) ([]model.Recording, error) {
	defer d.observeQuery("ListStalePendingRecordings", time.Now())
	if limit <= 0 {
		limit = 500
	}
	sqlstr := selectRecordingColumns + ` WHERE ((merge_status = 'pending' AND COALESCE(layer,0)=0) OR merge_status = 'sublayer') AND archived = 0
		AND (ai_status IS NULL OR ai_status = '')
		AND started_at < ? ORDER BY started_at LIMIT ?;`
	rows, err := d.readConn().QueryContext(ctx, sqlstr, timeToDB(cutoff), limit)
	if err != nil {
		return nil, fmt.Errorf("list stale pending recordings: %w", err)
	}
	defer rows.Close()
	var res []model.Recording
	for rows.Next() {
		r, err := scanRecordingRow(rows)
		if err != nil {
			return nil, fmt.Errorf("list stale pending recordings: %w", err)
		}
		res = append(res, *r)
	}
	return res, nil
}

// ListRecordingsWithoutTranscode returns recordings for a camera that have ended
// but have no corresponding transcoding task, and are not archived.
func (d *DB) ListRecordingsWithoutTranscode(ctx context.Context, cameraID string) ([]model.Recording, error) {
	sqlstr := selectRecordingColumns + ` WHERE camera_id = ? AND ended_at IS NOT NULL AND archived = 0 AND NOT EXISTS (SELECT 1 FROM transcoding_tasks WHERE recording_id = recordings.id) ORDER BY started_at DESC;`
	rows, err := d.readConn().QueryContext(ctx, sqlstr, cameraID)
	if err != nil {
		return nil, fmt.Errorf("list recordings without transcode: %w", err)
	}
	defer rows.Close()
	res := make([]model.Recording, 0)
	for rows.Next() {
		r, err := scanRecordingRow(rows)
		if err != nil {
			return nil, fmt.Errorf("list recordings without transcode: %w", err)
		}
		res = append(res, *r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list recordings without transcode: %w", err)
	}
	return res, nil
}

// RepairZeroDurationRecordings returns recordings where duration=0 but the file is
// non-trivial in size, non-MJPEG, has ended_at set, and merge_status=pending.
// These are candidates for duration repair via ffprobe.
func (d *DB) RepairZeroDurationRecordings(ctx context.Context) ([]model.Recording, error) {
	sqlstr := selectRecordingColumns + ` WHERE duration = 0 AND file_size > 1048576 AND format != 'mjpeg' AND ended_at IS NOT NULL AND merge_status = 'pending';`
	rows, err := d.readConn().QueryContext(ctx, sqlstr)
	if err != nil {
		return nil, fmt.Errorf("repair zero duration recordings: %w", err)
	}
	defer rows.Close()
	var res []model.Recording
	for rows.Next() {
		r, err := scanRecordingRow(rows)
		if err != nil {
			return nil, fmt.Errorf("repair zero duration recordings: %w", err)
		}
		res = append(res, *r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("repair zero duration recordings: %w", err)
	}
	return res, nil
}

// UpdateRecordingDuration updates the duration and ended_at for a recording.
func (d *DB) UpdateRecordingDuration(ctx context.Context, id string, duration float64, endedAt time.Time) error {
	defer d.observeTxn(ctx, "recording_close", time.Now())
	_, err := d.db.ExecContext(ctx, `UPDATE recordings SET duration=?, ended_at=? WHERE id=?;`, duration, timeToDB(endedAt), id)
	return err
}

// ListZeroDurationRecordings returns recordings whose duration is 0 — these are
// corrupted segments where the recorder failed to compute ended_at at close time
// (a bug fixed in a prior release; historical rows remain). The video files are
// typically intact, so a CLI repair tool can re-probe the file duration and call
// UpdateRecordingDuration to restore correct metadata.
//
// cameraID="" scans all cameras. limit caps the result (0 = unlimited).
func (d *DB) ListZeroDurationRecordings(ctx context.Context, cameraID string, limit int) ([]*model.Recording, error) {
	query := selectRecordingColumns + ` WHERE duration = 0 AND ended_at IS NOT NULL`
	args := []interface{}{}
	if cameraID != "" {
		query += " AND camera_id = ?"
		args = append(args, cameraID)
	}
	query += " ORDER BY camera_id, started_at ASC"
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}
	query += ";"

	rows, err := d.readConn().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list zero duration recordings: %w", err)
	}
	defer rows.Close()
	var res []*model.Recording
	for rows.Next() {
		r, err := scanRecordingRow(rows)
		if err != nil {
			return nil, fmt.Errorf("list zero duration recordings: %w", err)
		}
		res = append(res, r)
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("list zero duration recordings: %w", err)
		}
	}
	return res, nil
}

// UpdateRecordingAIStatus sets the AI processing status for a recording.
// Terminal statuses (completed — what the API handler validates and external
// consumers send — plus the legacy "done"/"skipped" spellings) stamp
// ai_processed_at. The list previously held only "done", which the handler
// rejects, so ai_processed_at was NEVER stamped on successful processing.
// Non-terminal statuses (pending/processing) leave it untouched.
//
// Multi-instance aggregate guard: with several vision consumers processing
// the same recording, the single ai_status column must never regress —
// writes are ranked (completed > skipped > failed > pending/processing/”)
// and only land when their rank EXCEEDS the current one. A consumer retry
// flow (failed → processing → completed) therefore shows "failed" until the
// final "completed" lands, and one instance's completed result is never
// clobbered by another's failed/skipped report.
func (d *DB) UpdateRecordingAIStatus(ctx context.Context, id, status, errMsg string) error {
	defer d.observeTxn(ctx, "ai_event", time.Now())
	now := time.Now().UTC().Format("2006-01-02 15:04:05.999999999")
	rank := aiStatusRank(status)
	const currentRank = `CASE COALESCE(ai_status,'') WHEN 'completed' THEN 3 WHEN 'skipped' THEN 2 WHEN 'failed' THEN 1 ELSE 0 END`
	_, err := d.db.ExecContext(ctx,
		`UPDATE recordings SET
			ai_status = CASE WHEN ? >= (`+currentRank+`) THEN ? ELSE ai_status END,
			ai_error = CASE WHEN ? >= (`+currentRank+`) THEN ? ELSE ai_error END,
			ai_processed_at = CASE WHEN ? >= (`+currentRank+`) AND ? IN ('completed','done','failed','skipped') THEN ? ELSE ai_processed_at END
		WHERE id=?;`,
		rank, status,
		rank, errMsg,
		rank, status, now, id)
	return err
}

// aiStatusRank maps an ai_status value to its aggregate precedence.
func aiStatusRank(status string) int {
	switch status {
	case "completed":
		return 3
	case "skipped":
		return 2
	case "failed":
		return 1
	default: // pending / processing / ''
		return 0
	}
}

// GetRecordingAIStatus returns the AI processing status of a recording.
func (d *DB) GetRecordingAIStatus(ctx context.Context, id string) (status string, err error) {
	err = d.readConn().QueryRowContext(ctx, `SELECT COALESCE(ai_status, '') FROM recordings WHERE id=?`, id).Scan(&status)
	return
}

// MarkRecordingsSkippedByIDs marks the given recordings ai_status='skipped'
// with the drop reason in ai_error (#671 — consumer-reported queue drops).
// Only rows still in a non-terminal AI state (”, pending, processing) are
// touched; completed/failed/skipped stay as-is. Returns rows marked.
func (d *DB) MarkRecordingsSkippedByIDs(ctx context.Context, ids []string, aiErr string) (int64, error) {
	defer d.observeQuery("MarkRecordingsSkippedByIDs", time.Now())
	if len(ids) == 0 {
		return 0, nil
	}
	var sb strings.Builder
	sb.WriteString(`UPDATE recordings SET ai_status='skipped', ai_error=?, ai_processed_at=? WHERE id IN (`)
	for range ids {
		sb.WriteString("?,")
	}
	sbString := sb.String()
	sbString = sbString[:len(sbString)-1] + `) AND COALESCE(ai_status,'') IN ('','pending','processing');`

	args := make([]any, 0, len(ids)+2)
	args = append(args, aiErr, time.Now().UTC().Format("2006-01-02 15:04:05.999999999"))
	for _, id := range ids {
		args = append(args, id)
	}
	res, err := d.db.ExecContext(ctx, sbString, args...)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// MarkRecordingsSkippedByRange is the id-less fallback of
// MarkRecordingsSkippedByIDs: mark every non-terminal recording of the given
// camera whose started_at falls inside [from, to] (#671). Returns rows marked.
func (d *DB) MarkRecordingsSkippedByRange(ctx context.Context, cameraID string, from, to time.Time, aiErr string) (int64, error) {
	defer d.observeQuery("MarkRecordingsSkippedByRange", time.Now())
	if cameraID == "" || to.Before(from) {
		return 0, nil
	}
	res, err := d.db.ExecContext(ctx,
		`UPDATE recordings SET ai_status='skipped', ai_error=?, ai_processed_at=? WHERE camera_id=? AND started_at >= ? AND started_at <= ? AND COALESCE(ai_status,'') IN ('','pending','processing');`,
		aiErr, time.Now().UTC().Format("2006-01-02 15:04:05.999999999"), cameraID, timeToDB(from), timeToDB(to))
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// UpdateRecordingMotionScore persists the compressed-domain activity score,
// absolute-size confidence (issue #634) and activity flags for a recording
// (issue #435). Written by the offline motion analyzer after a segment
// completes. Confidence < 0 means "unknown" (pre-v34 rows).
func (d *DB) UpdateRecordingMotionScore(ctx context.Context, id string, score, confidence float64, flags string) error {
	_, err := d.db.ExecContext(ctx,
		`UPDATE recordings SET motion_score=?, motion_confidence=?, activity_flags=? WHERE id=?`,
		score, confidence, flags, id)
	return err
}

// BatchGetRecordingAIStatus returns a map of recording ID to AI status for a batch of IDs.
// Eliminates N+1 by fetching all statuses in a single query.
func (d *DB) BatchGetRecordingAIStatus(ctx context.Context, ids []string) (map[string]string, error) {
	result := make(map[string]string, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	// SQLITE_MAX_VARIABLE_NUMBER rail (#1173): callers hand over unbounded id
	// sets — cleanup's BatchDeleteRecordingsWithFiles passes the full expired
	// list of a camera in one call, and an audio-only camera (born-terminal
	// rows, no merge consumer) accumulates well past 32766 expired rows. One
	// placeholder per id then failed the prepare with "too many SQL variables"
	// and the whole deletion batch aborted on every cleanup cycle, so retention
	// never converged for that camera. Chunk at 500 (same rail as
	// GetRecordingsByPathSet).
	const chunkSize = 500
	for start := 0; start < len(ids); start += chunkSize {
		end := start + chunkSize
		if end > len(ids) {
			end = len(ids)
		}
		chunk := ids[start:end]
		placeholders := make([]string, len(chunk))
		args := make([]interface{}, len(chunk))
		for i, id := range chunk {
			placeholders[i] = "?"
			args[i] = id
		}
		q := "SELECT id, COALESCE(ai_status, '') FROM recordings WHERE id IN (" + strings.Join(placeholders, ",") + ")"
		// Per-chunk closure so rows.Close is a defer (sqlclosecheck) while the
		// outer loop keeps scanning chunks — same shape as GetRecordingsByPathSet.
		if err := func() error {
			rows, err := d.readConn().QueryContext(ctx, q, args...)
			if err != nil {
				return fmt.Errorf("batch get recording ai status: %w", err)
			}
			defer rows.Close()
			for rows.Next() {
				var id, status string
				if err := rows.Scan(&id, &status); err != nil {
					return fmt.Errorf("batch get recording ai status: %w", err)
				}
				result[id] = status
			}
			return rows.Err()
		}(); err != nil {
			return nil, err
		}
	}
	return result, nil
}

// ListDarkRecordings returns recordings with merge_status='dark' older than
// the given grace period (from ended_at). These are night/no-IR segments
// excluded from merge and pending cleanup.
func (d *DB) ListDarkRecordings(ctx context.Context, gracePeriod time.Duration) ([]model.Recording, error) {
	// UTC is mandatory here: ended_at is stored UTC (timeToDB), so a local-time
	// cutoff would sit hours in the future on non-UTC hosts and delete fresh
	// dark segments immediately, voiding the grace period (found by #565 tests).
	cutoff := time.Now().UTC().Add(-gracePeriod).Format("2006-01-02 15:04:05.999999999")
	q := selectRecordingColumns + ` WHERE merge_status = 'dark' AND ended_at IS NOT NULL AND ended_at < ? ORDER BY ended_at ASC LIMIT 500;`
	rows, err := d.readConn().QueryContext(ctx, q, cutoff)
	if err != nil {
		return nil, fmt.Errorf("list dark recordings: %w", err)
	}
	defer rows.Close()
	var res []model.Recording
	for rows.Next() {
		r, err := scanRecordingRow(rows)
		if err != nil {
			return nil, fmt.Errorf("list dark recordings: %w", err)
		}
		res = append(res, *r)
	}
	return res, rows.Err()
}
