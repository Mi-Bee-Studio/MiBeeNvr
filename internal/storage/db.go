package storage

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

var logger = slog.Default().With("component", "storage")

// escapeLike escapes LIKE special characters (% and _) with backslash.
// This prevents SQL injection via LIKE wildcards while allowing literal searches.
// Must be used with ESCAPE '\\' clause in the SQL query.
func escapeLike(input string) string {
	escaped := strings.ReplaceAll(input, "\\", "\\\\")
	escaped = strings.ReplaceAll(escaped, "%", "\\%")
	escaped = strings.ReplaceAll(escaped, "_", "\\_")
	return escaped
}

type DB struct {
	path string
	db   *sql.DB
	// readDB is an optional separate connection pool for read-only queries (SELECT).
	// nil in tests / when not configured — readConn() falls back to db. When set,
	// WAL mode lets these reads proceed concurrently with writes on db, eliminating
	// the single-connection read-vs-write head-of-line blocking that stalled
	// InsertRecording during heavy GetRecordingTrends/ListRecordings queries.
	readDB *sql.DB
	// queryMetrics optionally records SQLite query latencies. nil = no-op (tests).
	queryMetrics QueryMetrics
	// countCache memoizes CountRecordingsWithFilter results per filter signature for a
	// short TTL. Paginated list requests ask for a page (ListRecordings) plus the total
	// (Count) — without this, every page navigation re-runs a full COUNT over the
	// filtered set (86K+ rows → ~tens of ms on SD card, worse at scale). The TTL is short
	// enough that newly-inserted recordings appear promptly.
	countMu    sync.Mutex
	countCache map[string]*countCacheEntry

	// totalRecordingsCache memoizes COUNT(*) FROM recordings (a full table scan in
	// SQLite) with a short TTL. The Dashboard polls /api/stats every 30s; without
	// this, each poll re-scans the entire recordings table. The count drifts by at
	// most one segment interval during the TTL, which is imperceptible to the user.
	totalRecordingsMu       sync.RWMutex
	totalRecordingsCached   int
	totalRecordingsCachedAt time.Time

	// trendsCache memoizes GetRecordingTrends results per (days) key with a longer
	// TTL — daily aggregates change at most once per day, so a 2-minute cache is
	// effectively always fresh while eliminating the GROUP BY scan on every poll.
	trendsMu    sync.Mutex
	trendsCache map[string]*trendsCacheEntry
}

// countCacheEntry holds a cached COUNT result and its expiry time.
type countCacheEntry struct {
	value    int
	expiryAt time.Time
}

// trendsCacheEntry holds a cached GetRecordingTrends result and its expiry time.
type trendsCacheEntry struct {
	value    []model.DailyStats
	expiryAt time.Time
}

// countCacheTTL bounds how long a COUNT result is reused. New recordings land in the
// table continuously (segment close every ~30s), so keep this short enough that the
// pagination total drifts by at most one segment interval.
const countCacheTTL = 2 * time.Second

// QueryMetrics is the minimal surface DB needs to record query latencies and busy
// errors. Implemented by *metrics.Metrics; kept interface-typed so the storage package
// does not import metrics (avoids cycles and keeps testability).
type QueryMetrics interface {
	ObserveQueryDuration(queryName string, seconds float64)
	IncSQLiteBusyErrors()
}

// DB returns the underlying write *sql.DB for advanced queries.
func (d *DB) DB() *sql.DB {
	return d.db
}

// readConn returns the read pool if configured, else the write pool. Used by SELECT
// methods so they do not contend with the single serialized write connection.
func (d *DB) readConn() *sql.DB {
	if d.readDB != nil {
		return d.readDB
	}
	return d.db
}

// SetMetrics wires optional observability hooks (query latency histogram, busy-error
// counter). Safe to call once at startup; nil disables instrumentation (no-op).
func (d *DB) SetMetrics(m QueryMetrics) {
	d.queryMetrics = m
}

// observeQuery records a query latency if metrics are wired; otherwise no-op.
// queryName is a short stable label (e.g. "ListRecordings", "InsertRecording").
func (d *DB) observeQuery(queryName string, start time.Time) {
	if d.queryMetrics == nil {
		return
	}
	d.queryMetrics.ObserveQueryDuration(queryName, time.Since(start).Seconds())
}

// Path returns the database file path.
func (d *DB) Path() string {
	return d.path
}

func New(dbPath string) (*DB, error) {
	// Use DSN-level _pragma so EVERY connection from the pool has these settings,
	// not just the one that ran the ExecContext PRAGMA call.
	// This is critical for busy_timeout to work across goroutines.
	dsn := dbPath + "?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(15000)&_pragma=cache_size(-20000)&_pragma=mmap_size(268435456)&_pragma=temp_store(MEMORY)"

	// First, check SQLite version to determine if we can use analysis_limit
	// modernc.org/sqlite bundles SQLite M-bM-^@M-^T version depends on Go module version
	tempDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	defer tempDB.Close()

	var sv string
	if err := tempDB.QueryRow("SELECT sqlite_version()").Scan(&sv); err == nil {
		logger.Info("SQLite version", "version", sv)
		// analysis_limit was added in SQLite 3.46.0
		// It limits ANALYZE to only scan at most N rows per index, keeping it cheap
		if sv >= "3.46.0" {
			dsn += "&_pragma=analysis_limit(1000)"
			logger.Info("analysis_limit pragma enabled", "limit", 1000)
		} else {
			logger.Info("analysis_limit not supported", "version", sv, "minimum_required", "3.46.0")
		}
	} else {
		logger.Warn("failed to check SQLite version, continuing without analysis_limit", "error", err)
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// Tune connection pool for RPi 3B: single connection to avoid write contention
	// and reduce memory pressure. SQLite with WAL mode handles concurrency well.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	logger.Info("Connection pool configured", "max_open_conns", 1, "max_idle_conns", 1, "conn_max_lifetime", 0)
	d := &DB{path: dbPath, db: db, countCache: make(map[string]*countCacheEntry)}
	// Open a separate read-only pool so heavy SELECTs (ListRecordings, GetRecordingTrends,
	// CountRecordingsWithFilter) do not block the single write connection (InsertRecording,
	// cleanup, merge). WAL mode permits concurrent readers + a single writer. Pool size is
	// conservative for RPi 3B memory; callers can override via SetReadPoolSize.
	readDB, err := sql.Open("sqlite", dsn+"&_pragma=query_only(1)")
	if err != nil {
		db.Close()
		return nil, err
	}
	readDB.SetMaxOpenConns(defaultReadPoolSize)
	readDB.SetMaxIdleConns(defaultReadPoolSize)
	readDB.SetConnMaxLifetime(0)
	d.readDB = readDB
	logger.Info("Read pool configured", "max_open_conns", defaultReadPoolSize)
	return d, nil
}

// defaultReadPoolSize is the size of the read-only connection pool. Each connection
// holds its own page cache (cache_size=-20000 = 20MB), so 5 connections ≈ 100MB.
// Raised from 3 to 5: the Dashboard fires 4 concurrent reads on mount + every 30s,
// and 3 connections caused head-of-line blocking when heavy queries (COUNT, GROUP BY)
// monopolized a slot. 5 is safe on RPi 3B (1GB RAM) — the pool is mostly idle and
// SQLite idle connections release their page cache under memory pressure.
const defaultReadPoolSize = 5

// SetReadPoolSize adjusts the read-only connection pool size. Use to raise it on
// hardware with more RAM (e.g. Banana Pi M5 with 4GB) or lower it under memory pressure.
// Must be called before queries run; no-op if the read pool is disabled.
func (d *DB) SetReadPoolSize(n int) {
	if d.readDB == nil || n <= 0 {
		return
	}
	d.readDB.SetMaxOpenConns(n)
	d.readDB.SetMaxIdleConns(n)
}

// ReadPoolStats returns the read pool connection statistics, or (zero, false) if the
// read pool is disabled (e.g. in tests). Used by metrics to expose read-pool utilization
// separately from the writer pool (DB().Stats() only reports the writer).
func (d *DB) ReadPoolStats() (sql.DBStats, bool) {
	if d.readDB == nil {
		return sql.DBStats{}, false
	}
	return d.readDB.Stats(), true
}

// Init initializes the database schema. 0.10.0 uses a "baseline" approach:
// all tables are CREATE TABLE IF NOT EXISTS with the final column set inline,
// replacing the 27 incremental ALTER TABLE migrations from v1–v28.
//
// Upgrade path: users on 0.9.x (schema v28) upgrade transparently — the IF NOT
// EXISTS clauses skip existing tables, and the v28→v29 migration block handles
// the only breaking change (dropping the redundant `merged` column now that
// merge_status fully replaces it). Users below v28 must upgrade to 0.9.x first.
//
// The schema_meta table tracks the schema version for future migrations.
const currentSchemaVersion = "29"

func (d *DB) Init(ctx context.Context) error {
	// ── Tables (full baseline — new installs get the final schema in one step) ──

	camSQL := `CREATE TABLE IF NOT EXISTS cameras (
        id TEXT PRIMARY KEY,
        name TEXT NOT NULL,
        protocol TEXT NOT NULL,
        encoding TEXT NOT NULL DEFAULT '',
        url TEXT NOT NULL,
        username TEXT DEFAULT '',
        password TEXT DEFAULT '',
        enabled INTEGER DEFAULT 1,
        description TEXT DEFAULT '',
        location TEXT DEFAULT '',
        brand TEXT DEFAULT '',
        model TEXT DEFAULT '',
        serial_number TEXT DEFAULT '',
        stable_id TEXT DEFAULT '',
        retention_days INTEGER DEFAULT 0,
        merge_enabled INTEGER,
        merge_check_interval TEXT,
        merge_window_size TEXT,
        merge_batch_limit INTEGER,
        merge_min_segment_age TEXT,
        merge_min_segments_to_merge INTEGER,
        onvif_endpoint TEXT DEFAULT '',
        profile_token TEXT DEFAULT '',
        stream_encoding TEXT DEFAULT '',
        archived INTEGER DEFAULT 0,
        archived_at DATETIME DEFAULT NULL,
        archive_retention_days INTEGER DEFAULT 0,
        activation_state TEXT DEFAULT 'active',
        merge_duration TEXT DEFAULT 'natural-day',
        stream_key TEXT DEFAULT '',
        srt_passphrase TEXT DEFAULT '',
        srt_stream_id TEXT DEFAULT '',
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
        merge_status TEXT NOT NULL DEFAULT 'pending',
        merge_path TEXT DEFAULT '',
        merge_error TEXT DEFAULT '',
        merge_tier TEXT DEFAULT '',
        merge_progress INTEGER DEFAULT 0,
        merge_quality TEXT DEFAULT 'complete',
        archived INTEGER DEFAULT 0,
        ai_status TEXT DEFAULT NULL,
        ai_processed_at TEXT DEFAULT NULL,
        ai_error TEXT DEFAULT NULL,
        created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
        FOREIGN KEY (camera_id) REFERENCES cameras(id)
    );`
	metaSQL := `CREATE TABLE IF NOT EXISTS schema_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);`
	featSQL := `CREATE TABLE IF NOT EXISTS feature_flags (
		key TEXT PRIMARY KEY,
		value BOOLEAN NOT NULL DEFAULT FALSE,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);`
	healthSQL := `CREATE TABLE IF NOT EXISTS camera_health_events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		camera_id TEXT NOT NULL,
		event_type TEXT NOT NULL,
		status TEXT NOT NULL,
		message TEXT DEFAULT '',
		metadata TEXT DEFAULT '{}',
		created_at TEXT DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now'))
	);`
	transcodeSQL := `CREATE TABLE IF NOT EXISTS transcoding_tasks (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		camera_id TEXT NOT NULL,
		recording_id TEXT NOT NULL,
		input_path TEXT NOT NULL,
		input_format TEXT NOT NULL,
		output_path TEXT NOT NULL,
		output_format TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'pending',
		progress REAL NOT NULL DEFAULT 0.0,
		error TEXT,
		created_at DATETIME NOT NULL,
		started_at DATETIME,
		completed_at DATETIME,
		original_deleted BOOLEAN NOT NULL DEFAULT 0,
		framerate INTEGER DEFAULT 0,
		bitrate TEXT DEFAULT '',
		crf INTEGER DEFAULT 0
	);`
	aiEventsSQL := `CREATE TABLE IF NOT EXISTS ai_events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		camera_id TEXT NOT NULL,
		recording_id TEXT,
		event_type TEXT NOT NULL,
		severity TEXT NOT NULL DEFAULT 'info',
		zone_name TEXT,
		class_name TEXT,
		confidence REAL,
		frame_idx INTEGER,
		frame_timestamp TEXT,
		bbox TEXT,
		snapshot_path TEXT,
		metadata TEXT,
		created_at TEXT DEFAULT (datetime('now'))
	)`
	timelapseMergesSQL := `CREATE TABLE IF NOT EXISTS timelapse_merges (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		camera_id TEXT NOT NULL,
		window_start TEXT NOT NULL,
		window_end TEXT NOT NULL,
		duration_label TEXT NOT NULL,
		output_path TEXT NOT NULL,
		file_size INTEGER DEFAULT 0,
		frame_count INTEGER DEFAULT 0,
		codec TEXT DEFAULT '',
		fps INTEGER DEFAULT 30,
		status TEXT NOT NULL DEFAULT 'pending',
		error TEXT DEFAULT '',
		source_segment_ids TEXT DEFAULT '',
		created_at TEXT NOT NULL,
		completed_at TEXT DEFAULT ''
	)`
	for _, sql := range []string{camSQL, recSQL, metaSQL, featSQL, healthSQL, transcodeSQL, aiEventsSQL, timelapseMergesSQL} {
		if _, err := d.db.ExecContext(ctx, sql); err != nil {
			return fmt.Errorf("create table: %w", err)
		}
	}

	// Seed schema_meta + default feature flags for new databases.
	_, _ = d.db.ExecContext(ctx, "INSERT OR IGNORE INTO schema_meta (key, value) VALUES ('schema_version', '"+currentSchemaVersion+"')")
	_, _ = d.db.ExecContext(ctx, `INSERT OR IGNORE INTO feature_flags (key, value) VALUES
		('protocol.xiaomi', 1),
		('protocol.rtsp', 1),
		('protocol.http', 1),
		('protocol.onvif', 1),
		('protocol.srt', 1),
		('protocol.rtmp', 1);`)

	// ── v28 → v29 migration: drop the legacy `merged` column ──
	// The `merged` bool was fully superseded by `merge_status` TEXT in v13.
	// All write paths already set both; all read paths prefer merge_status.
	// SQLite 3.35+ (modernc.org/sqlite v1.x) supports DROP COLUMN.
	d.migrateV28ToV29(ctx)

	// ── Indexes (final optimized set — all the compound/covering indexes) ──
	indexes := []string{
		// Recordings: the dominant query patterns
		"CREATE INDEX IF NOT EXISTS idx_recordings_camera_time ON recordings(camera_id, started_at, ended_at, archived)",
		"CREATE INDEX IF NOT EXISTS idx_recordings_time ON recordings(started_at)",
		"CREATE INDEX IF NOT EXISTS idx_recordings_merge_status ON recordings(merge_status)",
		"CREATE INDEX IF NOT EXISTS idx_recordings_archived_time ON recordings(archived, started_at DESC)",
		"CREATE INDEX IF NOT EXISTS idx_recordings_camera_ended ON recordings(camera_id, ended_at DESC)",
		"CREATE INDEX IF NOT EXISTS idx_recordings_camera_merge_status ON recordings(camera_id, merge_status, started_at)",
		"CREATE INDEX IF NOT EXISTS idx_recordings_archived_ended ON recordings(archived, ended_at)",
		// Cameras
		"CREATE INDEX IF NOT EXISTS idx_cameras_archived ON cameras(archived)",
		// Health events
		"CREATE INDEX IF NOT EXISTS idx_health_events_camera_id ON camera_health_events(camera_id)",
		"CREATE INDEX IF NOT EXISTS idx_health_events_created_at ON camera_health_events(created_at)",
		// Transcoding tasks
		"CREATE INDEX IF NOT EXISTS idx_transcoding_status ON transcoding_tasks(status)",
		"CREATE INDEX IF NOT EXISTS idx_transcoding_created ON transcoding_tasks(created_at)",
		"CREATE INDEX IF NOT EXISTS idx_transcoding_camera ON transcoding_tasks(camera_id)",
		"CREATE INDEX IF NOT EXISTS idx_transcoding_recording ON transcoding_tasks(recording_id)",
		"CREATE INDEX IF NOT EXISTS idx_transcoding_status_created ON transcoding_tasks(status, created_at)",
		"CREATE INDEX IF NOT EXISTS idx_transcoding_camera_status ON transcoding_tasks(camera_id, status)",
		// AI events
		"CREATE INDEX IF NOT EXISTS idx_ai_events_camera_time ON ai_events(camera_id, created_at DESC)",
		"CREATE INDEX IF NOT EXISTS idx_ai_events_recording ON ai_events(recording_id)",
		// Timelapse merges
		"CREATE INDEX IF NOT EXISTS idx_timelapse_merges_camera_window ON timelapse_merges(camera_id, window_start)",
		"CREATE INDEX IF NOT EXISTS idx_timelapse_merges_status ON timelapse_merges(status)",
	}
	for _, idx := range indexes {
		_, _ = d.db.ExecContext(ctx, idx)
	}

	// Drop legacy indexes superseded by compound indexes (idempotent).
	for _, drop := range []string{
		"DROP INDEX IF EXISTS idx_recordings_camera",   // superseded by idx_recordings_camera_time
		"DROP INDEX IF EXISTS idx_recordings_merged",   // merged column removed; merge_status indexed separately
		"DROP INDEX IF EXISTS idx_recordings_archived", // superseded by idx_recordings_archived_time
		"DROP INDEX IF EXISTS idx_recordings_pinned",   // ancient (v3→v4 rename)
	} {
		_, _ = d.db.ExecContext(ctx, drop)
	}

	_, _ = d.db.ExecContext(ctx, "UPDATE schema_meta SET value='"+currentSchemaVersion+"' WHERE key='schema_version'")

	// Enable auto_vacuum = INCREMENTAL for fresh databases (no-op for existing).
	_, _ = d.db.ExecContext(ctx, "PRAGMA auto_vacuum = INCREMENTAL")

	// Refresh query planner stats (incremental ANALYZE where needed). Cheap on startup.
	_, _ = d.db.ExecContext(ctx, `PRAGMA optimize`)

	return nil
}

// migrateV28ToV29 drops the legacy `merged` bool column from recordings.
// merge_status (TEXT, added in v13) fully replaces it. All write paths already
// set merge_status; this is purely removing the redundant column.
// Safe to call on fresh installs (column never existed) and on already-migrated DBs.
func (d *DB) migrateV28ToV29(ctx context.Context) {
	var mergedColExists int
	_ = d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('recordings') WHERE name='merged'`).Scan(&mergedColExists)
	if mergedColExists == 0 {
		return // fresh install or already migrated
	}
	// Safety net: ensure merge_status is populated for any row still at default
	// 'pending' but with merged=1 (shouldn't happen, but prevents data loss).
	_, _ = d.db.ExecContext(ctx, `UPDATE recordings SET merge_status = 'merged' WHERE merged = 1 AND merge_status = 'pending'`)
	// Backup before destructive schema change (best-effort).
	backupPath := d.path + ".pre-v29-backup"
	if backupErr := d.Backup(ctx, backupPath); backupErr != nil {
		logger.Warn("failed to create pre-v29 backup", "path", backupPath, "error", backupErr)
	}
	if _, err := d.db.ExecContext(ctx, `ALTER TABLE recordings DROP COLUMN merged`); err != nil {
		logger.Error("failed to drop merged column (v29 migration)", "error", err)
	} else {
		logger.Info("dropped legacy merged column from recordings (migration v29)")
	}
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
	// auto_vacuum must be set on the NEW file — VACUUM INTO resets it to NONE by default.
	// We do this by setting the pragma on a temp DB first, then vacuuming into it.
	// However, VACUUM INTO does not carry pragma settings. The workaround: vacuum into
	// the temp file, then we rely on the next full rebuild cycle to set it. For now,
	// defragmentation alone is the win.
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

// sqliteTimeFormat is the format used to store timestamps in SQLite.
// sqliteTimeFormat is the format used to store timestamps in SQLite.
// Uses UTC without timezone suffix, compatible with SQLite's datetime() for string comparison.
const sqliteTimeFormat = "2006-01-02 15:04:05.999999999"

// timeToDB converts time.Time to a SQLite-compatible string value.
// Returns nil for zero time (which SQLite stores as NULL).
func timeToDB(t time.Time) any {
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
// 0.10.0+: legacy Go time.Time.String() formats (monotonic clock suffix "m=+...",
// timezone abbreviations like "CST") were removed — databases still containing
// those formats must be upgraded via 0.9.x first. RFC3339 remains supported as
// it is a standard interchange format used by external callers (e.g. MiBeeVision).
func parseTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	// Canonical format (current)
	if t, err := time.Parse(sqliteTimeFormat, s); err == nil {
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
