package storage

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"
	_ "modernc.org/sqlite"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

var logger = slog.Default().With("component", "storage")

type DB struct {
	path string
	db   *sql.DB
}

// DB returns the underlying *sql.DB for advanced queries.
func (d *DB) DB() *sql.DB {
	return d.db
}

func New(dbPath string) (*DB, error) {
	dsn := dbPath
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// Set pragmas on open
	if _, err := db.Exec("PRAGMA journal_mode=WAL;"); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec("PRAGMA synchronous=NORMAL;"); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec("PRAGMA busy_timeout=5000;"); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec("PRAGMA cache_size=-2000;"); err != nil {
		db.Close()
		return nil, err
	}
	return &DB{path: dbPath, db: db}, nil
}

func (d *DB) Init(ctx context.Context) error {
	// create tables if not exist
	camSQL := `CREATE TABLE IF NOT EXISTS cameras (
        id TEXT PRIMARY KEY,
        name TEXT NOT NULL,
        protocol TEXT NOT NULL,
        url TEXT NOT NULL,
        username TEXT DEFAULT '',
        password TEXT DEFAULT '',
        enabled INTEGER DEFAULT 1,
        description TEXT DEFAULT '',
        location TEXT DEFAULT '',
        brand TEXT DEFAULT '',
        model TEXT DEFAULT '',
        serial_number TEXT DEFAULT '',
        created_at DATETIME DEFAULT CURRENT_TIMESTAMP
    );`
	recSQL := `CREATE TABLE IF NOT EXISTS recordings (
        id TEXT PRIMARY KEY,
        camera_id TEXT NOT NULL,
        file_path TEXT NOT NULL,
        format TEXT NOT NULL,
        started_at DATETIME NOT NULL,
        ended_at DATETIME,
        duration REAL,
        file_size INTEGER DEFAULT 0,
        frame_count INTEGER DEFAULT 0,
        pinned INTEGER DEFAULT 0,
        created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
        FOREIGN KEY (camera_id) REFERENCES cameras(id)
    );`
	if _, err := d.db.ExecContext(ctx, camSQL); err != nil {
		return err
	}
	if _, err := d.db.ExecContext(ctx, recSQL); err != nil {
		return err
	}
	// indices
	idx1 := `CREATE INDEX IF NOT EXISTS idx_recordings_camera ON recordings(camera_id);`
	idx2 := `CREATE INDEX IF NOT EXISTS idx_recordings_time ON recordings(started_at);`
	idx3 := `CREATE INDEX IF NOT EXISTS idx_recordings_pinned ON recordings(pinned);`
	if _, err := d.db.ExecContext(ctx, idx1); err != nil { return err }
	if _, err := d.db.ExecContext(ctx, idx2); err != nil { return err }
	if _, err := d.db.ExecContext(ctx, idx3); err != nil { return err }
	// schema metadata
	metaSQL := `CREATE TABLE IF NOT EXISTS schema_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);`
	if _, err := d.db.ExecContext(ctx, metaSQL); err != nil { return err }
	_, _ = d.db.ExecContext(ctx, "INSERT OR IGNORE INTO schema_meta (key, value) VALUES ('schema_version', '2');")
	// Migration v1 → v2: add camera metadata columns
	var version string
	if err := d.db.QueryRowContext(ctx, "SELECT value FROM schema_meta WHERE key='schema_version'").Scan(&version); err == nil && version == "1" {
		columns := []string{
			"ALTER TABLE cameras ADD COLUMN description TEXT DEFAULT ''",
			"ALTER TABLE cameras ADD COLUMN location TEXT DEFAULT ''",
			"ALTER TABLE cameras ADD COLUMN brand TEXT DEFAULT ''",
			"ALTER TABLE cameras ADD COLUMN model TEXT DEFAULT ''",
			"ALTER TABLE cameras ADD COLUMN serial_number TEXT DEFAULT ''",
		}
		for _, col := range columns {
			_, _ = d.db.ExecContext(ctx, col) // ignore error if column already exists
		}
		_, _ = d.db.ExecContext(ctx, "UPDATE schema_meta SET value='2' WHERE key='schema_version'")
	}
	// Migration v2 → v3: add per-camera retention_days
	if version == "2" {
		_, _ = d.db.ExecContext(ctx, "ALTER TABLE cameras ADD COLUMN retention_days INTEGER DEFAULT 0")
		_, _ = d.db.ExecContext(ctx, "UPDATE schema_meta SET value='3' WHERE key='schema_version'")
	}
	return nil
}

func (d *DB) Close() error {
	if d == nil || d.db == nil {
		return nil
	}
	return d.db.Close()
}

// Backup creates a backup of the database using VACUUM INTO.
func (d *DB) Backup(ctx context.Context, destPath string) error {
	_, err := d.db.ExecContext(ctx, "VACUUM INTO ?", destPath)
	return err
}

// sqliteTimeFormat is the format used to store timestamps in SQLite.
// Uses UTC without timezone suffix, compatible with SQLite's datetime() for string comparison.
const sqliteTimeFormat = "2006-01-02 15:04:05.999999999"

// timeToDB converts time.Time to a SQLite-compatible string value.
// Returns nil for zero time (which SQLite stores as NULL).
func timeToDB(t time.Time) interface{} {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(sqliteTimeFormat)
}

// formatTime formats a time.Time as a SQLite-compatible UTC string.
// Returns empty string for zero time.
func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(sqliteTimeFormat)
}

// parseTime parses a SQLite timestamp string back into time.Time (UTC).
// Supports multiple formats for backward compatibility with legacy data.
func parseTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	// Canonical format (our new format)
	if t, err := time.Parse(sqliteTimeFormat, s); err == nil {
		return t, nil
	}
	// Without fractional seconds (SQLite CURRENT_TIMESTAMP)
	if t, err := time.Parse("2006-01-02 15:04:05", s); err == nil {
		return t, nil
	}
	// RFC3339 variants
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	// Legacy Go time.Time.String() format with monotonic clock:
	// "2006-01-02 15:04:05.999999999 -0700 MST m=+123.456"
	cleaned := s
	if idx := strings.Index(cleaned, " m=+"); idx != -1 {
		cleaned = cleaned[:idx]
	}
	// Strip timezone name (e.g., "CST") after offset: "+0800 CST" → "+0800"
	fields := strings.Fields(cleaned)
	if len(fields) >= 4 && len(fields[2]) == 5 && (fields[2][0] == '+' || fields[2][0] == '-') {
		cleaned = fields[0] + " " + fields[1] + " " + fields[2]
	}
	for _, layout := range []string{
		"2006-01-02 15:04:05.999999999 -0700",
		"2006-01-02 15:04:05 -0700",
	} {
		if t, err := time.Parse(layout, cleaned); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("cannot parse time: %q", s)
}

// scanTime converts a sql.NullString to time.Time using parseTime.
// Returns zero time for NULL or empty values.
func scanTime(ns sql.NullString) time.Time {
	if !ns.Valid || ns.String == "" {
		return time.Time{}
	}
	t, err := parseTime(ns.String)
	if err != nil {
		logger.Warn("scanTime: failed to parse time string", "value", ns.String, "error", err)
		return time.Time{}
	}
	return t
}

func (d *DB) InsertRecording(ctx context.Context, r *model.Recording) error {
	q := `INSERT INTO recordings(id, camera_id, file_path, format, started_at, ended_at, duration, file_size, frame_count, pinned) VALUES(?,?,?,?,?,?,?,?,?,?);`
	_, err := d.db.ExecContext(ctx, q, r.ID, r.CameraID, r.FilePath, r.Format, timeToDB(r.StartedAt), timeToDB(r.EndedAt), r.Duration, r.FileSize, r.FrameCount, r.Pinned)
	return err
}

func (d *DB) UpdateRecording(ctx context.Context, r *model.Recording) error {
	q := `UPDATE recordings SET camera_id=?, file_path=?, format=?, started_at=?, ended_at=?, duration=?, file_size=?, frame_count=?, pinned=? WHERE id=?;`
	_, err := d.db.ExecContext(ctx, q, r.CameraID, r.FilePath, r.Format, timeToDB(r.StartedAt), timeToDB(r.EndedAt), r.Duration, r.FileSize, r.FrameCount, r.Pinned, r.ID)
	return err
}

func (d *DB) GetRecording(ctx context.Context, id string) (*model.Recording, error) {
	row := d.db.QueryRowContext(ctx, `SELECT id, camera_id, file_path, format, started_at, ended_at, duration, file_size, frame_count, pinned FROM recordings WHERE id=?;`, id)
	var r model.Recording
	var startedAtStr, endedAtStr sql.NullString
	if err := row.Scan(&r.ID, &r.CameraID, &r.FilePath, &r.Format, &startedAtStr, &endedAtStr, &r.Duration, &r.FileSize, &r.FrameCount, &r.Pinned); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	r.StartedAt = scanTime(startedAtStr)
	r.EndedAt = scanTime(endedAtStr)
	return &r, nil
}

func (d *DB) ListRecordings(ctx context.Context, filter model.RecordingFilter) ([]model.Recording, error) {
	where := []string{}
	args := []any{}
	if filter.CameraID != "" {
		where = append(where, "camera_id=?"); args = append(args, filter.CameraID)
	}
	if filter.Pinned != nil {
		where = append(where, "pinned=?"); args = append(args, *filter.Pinned)
	}
	if !filter.StartTime.IsZero() {
		where = append(where, "started_at>=?"); args = append(args, formatTime(filter.StartTime))
	}
	if !filter.EndTime.IsZero() {
		where = append(where, "started_at<=?"); args = append(args, formatTime(filter.EndTime))
	}
	if filter.Format != "" {
		where = append(where, "format=?"); args = append(args, filter.Format)
	}
	if filter.Search != "" {
		escaped := strings.ReplaceAll(filter.Search, "\\", "\\\\")
		escaped = strings.ReplaceAll(escaped, "%", "\\%")
		escaped = strings.ReplaceAll(escaped, "_", "\\_")
		pattern := "%" + escaped + "%"
		where = append(where, "(camera_id LIKE ? ESCAPE '\\' OR format LIKE ? ESCAPE '\\' OR file_path LIKE ? ESCAPE '\\')")
		args = append(args, pattern, pattern, pattern)
	}
	sqlstr := "SELECT id, camera_id, file_path, format, started_at, ended_at, duration, file_size, frame_count, pinned FROM recordings"
	if len(where) > 0 {
		sqlstr += " WHERE " + strings.Join(where, " AND ")
	}
	// Build ORDER BY clause from filter (whitelisted columns only)
	allowedSortFields := map[string]bool{"started_at": true, "duration": true, "file_size": true, "camera_id": true}
	sortBy := "started_at"
	if filter.SortBy != "" && allowedSortFields[filter.SortBy] {
		sortBy = filter.SortBy
	}
	sortOrder := "DESC"
	if strings.EqualFold(filter.SortOrder, "asc") {
		sortOrder = "ASC"
	}
	sqlstr += " ORDER BY " + sortBy + " " + sortOrder
	if filter.Limit > 0 {
		sqlstr += fmt.Sprintf(" LIMIT %d", filter.Limit)
	}
	if filter.Offset > 0 {
		sqlstr += fmt.Sprintf(" OFFSET %d", filter.Offset)
	}
	sqlstr += ";"
	rows, err := d.db.QueryContext(ctx, sqlstr, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []model.Recording
	for rows.Next() {
		var r model.Recording
		var startedAtStr, endedAtStr sql.NullString
		if err := rows.Scan(&r.ID, &r.CameraID, &r.FilePath, &r.Format, &startedAtStr, &endedAtStr, &r.Duration, &r.FileSize, &r.FrameCount, &r.Pinned); err != nil {
			return nil, err
		}
		r.StartedAt = scanTime(startedAtStr)
		r.EndedAt = scanTime(endedAtStr)
		res = append(res, r)
	}
	return res, nil
}

func (d *DB) CountRecordingsWithFilter(ctx context.Context, filter model.RecordingFilter) (int, error) {
	where := []string{}
	args := []any{}
	if filter.CameraID != "" {
		where = append(where, "camera_id=?"); args = append(args, filter.CameraID)
	}
	if filter.Pinned != nil {
		where = append(where, "pinned=?"); args = append(args, *filter.Pinned)
	}
	if !filter.StartTime.IsZero() {
		where = append(where, "started_at>=?"); args = append(args, formatTime(filter.StartTime))
	}
	if !filter.EndTime.IsZero() {
		where = append(where, "started_at<=?"); args = append(args, formatTime(filter.EndTime))
	}
	if filter.Format != "" {
		where = append(where, "format=?"); args = append(args, filter.Format)
	}
	if filter.Search != "" {
		escaped := strings.ReplaceAll(filter.Search, "\\", "\\\\")
		escaped = strings.ReplaceAll(escaped, "%", "\\%")
		escaped = strings.ReplaceAll(escaped, "_", "\\_")
		pattern := "%" + escaped + "%"
		where = append(where, "(camera_id LIKE ? ESCAPE '\\' OR format LIKE ? ESCAPE '\\' OR file_path LIKE ? ESCAPE '\\')")
		args = append(args, pattern, pattern, pattern)
	}
	sqlstr := "SELECT COUNT(*) FROM recordings"
	if len(where) > 0 {
		sqlstr += " WHERE " + strings.Join(where, " AND ")
	}
	var count int
	err := d.db.QueryRowContext(ctx, sqlstr, args...).Scan(&count)
	return count, err
}

func (d *DB) DeleteRecording(ctx context.Context, id string) error {
	_, err := d.db.ExecContext(ctx, `DELETE FROM recordings WHERE id=?;`, id)
	return err
}

// DeleteRecordingsBatch deletes multiple recordings by ID using a transaction.
// Returns a slice of IDs that were successfully deleted.
func (d *DB) DeleteRecordingsBatch(ctx context.Context, ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	deleted := []string{}
	for _, id := range ids {
		res, err := tx.ExecContext(ctx, `DELETE FROM recordings WHERE id=?;`, id)
		if err != nil {
			logger.Warn("batch delete: failed to delete recording", "id", id, "error", err)
			continue
		}
		n, _ := res.RowsAffected()
		if n > 0 {
			deleted = append(deleted, id)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return deleted, nil
}

func (d *DB) SetPinned(ctx context.Context, id string, pinned bool) error {
	val := 0
	if pinned {
		val = 1
	}
	_, err := d.db.ExecContext(ctx, `UPDATE recordings SET pinned=? WHERE id=?;`, val, id)
	return err
}

func (d *DB) CleanupIncomplete(ctx context.Context) error {
	_, err := d.db.ExecContext(ctx, `DELETE FROM recordings WHERE ended_at IS NULL;`)
	return err
}

func (d *DB) ListExpiredRecordings(ctx context.Context, retentionDays int) ([]model.Recording, error) {
	sqlstr := `SELECT id, camera_id, file_path, format, started_at, ended_at, duration, file_size, frame_count, pinned FROM recordings WHERE pinned=0 AND ended_at IS NOT NULL AND ended_at < datetime('now', '-' || ? || ' days') ORDER BY ended_at ASC;`
	rows, err := d.db.QueryContext(ctx, sqlstr, retentionDays)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []model.Recording
	for rows.Next() {
		var r model.Recording
		var startedAtStr, endedAtStr sql.NullString
		if err := rows.Scan(&r.ID, &r.CameraID, &r.FilePath, &r.Format, &startedAtStr, &endedAtStr, &r.Duration, &r.FileSize, &r.FrameCount, &r.Pinned); err != nil {
			return nil, err
		}
		r.StartedAt = scanTime(startedAtStr)
		r.EndedAt = scanTime(endedAtStr)
		res = append(res, r)
	}
	return res, nil
}

// ListExpiredRecordingsByCamera returns expired recordings for a specific camera
func (d *DB) ListExpiredRecordingsByCamera(ctx context.Context, cameraID string, retentionDays int) ([]model.Recording, error) {
	sqlstr := `SELECT id, camera_id, file_path, format, started_at, ended_at, duration, file_size, frame_count, pinned FROM recordings WHERE pinned=0 AND ended_at IS NOT NULL AND camera_id=? AND ended_at < datetime('now', '-' || ? || ' days') ORDER BY ended_at ASC;`
	rows, err := d.db.QueryContext(ctx, sqlstr, cameraID, retentionDays)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []model.Recording
	for rows.Next() {
		var r model.Recording
		var startedAtStr, endedAtStr sql.NullString
		if err := rows.Scan(&r.ID, &r.CameraID, &r.FilePath, &r.Format, &startedAtStr, &endedAtStr, &r.Duration, &r.FileSize, &r.FrameCount, &r.Pinned); err != nil {
			return nil, err
		}
		r.StartedAt = scanTime(startedAtStr)
		r.EndedAt = scanTime(endedAtStr)
		res = append(res, r)
	}
	return res, nil
}

func (d *DB) ListOldestUnpinnedRecordings(ctx context.Context, limit int) ([]model.Recording, error) {
	sqlstr := `SELECT id, camera_id, file_path, format, started_at, ended_at, duration, file_size, frame_count, pinned FROM recordings WHERE pinned=0 AND ended_at IS NOT NULL ORDER BY ended_at ASC LIMIT ?;`
	rows, err := d.db.QueryContext(ctx, sqlstr, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []model.Recording
	for rows.Next() {
		var r model.Recording
		var startedAtStr, endedAtStr sql.NullString
		if err := rows.Scan(&r.ID, &r.CameraID, &r.FilePath, &r.Format, &startedAtStr, &endedAtStr, &r.Duration, &r.FileSize, &r.FrameCount, &r.Pinned); err != nil {
			return nil, err
		}
		r.StartedAt = scanTime(startedAtStr)
		r.EndedAt = scanTime(endedAtStr)
		res = append(res, r)
	}
	return res, nil
}

type CameraRow struct {
	ID           string               `json:"id"`
	Name         string               `json:"name"`
	Protocol     string               `json:"protocol"`
	URL          string               `json:"url"`
	Enabled      bool                 `json:"enabled"`
	Description  string               `json:"description"`
	Location     string               `json:"location"`
	Brand        string               `json:"brand"`
	Model        string               `json:"model"`
	SerialNumber string               `json:"serial_number"`
	RetentionDays int                 `json:"retention_days"`
	Status       model.RecorderStatus `json:"status"`
	LastSeen     *time.Time           `json:"last_seen,omitempty"`
	}


func (d *DB) ListCameras(ctx context.Context) ([]CameraRow, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT id, name, protocol, url, enabled, description, location, brand, model, serial_number, retention_days FROM cameras ORDER BY id;`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []CameraRow
	for rows.Next() {
		var c CameraRow
		if err := rows.Scan(&c.ID, &c.Name, &c.Protocol, &c.URL, &c.Enabled, &c.Description, &c.Location, &c.Brand, &c.Model, &c.SerialNumber, &c.RetentionDays); err != nil {
			return nil, err
		}
		res = append(res, c)
	}
	return res, nil
}

func (d *DB) CountRecordings(ctx context.Context) (int, error) {
	var count int
	err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recordings;`).Scan(&count)
	return count, err
}

// UpsertCamera inserts or updates a camera record in the database

func (d *DB) UpsertCamera(ctx context.Context, id, name, protocol, url, username, password string, enabled bool) error {

    q := `INSERT INTO cameras(id, name, protocol, url, username, password, enabled) VALUES(?,?,?,?,?,?,?)

         ON CONFLICT(id) DO UPDATE SET name=excluded.name, protocol=excluded.protocol, url=excluded.url, username=excluded.username, password=excluded.password, enabled=excluded.enabled;`

    _, err := d.db.ExecContext(ctx, q, id, name, protocol, url, username, password, enabled)

	return err
}

func (d *DB) GetCamera(ctx context.Context, cameraID string) (*CameraRow, error) {
	var c CameraRow
	err := d.db.QueryRowContext(ctx, `SELECT id, name, protocol, url, enabled, description, location, brand, model, serial_number, retention_days FROM cameras WHERE id = ?`, cameraID).Scan(&c.ID, &c.Name, &c.Protocol, &c.URL, &c.Enabled, &c.Description, &c.Location, &c.Brand, &c.Model, &c.SerialNumber, &c.RetentionDays)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &c, nil
}

// DeleteCamera removes a camera record from the database.
// Returns an error if the camera does not exist.
func (d *DB) DeleteCamera(ctx context.Context, cameraID string) error {
	res, err := d.db.ExecContext(ctx, `DELETE FROM cameras WHERE id = ?;`, cameraID)
	if err != nil {
		return err
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// UpdateCameraMetadata updates DB-only metadata fields for a camera.
func (d *DB) UpdateCameraMetadata(ctx context.Context, id, description, location, brand, model, serialNumber string, retentionDays int) error {
	q := `UPDATE cameras SET description=?, location=?, brand=?, model=?, serial_number=?, retention_days=? WHERE id=?;`
	_, err := d.db.ExecContext(ctx, q, description, location, brand, model, serialNumber, retentionDays, id)
	return err
}

// GetRecordingTrends returns daily aggregated recording statistics.
// Days defaults to 7, clamped to [1, 30].
func (d *DB) GetRecordingTrends(ctx context.Context, days int) ([]model.DailyStats, error) {
	if days <= 0 {
		days = 7
	}
	if days > 30 {
		days = 30
	}
	cutoff := time.Now().AddDate(0, 0, -days).UTC()
	
	query := `SELECT DATE(r.started_at) as date, COUNT(*) as recordings, SUM(r.file_size) as total_size, r.camera_id, COALESCE(c.name, r.camera_id) as camera_name
		FROM recordings r LEFT JOIN cameras c ON r.camera_id = c.id
		WHERE r.started_at >= ?
		GROUP BY DATE(r.started_at), r.camera_id
		ORDER BY date`
	
	rows, err := d.db.QueryContext(ctx, query, formatTime(cutoff))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	
	// Aggregate per-camera rows into per-date stats
	dateIndex := make(map[string]int) // date -> index into result slice
	var result []model.DailyStats
	
	for rows.Next() {
		var date string
		var count int
		var totalSize int64
		var cameraID, cameraName string
		if err := rows.Scan(&date, &count, &totalSize, &cameraID, &cameraName); err != nil {
			return nil, err
		}
		idx, ok := dateIndex[date]
		if !ok {
			idx = len(result)
			dateIndex[date] = idx
			result = append(result, model.DailyStats{
				Date:         date,
				CameraCounts: make(map[string]int),
			})
		}
		result[idx].Recordings += count
		result[idx].TotalSize += totalSize
		result[idx].CameraCounts[cameraName] += count
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if result == nil {
		result = []model.DailyStats{}
	}
	return result, nil
}

// GetLastRecordingTime returns the most recent ended_at for a camera.
func (d *DB) GetLastRecordingTime(ctx context.Context, cameraID string) (*time.Time, error) {
	var endedAtStr sql.NullString
	err := d.db.QueryRowContext(ctx, "SELECT MAX(ended_at) FROM recordings WHERE camera_id=? AND ended_at IS NOT NULL", cameraID).Scan(&endedAtStr)
	if err != nil {
		return nil, err
	}
	if !endedAtStr.Valid || endedAtStr.String == "" {
		return nil, nil
	}
	t := scanTime(endedAtStr)
	return &t, nil
}

// GetAllLastRecordingTimes returns the last recording time for each camera.
func (d *DB) GetAllLastRecordingTimes(ctx context.Context) (map[string]*time.Time, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT camera_id, MAX(ended_at) as last_ended FROM recordings WHERE ended_at IS NOT NULL GROUP BY camera_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]*time.Time)
	for rows.Next() {
		var cameraID string
		var endedAtStr sql.NullString
		if err := rows.Scan(&cameraID, &endedAtStr); err != nil {
			return nil, err
		}
		if endedAtStr.Valid && endedAtStr.String != "" {
			t := scanTime(endedAtStr)
			result[cameraID] = &t
		}
	}
	return result, nil
}

// MergeWindow represents a group of consecutive recordings eligible for merging.
type MergeWindow struct {
	StartTime    time.Time `json:"start_time"`
	EndTime      time.Time `json:"end_time"`
	SegmentCount int      `json:"segment_count"`
	Format       string   `json:"format"`
}

// ListMergeableSegments returns recordings for a camera within a time window,
// excluding pinned and incomplete segments.
func (d *DB) ListMergeableSegments(ctx context.Context, cameraID string, windowStart, windowEnd time.Time) ([]*model.Recording, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT id, camera_id, file_path, format, started_at, ended_at, duration, file_size, frame_count, pinned FROM recordings WHERE camera_id = ? AND pinned = 0 AND ended_at IS NOT NULL AND started_at >= ? AND started_at < ? ORDER BY started_at ASC;`,
		cameraID, formatTime(windowStart), formatTime(windowEnd))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []*model.Recording
	for rows.Next() {
		var r model.Recording
		var startedAtStr, endedAtStr sql.NullString
		if err := rows.Scan(&r.ID, &r.CameraID, &r.FilePath, &r.Format, &startedAtStr, &endedAtStr, &r.Duration, &r.FileSize, &r.FrameCount, &r.Pinned); err != nil {
			return nil, err
		}
		r.StartedAt = scanTime(startedAtStr)
		r.EndedAt = scanTime(endedAtStr)
		res = append(res, &r)
	}
	return res, nil
}

// ListCameraMergeWindows returns hourly merge windows for a camera with 2+ segments.
// Only includes recordings older than minAge.
func (d *DB) ListCameraMergeWindows(ctx context.Context, cameraID string, minAge time.Duration) ([]MergeWindow, error) {
	cutoff := time.Now().Add(-minAge).Format(sqliteTimeFormat)
	query := `SELECT strftime('%Y-%m-%d %H', started_at) as hour, MIN(started_at), MAX(ended_at), COUNT(*), format FROM recordings WHERE camera_id = ? AND pinned = 0 AND ended_at IS NOT NULL AND ended_at < ? GROUP BY hour, format HAVING COUNT(*) >= 2 ORDER BY hour ASC;`
	rows, err := d.db.QueryContext(ctx, query, cameraID, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []MergeWindow
	for rows.Next() {
		var w MergeWindow
		var hourStr, minStart, maxEnd sql.NullString
		if err := rows.Scan(&hourStr, &minStart, &maxEnd, &w.SegmentCount, &w.Format); err != nil {
			return nil, err
		}
		w.StartTime = scanTime(minStart)
		w.EndTime = scanTime(maxEnd)
		res = append(res, w)
	}
	return res, nil
}
