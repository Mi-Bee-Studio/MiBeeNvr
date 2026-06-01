package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"
)

// TranscodeTask represents a transcoding task in the database.
type TranscodeTask struct {
	ID              int64          `json:"id"`
	CameraID        string         `json:"camera_id"`
	RecordingID     string         `json:"recording_id"`
	InputPath       string         `json:"input_path"`
	InputFormat     string         `json:"input_format"`
	OutputPath      string         `json:"output_path"`
	OutputFormat    string         `json:"output_format"`
	Status          string         `json:"status"`
	Progress        float64        `json:"progress"`
	Error           sql.NullString `json:"error"`
	CreatedAt       string         `json:"created_at"`
	StartedAt       sql.NullString `json:"started_at"`
	CompletedAt     sql.NullString `json:"completed_at"`
	OriginalDeleted bool           `json:"original_deleted"`
	Framerate       int            `json:"framerate"`
}

// MarshalJSON produces clean JSON for nullable fields.
// sql.NullString marshals as {"String":"...","Valid":true} which breaks API clients.
// Instead, we emit null for invalid values and the raw string for valid ones.
func (t TranscodeTask) MarshalJSON() ([]byte, error) {
	type Alias TranscodeTask
	return json.Marshal(&struct {
		*Alias
		Error       *string `json:"error"`
		StartedAt   *string `json:"started_at"`
		CompletedAt *string `json:"completed_at"`
	}{
		Alias:       (*Alias)(&t),
		Error:       nullStringToPtr(t.Error),
		StartedAt:   nullStringToPtr(t.StartedAt),
		CompletedAt: nullStringToPtr(t.CompletedAt),
	})
}

// EnqueueTask inserts a new pending transcoding task.
func (d *DB) EnqueueTask(ctx context.Context, task *TranscodeTask) error {
	q := `INSERT INTO transcoding_tasks (camera_id, recording_id, input_path, input_format, output_path, output_format, status, progress, created_at, original_deleted, framerate)
		VALUES (?, ?, ?, ?, ?, ?, 'pending', 0, ?, 0, ?);`
	result, err := d.db.ExecContext(ctx, q, task.CameraID, task.RecordingID, task.InputPath, task.InputFormat, task.OutputPath, task.OutputFormat, task.CreatedAt, task.Framerate)
	if err != nil {
		return err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return err
	}
	task.ID = id
	return nil
}

// DequeueTask gets the next pending task ordered by created_at ascending (FIFO),
// atomically claiming it by setting status to 'running'.
// Returns sql.ErrNoRows if no pending tasks exist.
func (d *DB) DequeueTask(ctx context.Context) (*TranscodeTask, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// Atomically claim the oldest pending task
	now := formatTime(time.Now().UTC())
	q := `UPDATE transcoding_tasks SET status = 'running', started_at = COALESCE(started_at, ?)
		WHERE id = (SELECT id FROM transcoding_tasks WHERE status = 'pending' ORDER BY created_at ASC, id ASC LIMIT 1)
		RETURNING id, camera_id, recording_id, input_path, input_format, output_path, output_format,
			status, progress, error, created_at, started_at, completed_at, original_deleted, framerate;`
	row := tx.QueryRowContext(ctx, q, now)
	task := &TranscodeTask{}
	err = row.Scan(
		&task.ID, &task.CameraID, &task.RecordingID,
		&task.InputPath, &task.InputFormat, &task.OutputPath, &task.OutputFormat,
		&task.Status, &task.Progress, &task.Error,
		&task.CreatedAt, &task.StartedAt, &task.CompletedAt, &task.OriginalDeleted, &task.Framerate,
	)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return task, nil
}

// UpdateTaskStatus updates task status, progress, and error message.
// Sets started_at when status becomes 'running'.
// Sets completed_at when status becomes 'completed', 'failed', or 'cancelled'.
func (d *DB) UpdateTaskStatus(ctx context.Context, id int64, status string, progress float64, errMsg string) error {
	now := formatTime(time.Now().UTC())

	var q string
	var args []interface{}

	switch status {
	case "running":
		q = `UPDATE transcoding_tasks SET status = ?, progress = ?, error = ?, started_at = COALESCE(started_at, ?) WHERE id = ?;`
		args = []interface{}{status, progress, errMsg, now, id}
	case "completed", "failed", "cancelled":
		q = `UPDATE transcoding_tasks SET status = ?, progress = ?, error = ?, completed_at = ? WHERE id = ?;`
		args = []interface{}{status, progress, errMsg, now, id}
	default:
		q = `UPDATE transcoding_tasks SET status = ?, progress = ?, error = ? WHERE id = ?;`
		args = []interface{}{status, progress, errMsg, id}
	}

	_, err := d.db.ExecContext(ctx, q, args...)
	return err
}

// GetTasksByStatus returns all tasks with the given status, ordered by created_at ascending.
func (d *DB) GetTasksByStatus(ctx context.Context, status string) ([]TranscodeTask, error) {
	q := `SELECT id, camera_id, recording_id, input_path, input_format, output_path, output_format,
		status, progress, error, created_at, started_at, completed_at, original_deleted, framerate
		FROM transcoding_tasks WHERE status = ? ORDER BY created_at ASC, id ASC;`
	rows, err := d.db.QueryContext(ctx, q, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []TranscodeTask
	for rows.Next() {
		var t TranscodeTask
		var startedAt, completedAt sql.NullString
		if err := rows.Scan(
			&t.ID, &t.CameraID, &t.RecordingID,
			&t.InputPath, &t.InputFormat, &t.OutputPath, &t.OutputFormat,
			&t.Status, &t.Progress, &t.Error,
			&t.CreatedAt, &startedAt, &completedAt, &t.OriginalDeleted, &t.Framerate,
		); err != nil {
			return nil, err
		}
		t.StartedAt = startedAt
		t.CompletedAt = completedAt
		tasks = append(tasks, t)
	}
	return tasks, nil
}

// GetTaskByID returns a task by its ID, or nil if not found.
func (d *DB) GetTaskByID(ctx context.Context, id int64) (*TranscodeTask, error) {
	q := `SELECT id, camera_id, recording_id, input_path, input_format, output_path, output_format,
		status, progress, error, created_at, started_at, completed_at, original_deleted, framerate
		FROM transcoding_tasks WHERE id = ?;`
	row := d.db.QueryRowContext(ctx, q, id)
	task := &TranscodeTask{}
	var startedAt, completedAt sql.NullString
	err := row.Scan(
		&task.ID, &task.CameraID, &task.RecordingID,
		&task.InputPath, &task.InputFormat, &task.OutputPath, &task.OutputFormat,
		&task.Status, &task.Progress, &task.Error,
		&task.CreatedAt, &startedAt, &completedAt, &task.OriginalDeleted, &task.Framerate,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	task.StartedAt = startedAt
	task.CompletedAt = completedAt
	return task, nil
}

// CancelTask sets task status to 'cancelled'.
// Only works on pending or running tasks. Idempotent — cancelling an already-cancelled
// or completed task is a no-op (no error).
func (d *DB) CancelTask(ctx context.Context, id int64) error {
	now := formatTime(time.Now().UTC())
	q := `UPDATE transcoding_tasks SET status = 'cancelled', completed_at = ? WHERE id = ? AND status IN ('pending', 'running');`
	_, err := d.db.ExecContext(ctx, q, now, id)
	return err
}

// DeleteCompletedTasks removes completed, failed, or cancelled tasks whose completed_at
// is older than the given duration. Returns the number of deleted rows.
func (d *DB) DeleteCompletedTasks(ctx context.Context, olderThan time.Duration) (int64, error) {
	cutoff := formatTime(time.Now().UTC().Add(-olderThan))
	q := `DELETE FROM transcoding_tasks WHERE status IN ('completed', 'failed', 'cancelled') AND completed_at < ?;`
	result, err := d.db.ExecContext(ctx, q, cutoff)
	if err != nil {
		return 0, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return n, nil
}

// TranscodeTaskFilter holds filter parameters for listing transcode tasks.
type TranscodeTaskFilter struct {
	Status   string // filter by status (pending, running, completed, failed, cancelled)
	CameraID string // filter by camera_id
	Limit    int    // page size (default 50, max 200)
	Offset   int    // page offset
}

// ListTranscodeTasks returns tasks matching the filter, ordered by created_at descending.
// Also returns the total count of matching tasks for pagination.
func (d *DB) ListTranscodeTasks(ctx context.Context, f TranscodeTaskFilter) ([]TranscodeTask, int, error) {
	if f.Limit <= 0 {
		f.Limit = 50
	}
	if f.Limit > 200 {
		f.Limit = 200
	}

	var where []string
	var args []interface{}

	if f.Status != "" {
		where = append(where, "status = ?")
		args = append(args, f.Status)
	}
	if f.CameraID != "" {
		where = append(where, "camera_id = ?")
		args = append(args, f.CameraID)
	}

	whereClause := ""
	if len(where) > 0 {
		whereClause = " WHERE " + strings.Join(where, " AND ")
	}

	// Count query
	countQ := `SELECT COUNT(*) FROM transcoding_tasks` + whereClause
	var total int
	if err := d.db.QueryRowContext(ctx, countQ, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	// Data query
	dataQ := `SELECT id, camera_id, recording_id, input_path, input_format, output_path, output_format,
		status, progress, error, created_at, started_at, completed_at, original_deleted, framerate
		FROM transcoding_tasks` + whereClause + ` ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`
	rows, err := d.db.QueryContext(ctx, dataQ, append(args, f.Limit, f.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var tasks []TranscodeTask
	for rows.Next() {
		var t TranscodeTask
		var startedAt, completedAt sql.NullString
		if err := rows.Scan(
			&t.ID, &t.CameraID, &t.RecordingID,
			&t.InputPath, &t.InputFormat, &t.OutputPath, &t.OutputFormat,
			&t.Status, &t.Progress, &t.Error,
			&t.CreatedAt, &startedAt, &completedAt, &t.OriginalDeleted, &t.Framerate,
		); err != nil {
			return nil, 0, err
		}
		t.StartedAt = startedAt
		t.CompletedAt = completedAt
		tasks = append(tasks, t)
	}
	return tasks, total, nil
}

// UpdateRecordingFormat updates the format column of a recording after transcoding.
// recordingID corresponds to the recordings.id TEXT PRIMARY KEY.
func (d *DB) UpdateRecordingFormat(ctx context.Context, recordingID string, format string) error {
	q := `UPDATE recordings SET format = ? WHERE id = ?;`
	_, err := d.db.ExecContext(ctx, q, format, recordingID)
	return err
}

// RecoverStuckTasks resets tasks stuck in 'running' status back to 'pending'.
// A task is considered stuck if its started_at is older than the given threshold.
// Returns the number of recovered tasks.
func (d *DB) RecoverStuckTasks(ctx context.Context, threshold time.Duration) (int64, error) {
	cutoff := formatTime(time.Now().UTC().Add(-threshold))
	q := `UPDATE transcoding_tasks SET status = 'pending', progress = 0, started_at = NULL
		WHERE status = 'running' AND started_at < ?;`
	result, err := d.db.ExecContext(ctx, q, cutoff)
	if err != nil {
		return 0, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return n, nil
}
