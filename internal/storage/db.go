package storage

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"
	"time"
	_ "modernc.org/sqlite"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

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
	_, _ = d.db.ExecContext(ctx, "INSERT OR IGNORE INTO schema_meta (key, value) VALUES ('schema_version', '1');")
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
		log.Printf("[storage] scanTime: failed to parse %q: %v", ns.String, err)
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
	sqlstr := "SELECT id, camera_id, file_path, format, started_at, ended_at, duration, file_size, frame_count, pinned FROM recordings"
	if len(where) > 0 {
		sqlstr += " WHERE " + strings.Join(where, " AND ")
	}
	sqlstr += " ORDER BY started_at DESC"
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
	ID       string `json:"id"`
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	URL      string `json:"url"`
	Enabled  bool   `json:"enabled"`
}

func (d *DB) ListCameras(ctx context.Context) ([]CameraRow, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT id, name, protocol, url, enabled FROM cameras ORDER BY id;`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []CameraRow
	for rows.Next() {
		var c CameraRow
		if err := rows.Scan(&c.ID, &c.Name, &c.Protocol, &c.URL, &c.Enabled); err != nil {
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
	err := d.db.QueryRowContext(ctx, `SELECT id, name, protocol, url, enabled FROM cameras WHERE id = ?`, cameraID).Scan(&c.ID, &c.Name, &c.Protocol, &c.URL, &c.Enabled)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &c, nil
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