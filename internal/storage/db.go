package storage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/slogx"

	_ "modernc.org/sqlite"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

var logger = slogx.Component("storage")

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

	// cameraStatsCache memoizes GetCameraStorageStats (a GROUP BY over all
	// recordings rows) with the same reasoning as trendsCache: the Dashboard
	// polls every 30s and per-camera footprints drift by one segment at a time.
	cameraStatsMu    sync.Mutex
	cameraStatsCache *cameraStatsCacheEntry
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

// cameraStatsCacheEntry holds a cached GetCameraStorageStats result and its
// expiry time.
type cameraStatsCacheEntry struct {
	value    []model.CameraStorageStats
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
	// ObserveTxn records one write transaction by source (#759):
	// recording_insert / recording_close / merge_status / ai_event / health /
	// api_write / cleanup / repair.
	ObserveTxn(source string, seconds float64)
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
// Upgrade path: users on 0.9.x (schema v28) must upgrade to 0.10.x first —
// the v28→v29 migration (dropping the redundant `merged` column) was removed
// in v30 (#240) since the v0.9.x → v0.10.x upgrade population has long since
// migrated. Users below v0.9.x must upgrade to 0.9.x first (see upgrade-guide).
//
// v32: added gb28181_devices + gb28181_channels (GB28181 platform-role device
// catalog, issue #315).
//
// v33: added recordings.motion_score + recordings.activity_flags (issue #435 —
// compressed-domain boring-segment detection). Both columns are also ensured
// via idempotent ALTER for databases created before v33.
//
// v34: added recordings.motion_confidence (issue #634 — absolute-size
// confidence discounting the relative score on bitrate-starved segments).
// Also ensured via idempotent ALTER; pre-v34 rows carry -1 = "full weight".
//
// v35: added ai_events.source (multi-Vision attribution by API key name).
//
// v36: added cameras.group_name (camera management grouping — UI organization
// only, never read by the recorder). Also ensured via idempotent ALTER.
//
// v37: added camera_groups (group registry — lets an EMPTY group survive with
// no member camera; rename/delete walk the member cameras' group_name).
//
// v38: added camera_groups.position (drag-to-reorder persistence — the
// management page sends the full named-group order). Also ensured via
// idempotent ALTER.
//
// v39: one-shot DATA migration (#763) — layer=1 tierrec rows inserted before
// the terminal-status fix carried merge_status='pending' forever (98% of all
// pending rows in production, polluting pending-based diagnostics and
// crowding the ghost-row sweep). Init rewrites them to the terminal
// 'sublayer' status; the UPDATE is idempotent (second boot touches 0 rows).
//
// v40: added offload_outbox (issue #874 batch 1) — the segment→object state
// machine for S3-compatible object-storage offload
// (pending → uploading → uploaded → evicted, terminal skipped). Pure addition;
// no column changes to existing tables.
//
// v41: added offload_outbox.started_at/ended_at/duration/format (issue #874
// batch 2 — remote playback needs timeline metadata on the outbox row, since
// eviction deletes the recordings row). Ensured via idempotent ALTER +
// one-shot backfill from recordings (rows whose recording row is already
// gone stay empty and are excluded from remote listings).
//
// v42: added offload_outbox.bucket (issue #874 batch 3 — per-camera bucket
// routing). ” = the configured default bucket; resolution happens at the
// consumer (offload storeFor), never rewritten in place.
//
// v43 (dual-use — two lines shipped the same number independently and both
// landed here via merge): added camera_cred_probe (#credprobe, the
// once-per-camera default-credential-probe ledger) AND
// cameras.audio_link_camera_id (#audio, audio-source ↔ video camera
// association). Both are pure additions with idempotent ensures; the
// baseline CREATE covers fresh installs either way.
//
// The schema_meta table tracks the schema version for future migrations.
const currentSchemaVersion = "43"

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
        group_name TEXT DEFAULT '',
        audio_link_camera_id TEXT DEFAULT '',
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
        motion_score REAL DEFAULT -1,
        motion_confidence REAL DEFAULT -1,
        activity_flags TEXT DEFAULT '',
        timeline_map TEXT DEFAULT '',
        layer INTEGER DEFAULT 0,
        created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
        FOREIGN KEY (camera_id) REFERENCES cameras(id)
    );`
	metaSQL := `CREATE TABLE IF NOT EXISTS schema_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);`

	// v43 (#credprobe): once-per-camera ledger for the default-credential
	// probe. A row existing at all means the camera was already probed —
	// success OR failure — and must never be probed again.
	credProbeSQL := `CREATE TABLE IF NOT EXISTS camera_cred_probe (
        camera_id TEXT PRIMARY KEY,
        status TEXT NOT NULL DEFAULT 'attempted',
        matched_username TEXT NOT NULL DEFAULT '',
        rotated INTEGER NOT NULL DEFAULT 0,
        attempted_at TEXT NOT NULL DEFAULT (datetime('now'))
    );`
	if _, err := d.db.ExecContext(ctx, credProbeSQL); err != nil {
		return fmt.Errorf("create camera_cred_probe: %w", err)
	}
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
		source TEXT DEFAULT '',
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

	// v40: added merge_lineage (fold provenance — every consumed recording ID
	// maps to the surviving merged row, written inside the fold transactions;
	// lets stale recording IDs resolve to their merged product exactly, without
	// time-window heuristics, see #903).
	mergeLineageSQL := `CREATE TABLE IF NOT EXISTS merge_lineage (
		source_id TEXT PRIMARY KEY,
		merged_id TEXT NOT NULL,
		camera_id TEXT NOT NULL,
		created_at TEXT NOT NULL
	)`

	gbDevSQL := `CREATE TABLE IF NOT EXISTS gb28181_devices (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL DEFAULT '',
		manufacturer TEXT NOT NULL DEFAULT '',
		model TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL DEFAULT 'offline',
		last_keepalive DATETIME,
		registered_at DATETIME
	);`
	gbChSQL := `CREATE TABLE IF NOT EXISTS gb28181_channels (
		id TEXT PRIMARY KEY,
		device_id TEXT NOT NULL,
		name TEXT NOT NULL DEFAULT '',
		manufacturer TEXT NOT NULL DEFAULT '',
		parental INTEGER NOT NULL DEFAULT 0,
		status TEXT NOT NULL DEFAULT 'idle',
		camera_id TEXT,
		updated_at DATETIME
	);`

	archiveCleanupTasksSQL := `CREATE TABLE IF NOT EXISTS archive_cleanup_tasks (
		camera_id TEXT PRIMARY KEY,
		camera_name TEXT NOT NULL DEFAULT '',
		recording_count INTEGER NOT NULL DEFAULT 0,
		total_size INTEGER NOT NULL DEFAULT 0,
		status TEXT NOT NULL DEFAULT 'pending',
		error TEXT DEFAULT '',
		created_at TEXT NOT NULL DEFAULT (datetime('now')),
		completed_at TEXT DEFAULT ''
	);`

	// Local PTZ preset registry for protocols without device-side preset
	// queries (GB28181). token = the preset number sent in the A.3.4 command.
	ptzPresetsSQL := `CREATE TABLE IF NOT EXISTS camera_ptz_presets (
		camera_id TEXT NOT NULL,
		token TEXT NOT NULL,
		name TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL DEFAULT (datetime('now')),
		PRIMARY KEY (camera_id, token)
	);`
	cascadeChSQL := `CREATE TABLE IF NOT EXISTS cascade_channels (
		camera_id TEXT PRIMARY KEY,
		gb_channel_id TEXT NOT NULL UNIQUE,
		name TEXT NOT NULL DEFAULT '',
		updated_at DATETIME
	);`

	gbFpSQL := `CREATE TABLE IF NOT EXISTS gb28181_fingerprints (
		device_id TEXT PRIMARY KEY,
		serial TEXT NOT NULL,
		source_ip TEXT NOT NULL DEFAULT '',
		probed_at DATETIME
	);`

	camGroupsSQL := `CREATE TABLE IF NOT EXISTS camera_groups (
        name TEXT PRIMARY KEY,
        created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
        position INTEGER NOT NULL DEFAULT 0
    );`

	offloadOutboxSQL := `CREATE TABLE IF NOT EXISTS offload_outbox (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        recording_id TEXT NOT NULL UNIQUE,
        camera_id TEXT NOT NULL,
        object_key TEXT NOT NULL UNIQUE,
        local_path TEXT NOT NULL,
        file_size INTEGER NOT NULL DEFAULT 0,
        status TEXT NOT NULL DEFAULT 'pending',
        etag TEXT DEFAULT '',
        bucket TEXT DEFAULT '',
        uploaded_size INTEGER NOT NULL DEFAULT 0,
        attempts INTEGER NOT NULL DEFAULT 0,
        last_error TEXT DEFAULT '',
        created_at TEXT NOT NULL,
        uploaded_at TEXT DEFAULT '',
        evicted_at TEXT DEFAULT ''
    );`

	thermalReadingsSQL := `CREATE TABLE IF NOT EXISTS thermal_readings (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        camera_id TEXT NOT NULL,
        minute TEXT NOT NULL,
        max_c REAL,
        min_c REAL,
        spots TEXT NOT NULL DEFAULT '',
        UNIQUE(camera_id, minute)
    );`

	for _, sql := range []string{camSQL, recSQL, metaSQL, featSQL, healthSQL, transcodeSQL, aiEventsSQL, timelapseMergesSQL, archiveCleanupTasksSQL, gbDevSQL, gbChSQL, gbFpSQL, cascadeChSQL, ptzPresetsSQL, camGroupsSQL, offloadOutboxSQL, mergeLineageSQL, thermalReadingsSQL} {
		if _, err := d.db.ExecContext(ctx, sql); err != nil {
			return fmt.Errorf("create table: %w", err)
		}
	}

	// Seed schema_meta + default feature flags for new databases.
	_, _ = d.db.ExecContext(ctx, "INSERT OR IGNORE INTO schema_meta (key, value) VALUES ('schema_version', ?)", currentSchemaVersion)
	_, _ = d.db.ExecContext(ctx, `INSERT OR IGNORE INTO feature_flags (key, value) VALUES
		('protocol.xiaomi', 1),
		('protocol.rtsp', 1),
		('protocol.http', 1),
		('protocol.onvif', 1),
		('protocol.srt', 1),
		('protocol.rtmp', 1);`)

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
		// Composite index for the recordings ?ai_class= filter (EXISTS subquery on
		// class_name + recording_id). Also serves class_name-only lookups.
		"CREATE INDEX IF NOT EXISTS idx_ai_events_class_recording ON ai_events(class_name, recording_id)",
		// Timelapse merges
		"CREATE INDEX IF NOT EXISTS idx_timelapse_merges_camera_window ON timelapse_merges(camera_id, window_start)",
		"CREATE INDEX IF NOT EXISTS idx_timelapse_merges_status ON timelapse_merges(status)",
		// Archive cleanup tasks
		"CREATE INDEX IF NOT EXISTS idx_archive_cleanup_status ON archive_cleanup_tasks(status)",
		// Merge lineage (v40): re-target on bucket re-fold + purge when the
		// target row is deleted; camera purge on archive cleanup.
		"CREATE INDEX IF NOT EXISTS idx_merge_lineage_merged ON merge_lineage(merged_id)",
		"CREATE INDEX IF NOT EXISTS idx_merge_lineage_camera ON merge_lineage(camera_id)",
		// Offload outbox (issue #874): claim sweep + backlog accounting
		"CREATE INDEX IF NOT EXISTS idx_offload_outbox_status ON offload_outbox(status)",
		"CREATE INDEX IF NOT EXISTS idx_offload_outbox_camera ON offload_outbox(camera_id)",
	}
	for _, idx := range indexes {
		_, _ = d.db.ExecContext(ctx, idx)
	}

	// Drop legacy indexes superseded by compound indexes (idempotent).
	for _, drop := range []string{
		"DROP INDEX IF EXISTS idx_recordings_camera",   // superseded by idx_recordings_camera_time
		"DROP INDEX IF EXISTS idx_recordings_archived", // superseded by idx_recordings_archived_time
		"DROP INDEX IF EXISTS idx_recordings_pinned",   // ancient (v3→v4 rename)
	} {
		_, _ = d.db.ExecContext(ctx, drop)
	}

	// ── Column backfill for pre-v33 databases (issue #435) ──
	// The baseline CREATE TABLE above only carries the final column set for
	// NEW databases; an existing recordings table gets the motion columns via
	// idempotent ALTER. Default -1 = unanalyzed (model.MotionScoreUnanalyzed).
	if err := d.ensureRecordingsMotionColumns(ctx); err != nil {
		return err
	}

	// ── Column backfill for pre-v36 databases: cameras.group_name ──
	if err := d.ensureCameraGroupNameColumn(ctx); err != nil {
		return err
	}

	// ── Column backfill for pre-v38 databases: camera_groups.position ──
	if err := d.ensureCameraGroupsPositionColumn(ctx); err != nil {
		return err
	}

	// ── v41 (issue #874 batch 2): offload_outbox timeline metadata ──
	if err := d.ensureOffloadOutboxMetadataColumns(ctx); err != nil {
		return err
	}

	// ── v42 (issue #874 batch 3): offload_outbox.bucket ──
	var bucketCol int
	if err := d.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('offload_outbox') WHERE name='bucket'`,
	).Scan(&bucketCol); err != nil {
		return fmt.Errorf("check offload_outbox.bucket column: %w", err)
	}
	if bucketCol == 0 {
		if _, err := d.db.ExecContext(ctx,
			`ALTER TABLE offload_outbox ADD COLUMN bucket TEXT DEFAULT ''`); err != nil {
			return fmt.Errorf("add offload_outbox.bucket column: %w", err)
		}
	}

	// ── v39 data migration (#763): retire permanently-pending layer=1 rows ──
	// Sub-layer segments never enter the merge pipeline, so 'pending' was a
	// wrong lifecycle from birth. Idempotent: after the first run no
	// layer!=0 'pending' rows remain (the writer now inserts terminal).
	if _, err := d.db.ExecContext(ctx,
		`UPDATE recordings SET merge_status='sublayer' WHERE COALESCE(layer,0)!=0 AND merge_status='pending'`,
	); err != nil {
		return fmt.Errorf("v39 sublayer status migration: %w", err)
	}

	// ── Column backfill for pre-v43 databases: cameras.audio_link_camera_id ──
	if err := d.ensureCameraAudioLinkColumn(ctx); err != nil {
		return err
	}

	_, _ = d.db.ExecContext(ctx, "UPDATE schema_meta SET value=? WHERE key='schema_version'", currentSchemaVersion)

	// Enable auto_vacuum = INCREMENTAL for fresh databases (no-op for existing).
	_, _ = d.db.ExecContext(ctx, "PRAGMA auto_vacuum = INCREMENTAL")

	// Cap the WAL high-water mark (#752): after a checkpoint the -wal file
	// shrinks to at most this size, and crash-recovery replay is bounded by
	// it. Set on the writer only (the checkpointing connection) — the DSN
	// baseline stays frozen.
	_, _ = d.db.ExecContext(ctx, fmt.Sprintf("PRAGMA journal_size_limit = %d", walJournalSizeLimit))

	// Refresh query planner stats (incremental ANALYZE where needed). Cheap on startup.
	_, _ = d.db.ExecContext(ctx, `PRAGMA optimize`)

	return nil
}
