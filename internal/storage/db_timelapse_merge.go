package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// TimelapseMergeFilter holds query parameters for listing timelapse merges.
type TimelapseMergeFilter struct {
	CameraID      string
	StartTime     time.Time // inclusive lower bound on window_start
	EndTime       time.Time // inclusive upper bound on window_start
	DurationLabel string    // optional exact match (e.g. "24h", "natural-day")
	Status        string    // optional exact match (pending/merging/completed/failed)
	Limit         int
	Offset        int
}

// InsertTimelapseMerge creates a new timelapse_merges row and returns its id.
// CreatedAt is set to now (UTC) if zero. Status defaults to "pending" at the
// SQL layer; callers may override via m.Status.
func (d *DB) InsertTimelapseMerge(ctx context.Context, m *model.TimelapseMerge) (int64, error) {
	status := m.Status
	if status == "" {
		status = model.TimelapseMergeStatusPending
	}
	createdAt := m.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	q := `INSERT INTO timelapse_merges (
		camera_id, window_start, window_end, duration_label, output_path,
		file_size, frame_count, codec, fps, status, error, source_segment_ids,
		created_at, completed_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);`
	result, err := d.db.ExecContext(ctx, q,
		m.CameraID, timeToDB(m.WindowStart), timeToDB(m.WindowEnd),
		m.DurationLabel, m.OutputPath, m.FileSize, m.FrameCount,
		m.Codec, m.FPS, status, m.Error, m.SourceSegmentIDs,
		timeToDB(createdAt), timeToDB(m.CompletedAt),
	)
	if err != nil {
		return 0, fmt.Errorf("insert timelapse merge: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	m.ID = id
	m.Status = status
	m.CreatedAt = createdAt
	return id, nil
}

// UpdateTimelapseMergeStatus updates status, error, and completed_at for a
// single timelapse merge. When status is "completed", completed_at is set to
// now (UTC) unless already set.
func (d *DB) UpdateTimelapseMergeStatus(ctx context.Context, id int64, status, errMsg string) error {
	completedAt := any(nil)
	if status == model.TimelapseMergeStatusCompleted {
		completedAt = timeToDB(time.Now().UTC())
	}
	q := `UPDATE timelapse_merges SET status=?, error=?, completed_at=COALESCE(?, completed_at) WHERE id=?;`
	_, err := d.db.ExecContext(ctx, q, status, errMsg, completedAt, id)
	if err != nil {
		return fmt.Errorf("update timelapse merge status: %w", err)
	}
	return nil
}

// CompleteTimelapseMerge marks a timelapse merge as completed and records the
// final output metadata (path, size, frame count, codec, source segments).
// CompletedAt is set to now (UTC). This is the success-path update — use
// UpdateTimelapseMergeStatus for failure.
func (d *DB) CompleteTimelapseMerge(ctx context.Context, id int64, outputPath string, fileSize int64, frameCount int, codec string, sourceSegmentIDs string) error {
	q := `UPDATE timelapse_merges
		SET status=?, output_path=?, file_size=?, frame_count=?, codec=?, source_segment_ids=?, error='', completed_at=?
		WHERE id=?;`
	_, err := d.db.ExecContext(ctx, q,
		model.TimelapseMergeStatusCompleted,
		outputPath, fileSize, frameCount, codec, sourceSegmentIDs,
		timeToDB(time.Now().UTC()),
		id,
	)
	if err != nil {
		return fmt.Errorf("complete timelapse merge: %w", err)
	}
	return nil
}

// GetTimelapseMerge returns a single timelapse merge by id.
func (d *DB) GetTimelapseMerge(ctx context.Context, id int64) (*model.TimelapseMerge, error) {
	q := selectTimelapseMergeColumns + ` FROM timelapse_merges WHERE id=?`
	row := d.readConn().QueryRowContext(ctx, q, id)
	m, err := scanTimelapseMerge(row)
	if err != nil {
		return nil, fmt.Errorf("get timelapse merge: %w", err)
	}
	return m, nil
}

// FindTimelapseMergeByWindow returns the merge row for a given camera + window
// start + duration label, or (nil, nil) if none exists. Used by the periodic
// merger to decide whether to upsert (re-run) or insert fresh.
func (d *DB) FindTimelapseMergeByWindow(ctx context.Context, cameraID string, windowStart time.Time, durationLabel string) (*model.TimelapseMerge, error) {
	q := selectTimelapseMergeColumns + ` FROM timelapse_merges WHERE camera_id=? AND window_start=? AND duration_label=? LIMIT 1`
	row := d.readConn().QueryRowContext(ctx, q, cameraID, timeToDB(windowStart), durationLabel)
	m, err := scanTimelapseMerge(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find timelapse merge by window: %w", err)
	}
	return m, nil
}

// ListTimelapseMerges returns a filtered, paginated list of timelapse merges.
func (d *DB) ListTimelapseMerges(ctx context.Context, f TimelapseMergeFilter) ([]model.TimelapseMerge, error) {
	whereClause, args := buildTimelapseMergeWhere(f)
	q := selectTimelapseMergeColumns + ` FROM timelapse_merges` + whereClause + ` ORDER BY window_start DESC`
	if f.Limit > 0 {
		q += ` LIMIT ?`
		args = append(args, f.Limit)
		if f.Offset > 0 {
			q += ` OFFSET ?`
			args = append(args, f.Offset)
		}
	}
	rows, err := d.readConn().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list timelapse merges: %w", err)
	}
	defer rows.Close()
	var out []model.TimelapseMerge
	for rows.Next() {
		m, err := scanTimelapseMerge(rows)
		if err != nil {
			return nil, fmt.Errorf("scan timelapse merge: %w", err)
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

// CountTimelapseMerges returns the total count of merges matching the filter
// (ignoring Limit/Offset).
func (d *DB) CountTimelapseMerges(ctx context.Context, f TimelapseMergeFilter) (int, error) {
	whereClause, args := buildTimelapseMergeWhere(f)
	q := `SELECT COUNT(*) FROM timelapse_merges` + whereClause
	var total int
	if err := d.readConn().QueryRowContext(ctx, q, args...).Scan(&total); err != nil {
		return 0, fmt.Errorf("count timelapse merges: %w", err)
	}
	return total, nil
}

// DeleteTimelapseMerge removes a single timelapse merge row by id.
// Does NOT delete the output file on disk — the caller is responsible for that
// (mirror the recordings delete pattern: DB-first, then os.Remove).
func (d *DB) DeleteTimelapseMerge(ctx context.Context, id int64) error {
	_, err := d.db.ExecContext(ctx, `DELETE FROM timelapse_merges WHERE id=?`, id)
	if err != nil {
		return fmt.Errorf("delete timelapse merge: %w", err)
	}
	return nil
}

// selectTimelapseMergeColumns is the canonical column list for SELECT queries.
// Kept in sync with scanTimelapseMerge.
const selectTimelapseMergeColumns = `SELECT id, camera_id, window_start, window_end, duration_label,
	output_path, file_size, frame_count, codec, fps, status, error, source_segment_ids,
	created_at, completed_at`

// scanner is satisfied by both *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

func scanTimelapseMerge(s scanner) (*model.TimelapseMerge, error) {
	var m model.TimelapseMerge
	var windowStart, windowEnd, createdAt, completedAt sql.NullString
	if err := s.Scan(
		&m.ID, &m.CameraID, &windowStart, &windowEnd, &m.DurationLabel,
		&m.OutputPath, &m.FileSize, &m.FrameCount, &m.Codec, &m.FPS,
		&m.Status, &m.Error, &m.SourceSegmentIDs,
		&createdAt, &completedAt,
	); err != nil {
		return nil, err
	}
	m.WindowStart = scanTime(windowStart)
	m.WindowEnd = scanTime(windowEnd)
	m.CreatedAt = scanTime(createdAt)
	m.CompletedAt = scanTime(completedAt)
	return &m, nil
}

func buildTimelapseMergeWhere(f TimelapseMergeFilter) (string, []any) {
	var clauses []string
	var args []any
	if f.CameraID != "" {
		clauses = append(clauses, "camera_id=?")
		args = append(args, f.CameraID)
	}
	if !f.StartTime.IsZero() {
		clauses = append(clauses, "window_start>=?")
		args = append(args, timeToDB(f.StartTime))
	}
	if !f.EndTime.IsZero() {
		clauses = append(clauses, "window_start<=?")
		args = append(args, timeToDB(f.EndTime))
	}
	if f.DurationLabel != "" {
		clauses = append(clauses, "duration_label=?")
		args = append(args, f.DurationLabel)
	}
	if f.Status != "" {
		clauses = append(clauses, "status=?")
		args = append(args, f.Status)
	}
	if len(clauses) == 0 {
		return "", nil
	}
	whereClause := " WHERE " + strings.Join(clauses, " AND ")
	return whereClause, args
}
