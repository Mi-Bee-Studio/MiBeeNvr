package app

// dbpath.go — recording-root / database decoupling (#395 rework).
//
// The SQLite database is SMALL and constantly written; the recording root is
// HUGE and the thing users need to move. They no longer live together: the DB
// stays on the data volume (NVR_DATA_DIR) and the recording root can be
// switched or migrated hot, without touching the database file.
//
// Legacy installs (DB under the recording root) are adopted once at boot.

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)

// dataDir returns the platform data volume (docker NVR_DATA_DIR), or "".
func dataDir() string {
	return strings.TrimSpace(os.Getenv("NVR_DATA_DIR"))
}

// resolveDBPath picks where the database lives: explicit config override,
// then the data volume, then (bare-metal installs without NVR_DATA_DIR) the
// recording root. Resolved dynamically at boot — except that the bare-metal
// root fallback is pinned into the config on first boot (see openDatabase),
// so a later root switch can never re-resolve the DB under the NEW root.
func resolveDBPath(cfg *config.Config) string {
	if p := strings.TrimSpace(cfg.Storage.DBPath); p != "" {
		return p
	}
	if dd := dataDir(); dd != "" {
		return filepath.Join(dd, "mibee-nvr.db")
	}
	return filepath.Join(cfg.Storage.RootDir, "mibee-nvr.db")
}

const dbFileName = "mibee-nvr.db"

// adoptDatabase performs the one-time move of a legacy root-bound database to
// the data volume (docker deployments). Runs at boot with all writers stopped:
//   - only the legacy DB exists            → snapshot it into the data volume;
//   - both exist                           → the NEWER file wins (the other is
//     a stale remnant of a past experiment), loser content is overwritten;
//   - only the data-volume DB exists       → nothing to do.
func adoptDatabase(cfg *config.Config) {
	dd := dataDir()
	if dd == "" || dd == cfg.Storage.RootDir {
		return // bare metal or already-decoupled layout
	}
	resolved := filepath.Join(dd, dbFileName)
	legacy := filepath.Join(cfg.Storage.RootDir, dbFileName)

	legacyInfo, legacyErr := os.Stat(legacy)
	if legacyErr != nil {
		return // no legacy DB — fresh install
	}
	resolvedInfo, resolvedErr := os.Stat(resolved)
	if resolvedErr == nil && !resolvedInfo.ModTime().Before(legacyInfo.ModTime()) {
		slog.Info("database already on data volume (newer than any legacy copy)",
			"path", resolved)
		return
	}

	// Adopt the legacy DB: VACUUM INTO refuses to overwrite, so clear the
	// stale target first (it is either absent or older than the winner).
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(resolved + suffix); err != nil && !os.IsNotExist(err) {
			slog.Error("database adoption: cannot clear target", "path", resolved+suffix, "error", err)
			return
		}
	}
	if err := vacuumInto(legacy, resolved); err != nil {
		slog.Error("database adoption failed — keeping legacy database in place",
			"legacy", legacy, "target", resolved, "error", err)
		return
	}
	slog.Info("adopted legacy recording-root database onto the data volume",
		"from", legacy, "to", resolved,
		"legacy_mtime", legacyInfo.ModTime().Format(time.RFC3339))
}

// bootDSN is a minimal DSN for one-shot adoption connections.
const bootDSN = "?_pragma=busy_timeout(15000)&_pragma=journal_mode(WAL)"

func vacuumInto(srcDB, dstDB string) error {
	db, err := sql.Open("sqlite", srcDB+bootDSN)
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.ExecContext(context.Background(), "VACUUM INTO "+sqlString(dstDB))
	return err
}

func sqlString(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// openDatabase opens (and initializes) the NVR database at its resolved
// location, after running the one-time legacy adoption. On bare-metal
// installs (no NVR_DATA_DIR, no explicit db_path) the resolved location is
// PINNED into the config: without the pin, switching the recording root
// would make the next boot resolve the DB under the new root and silently
// start from an empty database.
func openDatabase(cfg *config.Config, configPath string) (*storage.DB, error) {
	adoptDatabase(cfg)
	dbPath := resolveDBPath(cfg)
	if cfg.Storage.DBPath == "" && dataDir() == "" && configPath != "" {
		cfg.Storage.DBPath = dbPath
		if err := config.Save(configPath, cfg); err != nil {
			slog.Warn("failed to persist the pinned database path", "error", err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}
	db, err := storage.New(dbPath)
	if err != nil {
		return nil, fmt.Errorf("db open: %w", err)
	}
	if err := db.Init(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("db init: %w", err)
	}
	return db, nil
}
