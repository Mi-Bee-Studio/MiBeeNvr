package cleanup

// This file holds the SQLite database-maintenance strategy that runs at the
// tail of each cleanup cycle: WAL checkpointing (PASSIVE with TRUNCATE
// escalation after 3 busy cycles) and incremental vacuum / online compaction
// based on fragmentation ratio. Also updates the SQLite health metrics.
//
// Extracted from cleanup.go (#227).

import (
	"context"
	"fmt"
	"os"
)

// performDatabaseMaintenance handles WAL checkpoint scheduling and incremental vacuum.
// Called after cleanup + PRAGMA optimize in each RunOnce cycle.
//
// WAL checkpoint strategy:
// - PASSIVE is the default (non-blocking).
// - If PASSIVE returns busy=1 for 3 consecutive cycles, escalate to TRUNCATE.
// - TRUNCATE resets the counter.
//
// Incremental vacuum:
// - If fragmentation > 20%, reclaim up to 1000 free pages stepwise.
// - NOT full VACUUM — does not require exclusive lock.
func (cm *CleanupManager) performDatabaseMaintenance(ctx context.Context) {
	// Step 1: WAL checkpoint (skip if WAL is under 10MB)
	walSize, err := cm.db.GetWALSize()
	if err != nil {
		logger.Warn("DB maintenance: failed to get WAL size", "error", err)
	} else if walSize > 10*1024*1024 {
		logger.Info("DB maintenance: large WAL file, attempting checkpoint", "size_bytes", walSize)
		busy, _, _, err := cm.db.CheckpointWAL(ctx, "PASSIVE")
		if err != nil {
			logger.Warn("DB maintenance: PASSIVE checkpoint failed", "error", err)
		} else if busy == 1 {
			cm.consecutivePassiveFailures++
			logger.Warn("DB maintenance: PASSIVE checkpoint busy",
				"consecutive_failures", cm.consecutivePassiveFailures)
			if cm.consecutivePassiveFailures >= 3 {
				logger.Info("DB maintenance: escalating to TRUNCATE checkpoint")
				busy2, logFrames, ckptFrames, err2 := cm.db.CheckpointWAL(ctx, "TRUNCATE")
				if err2 != nil {
					logger.Warn("DB maintenance: TRUNCATE checkpoint failed", "error", err2)
				} else {
					logger.Info("DB maintenance: TRUNCATE checkpoint completed",
						"busy", busy2, "log_frames", logFrames, "checkpointed_frames", ckptFrames)
					cm.consecutivePassiveFailures = 0
				}
			}
		} else {
			// PASSIVE succeeded, reset counter
			cm.consecutivePassiveFailures = 0
		}
	}

	// Step 2: Incremental vacuum (only if fragmentation > 20%)
	frac, err := cm.db.GetFragmentationRatio(ctx)
	if err != nil {
		logger.Warn("DB maintenance: failed to get fragmentation ratio", "error", err)
	} else if frac > 0.50 {
		// Severe fragmentation (>50%): incremental_vacuum is too slow (1000 pages/cycle)
		// and is a no-op on DBs created before auto_vacuum was enabled (auto_vacuum=0).
		// Do a full online compaction via VACUUM INTO — non-blocking, swaps files atomically.
		logger.Info("DB maintenance: severe fragmentation, running online compaction", "fragmentation_ratio", fmt.Sprintf("%.1f%%", frac*100))
		saved, compErr := cm.db.CompactOnline(ctx)
		if compErr != nil {
			logger.Warn("DB maintenance: online compaction failed", "error", compErr)
		} else {
			logger.Info("DB maintenance: online compaction succeeded", "saved_bytes", saved)
		}
	} else if frac > 0.20 {
		// Moderate fragmentation (20-50%): reclaim free pages incrementally.
		// Use a larger batch (5000) when fragmentation is high for faster reclamation.
		pages := 1000
		if frac > 0.35 {
			pages = 5000
		}
		logger.Info("DB maintenance: high fragmentation detected", "fragmentation_ratio", fmt.Sprintf("%.1f%%", frac*100), "vacuum_pages", pages)
		if err := cm.db.IncrementalVacuum(ctx, pages); err != nil {
			logger.Warn("DB maintenance: incremental vacuum failed", "error", err)
		}
	}
}

// updateSQLiteMetrics updates all SQLite database health metrics.
// Called at the end of each cleanup cycle after performDatabaseMaintenance.
func (cm *CleanupManager) updateSQLiteMetrics(ctx context.Context) {
	if cm.metrics == nil {
		return
	}

	// Update WAL size
	if walSize, err := cm.db.GetWALSize(); err == nil {
		cm.metrics.SQLiteWALSizeBytes.Set(float64(walSize))
	}

	// Update DB file size (use actual DB path, not hardcoded filename)
	dbPath := cm.db.Path()
	if info, err := os.Stat(dbPath); err == nil {
		cm.metrics.SQLiteDBSizeBytes.Set(float64(info.Size()))
	}

	// Update fragmentation ratio
	if frac, err := cm.db.GetFragmentationRatio(ctx); err == nil {
		cm.metrics.SQLiteFragmentationRatio.Set(frac)
	}

	// Update connection pool stats (writer pool — the single serialized connection)
	if db := cm.db.DB(); db != nil {
		stats := db.Stats()
		cm.metrics.SQLiteOpenConnections.Set(float64(stats.OpenConnections))
		cm.metrics.SQLiteInUseConnections.Set(float64(stats.InUse))
	}
	// Update read pool stats (separate pool for SELECTs — not visible via DB().Stats()).
	// WaitCount/WaitDuration reveal whether the pool is undersized: nonzero sustained
	// growth means callers are blocking for a connection and SetReadPoolSize should rise.
	if rstats, ok := cm.db.ReadPoolStats(); ok {
		cm.metrics.SQLiteReadOpenConnections.Set(float64(rstats.OpenConnections))
		cm.metrics.SQLiteReadInUseConnections.Set(float64(rstats.InUse))
		cm.metrics.SQLiteReadWaitCount.Add(float64(rstats.WaitCount))
		cm.metrics.SQLiteReadWaitDuration.Set(rstats.WaitDuration.Seconds())
	}
}
