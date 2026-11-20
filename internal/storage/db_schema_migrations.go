package storage

// Idempotent column migrations: every ensure* function adds a column the
// running schema may predate. Split out of db.go so Init() stays readable.

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"
)

// ensureRecordingsMotionColumns adds the motion-score columns (issue #435) to
// recordings tables created before schema v33. Idempotent: checks pragma
// table_info before each ALTER, so re-running on a current database is a no-op.
func (d *DB) ensureRecordingsMotionColumns(ctx context.Context) error {
	var motionColExists int
	if err := d.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('recordings') WHERE name='motion_score'`,
	).Scan(&motionColExists); err != nil {
		return fmt.Errorf("check motion_score column: %w", err)
	}
	if motionColExists == 0 {
		if _, err := d.db.ExecContext(ctx,
			`ALTER TABLE recordings ADD COLUMN motion_score REAL DEFAULT -1`); err != nil {
			return fmt.Errorf("add motion_score column: %w", err)
		}
	}
	var flagsColExists int
	if err := d.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('recordings') WHERE name='activity_flags'`,
	).Scan(&flagsColExists); err != nil {
		return fmt.Errorf("check activity_flags column: %w", err)
	}
	if flagsColExists == 0 {
		if _, err := d.db.ExecContext(ctx,
			`ALTER TABLE recordings ADD COLUMN activity_flags TEXT DEFAULT ''`); err != nil {
			return fmt.Errorf("add activity_flags column: %w", err)
		}
	}
	var mapColExists int
	if err := d.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('recordings') WHERE name='timeline_map'`,
	).Scan(&mapColExists); err != nil {
		return fmt.Errorf("check timeline_map column: %w", err)
	}
	if mapColExists == 0 {
		if _, err := d.db.ExecContext(ctx,
			`ALTER TABLE recordings ADD COLUMN timeline_map TEXT DEFAULT ''`); err != nil {
			return fmt.Errorf("add timeline_map column: %w", err)
		}
	}
	var confColExists int
	if err := d.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('recordings') WHERE name='motion_confidence'`,
	).Scan(&confColExists); err != nil {
		return fmt.Errorf("check motion_confidence column: %w", err)
	}
	if confColExists == 0 {
		if _, err := d.db.ExecContext(ctx,
			`ALTER TABLE recordings ADD COLUMN motion_confidence REAL DEFAULT -1`); err != nil {
			return fmt.Errorf("add motion_confidence column: %w", err)
		}
	}
	var layerColExists int
	if err := d.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('recordings') WHERE name='layer'`,
	).Scan(&layerColExists); err != nil {
		return fmt.Errorf("check layer column: %w", err)
	}
	if layerColExists == 0 {
		if _, err := d.db.ExecContext(ctx,
			`ALTER TABLE recordings ADD COLUMN layer INTEGER DEFAULT 0`); err != nil {
			return fmt.Errorf("add layer column: %w", err)
		}
	}
	// v35: AI 事件来源实例(多 Vision 接入,事件按 API Key 名归因;空=旧数据/匿名)。
	var aiSourceColExists int
	if err := d.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('ai_events') WHERE name='source'`,
	).Scan(&aiSourceColExists); err != nil {
		return fmt.Errorf("check ai_events.source column: %w", err)
	}
	if aiSourceColExists == 0 {
		if _, err := d.db.ExecContext(ctx,
			`ALTER TABLE ai_events ADD COLUMN source TEXT DEFAULT ''`); err != nil {
			return fmt.Errorf("add ai_events.source column: %w", err)
		}
	}
	return nil
}

// ensureCameraGroupNameColumn adds cameras.group_name (v36, camera management
// grouping) to databases created before v36. Idempotent: checks pragma
// table_info before the ALTER, so re-running on a current database is a no-op.
func (d *DB) ensureCameraGroupNameColumn(ctx context.Context) error {
	var colExists int
	if err := d.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('cameras') WHERE name='group_name'`,
	).Scan(&colExists); err != nil {
		return fmt.Errorf("check cameras.group_name column: %w", err)
	}
	if colExists == 0 {
		if _, err := d.db.ExecContext(ctx,
			`ALTER TABLE cameras ADD COLUMN group_name TEXT DEFAULT ''`); err != nil {
			return fmt.Errorf("add cameras.group_name column: %w", err)
		}
	}
	return nil
}

// ensureCameraGroupsPositionColumn adds camera_groups.position (v38,
// drag-to-reorder) to databases created before v38. Idempotent.
func (d *DB) ensureCameraGroupsPositionColumn(ctx context.Context) error {
	var colExists int
	if err := d.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('camera_groups') WHERE name='position'`,
	).Scan(&colExists); err != nil {
		return fmt.Errorf("check camera_groups.position column: %w", err)
	}
	if colExists == 0 {
		if _, err := d.db.ExecContext(ctx,
			`ALTER TABLE camera_groups ADD COLUMN position INTEGER NOT NULL DEFAULT 0`); err != nil {
			return fmt.Errorf("add camera_groups.position column: %w", err)
		}
	}
	return nil
}

// ensureCameraAudioLinkColumn adds cameras.audio_link_camera_id (v43, audio
// source devices): optional association to the video camera whose recordings
// the audio should be replayed alongside (a mic mounted next to a camera).
// ” = free-standing mic. Pure metadata — the recorder never reads it.
func (d *DB) ensureCameraAudioLinkColumn(ctx context.Context) error {
	var colExists int
	if err := d.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('cameras') WHERE name='audio_link_camera_id'`,
	).Scan(&colExists); err != nil {
		return fmt.Errorf("check cameras.audio_link_camera_id column: %w", err)
	}
	if colExists == 0 {
		if _, err := d.db.ExecContext(ctx,
			`ALTER TABLE cameras ADD COLUMN audio_link_camera_id TEXT DEFAULT ''`); err != nil {
			return fmt.Errorf("add cameras.audio_link_camera_id column: %w", err)
		}
	}
	return nil
}

func (d *DB) Close() error {
	if d == nil || d.db == nil {
		return nil
	}
	// Close the read pool first; ignore its error so the write-pool error (the
	// authoritative one) is still returned.
	if d.readDB != nil {
		_ = d.readDB.Close()
	}
	return d.db.Close()
}

// Backup creates a backup of the database using VACUUM INTO.

// Backup creates a backup of the database using VACUUM INTO.
func (d *DB) Backup(ctx context.Context, destPath string) error {
	_, err := d.db.ExecContext(ctx, "VACUUM INTO ?", destPath)
	return err
}

// Optimize runs PRAGMA optimize to refresh query planner statistics.
// This performs an incremental ANALYZE only where needed.
func (d *DB) Optimize(ctx context.Context) error {
	_, err := d.db.ExecContext(ctx, "PRAGMA optimize")
	if err != nil {
		logger.Warn("PRAGMA optimize failed", "error", err)
		return err
	}
	logger.Debug("PRAGMA optimize completed")
	return nil
}

// CheckpointWAL performs a WAL checkpoint operation.
// Mode can be "PASSIVE", "FULL", "RESTART", or "TRUNCATE".
func (d *DB) CheckpointWAL(ctx context.Context, mode string) (busy int, logFrames int, checkpointedFrames int, err error) {
	// PRAGMA does not accept bound parameters, so the mode is interpolated into
	// the statement text — whitelist it so no caller can smuggle arbitrary SQL.
	switch mode {
	case "PASSIVE", "FULL", "RESTART", "TRUNCATE":
	default:
		return 0, 0, 0, fmt.Errorf("invalid checkpoint mode: %q", mode)
	}
	query := fmt.Sprintf("PRAGMA wal_checkpoint(%s)", mode)
	row := d.db.QueryRowContext(ctx, query)
	err = row.Scan(&busy, &logFrames, &checkpointedFrames)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("wal_checkpoint(%s): %w", mode, err)
	}
	logger.Info("WAL checkpoint completed", "mode", mode, "busy", busy, "log_frames", logFrames, "checkpointed_frames", checkpointedFrames)
	return busy, logFrames, checkpointedFrames, nil
}

// GetWALSize returns the size of the -wal file in bytes.
// Returns 0 if the WAL file does not exist.
func (d *DB) GetWALSize() (int64, error) {
	walPath := d.path + "-wal"
	info, err := os.Stat(walPath)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("stat WAL file: %w", err)
	}
	return info.Size(), nil
}

// GetFragmentationRatio returns fragmentation as freelist_count/page_count.
func (d *DB) GetFragmentationRatio(ctx context.Context) (float64, error) {
	var pageCount, freelistCount int64
	err := d.db.QueryRowContext(ctx, "PRAGMA page_count").Scan(&pageCount)
	if err != nil {
		return 0, fmt.Errorf("get page_count: %w", err)
	}
	if pageCount == 0 {
		return 0, nil
	}
	err = d.db.QueryRowContext(ctx, "PRAGMA freelist_count").Scan(&freelistCount)
	if err != nil {
		return 0, fmt.Errorf("get freelist_count: %w", err)
	}
	return float64(freelistCount) / float64(pageCount), nil
}

// IncrementalVacuum reclaims up to N free pages without exclusive lock.
// Only effective if auto_vacuum != 0 (set at DB creation time). For DBs created before
// auto_vacuum was enabled, use CompactOnline instead.
func (d *DB) IncrementalVacuum(ctx context.Context, n int) error {
	_, err := d.db.ExecContext(ctx, fmt.Sprintf("PRAGMA incremental_vacuum(%d)", n))
	if err != nil {
		return fmt.Errorf("incremental_vacuum(%d): %w", n, err)
	}
	logger.Info("Incremental vacuum completed", "max_pages", n)
	return nil
}

// CompactOnline performs a non-blocking online database compaction via VACUUM INTO.
// It writes a fresh, defragmented copy to a temp file, then atomically replaces the
// old DB files (main + WAL + SHM) with the compacted copy. Unlike full VACUUM, this
// never holds an exclusive lock on the live DB — readers and the writer continue
// operating throughout. The new file also gets auto_vacuum=INCREMENTAL enabled so
// future incremental_vacuum calls work.
//
// This is the correct fix for DBs created before auto_vacuum was enabled (where
// incremental_vacuum is a no-op). Call it when fragmentation exceeds ~50%.
//
// Returns the number of bytes saved (old size - new size), or 0 if nothing was done.
func (d *DB) CompactOnline(ctx context.Context) (int64, error) {
	oldInfo, err := os.Stat(d.path)
	if err != nil {
		return 0, fmt.Errorf("stat db: %w", err)
	}
	oldSize := oldInfo.Size()

	// VACUUM INTO creates a new DB file with all data defragmented and packed.
	// auto_vacuum must be set on the NEW file — VACUUM INTO resets it to NONE by
	// default, so the target connection sets it before populate (below).
	tmpPath := d.path + ".compact.tmp"
	// Remove stale temp file if a previous attempt was interrupted.
	_ = os.Remove(tmpPath)

	// Set auto_vacuum=INCREMENTAL on the target BEFORE populating it, so the compacted
	// file has incremental vacuum enabled for future maintenance.
	// SQLite: VACUUM INTO creates a new file from scratch; to get auto_vacuum we must
	// open the temp, set pragma, then VACUUM INTO. But VACUUM INTO overwrites.
	// The reliable approach: open temp DB, set auto_vacuum=INCREMENTAL, then use it
	// as the VACUUM target. SQLite will carry the setting since it creates fresh pages.
	if _, err := d.db.ExecContext(ctx, "PRAGMA auto_vacuum = INCREMENTAL"); err != nil {
		// Non-fatal: on existing DBs this is a no-op, but VACUUM INTO on a DB with
		// this connection pragma still creates NONE auto_vacuum. We set it on the
		// result below.
		logger.Warn("CompactOnline: could not set auto_vacuum pragma (non-fatal)", "error", err)
	}

	if _, err := d.db.ExecContext(ctx, "VACUUM INTO ?", tmpPath); err != nil {
		_ = os.Remove(tmpPath)
		return 0, fmt.Errorf("vacuum into: %w", err)
	}

	newInfo, err := os.Stat(tmpPath)
	if err != nil {
		_ = os.Remove(tmpPath)
		return 0, fmt.Errorf("stat compacted db: %w", err)
	}
	newSize := newInfo.Size()

	// Enable auto_vacuum=INCREMENTAL on the compacted file so future
	// incremental_vacuum calls actually work. We must open it, set the pragma, and
	// do a mini-vacuum to apply it (auto_vacuum only takes effect on VACUUM).
	// This is the ONLY reliable way to set it on an existing DB.
	if err := applyAutoVacuumIncremental(tmpPath); err != nil {
		logger.Warn("CompactOnline: could not set auto_vacuum on compacted db (defrag still succeeded)", "error", err)
	}

	// Atomically swap: close current connections are NOT needed (VACUUM INTO doesn't
	// touch the source). We rename the files after a final sync.
	// Rename: old → .bak, new → live. If rename fails, restore.
	walPath := d.path + "-wal"
	shmPath := d.path + "-shm"
	bakPath := d.path + ".bak"
	_ = os.Remove(bakPath)

	// Move old files aside (best-effort on WAL/SHM — they may not exist).
	if err := os.Rename(d.path, bakPath); err != nil {
		_ = os.Remove(tmpPath)
		return 0, fmt.Errorf("rename old db to bak: %w", err)
	}
	// Move compacted file into place.
	if err := os.Rename(tmpPath, d.path); err != nil {
		// Restore old file
		_ = os.Rename(bakPath, d.path)
		_ = os.Remove(tmpPath)
		return 0, fmt.Errorf("rename compacted db to live: %w", err)
	}
	// Remove old WAL/SHM — the compacted DB starts fresh with empty WAL.
	_ = os.Remove(walPath)
	_ = os.Remove(shmPath)
	// Clean up backup after successful swap.
	_ = os.Remove(bakPath)

	saved := oldSize - newSize
	logger.Info("Online compaction completed",
		"old_bytes", oldSize, "new_bytes", newSize, "saved_bytes", saved,
		"saved_pct", fmt.Sprintf("%.1f%%", 100.0*float64(saved)/float64(max(oldSize, 1))))
	return saved, nil
}

// applyAutoVacuumIncremental opens dbPath, sets auto_vacuum=INCREMENTAL, and runs a
// VACUUM to apply it (SQLite only honors auto_vacuum changes during VACUUM on existing
// databases). Uses a temporary connection so the main pool is unaffected.
func applyAutoVacuumIncremental(dbPath string) error {
	dsn := dbPath + "?_pragma=busy_timeout(5000)"
	tmpDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return fmt.Errorf("open for auto_vacuum: %w", err)
	}
	defer tmpDB.Close()
	if _, err := tmpDB.Exec("PRAGMA auto_vacuum = INCREMENTAL"); err != nil {
		return fmt.Errorf("set auto_vacuum: %w", err)
	}
	// VACUUM applies the auto_vacuum mode change. This briefly locks the file, but
	// it's the compacted temp (not the live DB at this point in CompactOnline's flow).
	if _, err := tmpDB.Exec("VACUUM"); err != nil {
		return fmt.Errorf("vacuum to apply auto_vacuum: %w", err)
	}
	return nil
}

// TimeLayout is the format used to store timestamps in SQLite.
// Uses UTC without timezone suffix, compatible with SQLite's datetime() for string comparison.
// Exported as the single source — the api/transcoding packages format with it
// too, and a diverging literal silently queries empty (#876).
const TimeLayout = "2006-01-02 15:04:05.999999999"

// timeToDB converts time.Time to a SQLite-compatible string value.
// Returns nil for zero time (which SQLite stores as NULL).
func timeToDB(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(TimeLayout)
}

// formatTime formats a time.Time as a SQLite-compatible UTC string.
// Returns empty string for zero time.
func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(TimeLayout)
}

// parseTime parses a SQLite timestamp string back into time.Time (UTC).
// 0.10.0+: legacy Go time.Time.String() formats (monotonic clock suffix "m=+...",
// timezone abbreviations like "CST") were removed — databases still containing
// those formats must be upgraded via 0.9.x first. RFC3339 remains supported as
// it is a standard interchange format used by external callers (e.g. MiBeeVision).
func parseTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	// Canonical format (current)
	if t, err := time.Parse(TimeLayout, s); err == nil {
		return t, nil
	}
	// Without fractional seconds (SQLite CURRENT_TIMESTAMP / datetime('now'))
	if t, err := time.Parse("2006-01-02 15:04:05", s); err == nil {
		return t, nil
	}
	// RFC3339 (standard interchange format, used by external API callers)
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
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

// Nullable helper functions for per-camera merge config.

func nullBoolToPtr(v sql.NullBool) *bool {
	if !v.Valid {
		return nil
	}
	b := v.Bool
	return &b
}

func nullStringToPtr(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	return &v.String
}

func nullInt64ToPtr(v sql.NullInt64) *int {
	if !v.Valid {
		return nil
	}
	i := int(v.Int64)
	return &i
}

func ptrToNullBool(v *bool) sql.NullBool {
	if v == nil {
		return sql.NullBool{}
	}
	return sql.NullBool{Valid: true, Bool: *v}
}

func ptrToNullString(v *string) sql.NullString {
	if v == nil {
		return sql.NullString{}
	}
	return sql.NullString{Valid: true, String: *v}
}

func ptrToNullInt64(v *int) sql.NullInt64 {
	if v == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Valid: true, Int64: int64(*v)}
}
