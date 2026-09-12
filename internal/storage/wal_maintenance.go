package storage

// wal_maintenance.go — planned WAL checkpointing (#752).
//
// Baseline already in place elsewhere: the cleanup cycle's tail does a
// PASSIVE checkpoint (TRUNCATE escalation after 3 busy cycles) and the 60s
// metrics ticker exports sqlite_wal_size_bytes. What was missing:
//
//   - checkpointing was coupled to the (default hourly) cleanup cycle, so a
//     WAL bloated by reader pressure sat large until the next cycle;
//   - nothing bounded the WAL high-water mark for crash exits.
//
// This file adds a time-gated idle-window PASSIVE ticker (fires only when
// the WAL exceeds the threshold AND the minimum gap since the last trigger
// has passed — quiet read-mostly periods burn nothing) and caps the WAL via
// journal_size_limit in Init() (NOT in the DSN — the DSN baseline stays
// frozen; the limit only matters on the writer, the checkpointing conn).
//
// Graceful shutdown needs no explicit TRUNCATE: closing the last connection
// checkpoints and removes the -wal file (SQLite semantics, verified by
// TestClose_CheckpointsWALViaLastConnClose). Crash exits are bounded by
// journal_size_limit instead.

import (
	"context"
	"time"
)

const (
	// walJournalSizeLimit bounds the WAL file's high-water mark: after a
	// checkpoint the file shrinks to at most this size, and an un-checkpointed
	// crash replay is capped by it.
	walJournalSizeLimit = 64 << 20 // 64MB
	// walCheckpointThreshold: trigger a PASSIVE checkpoint once the WAL holds
	// more than this (~2× the 1000-page autocheckpoint, i.e. autocheckpointing
	// is already losing ground — usually sustained reader pressure).
	walCheckpointThreshold = 8 << 20 // 8MB
	// walCheckpointMinGap is the minimum spacing between triggered
	// checkpoints — read-heavy phases must not turn the ticker into a busy
	// checkpoint loop.
	walCheckpointMinGap = 5 * time.Minute
	// walMaintenanceTick is how often the gates are evaluated.
	walMaintenanceTick = time.Minute
)

// walCheckpointFn is the loop's checkpoint seam (call-pattern tests).
var walCheckpointFn = func(d *DB, mode string) (busy int, logFrames int, checkpointedFrames int, err error) {
	return d.CheckpointWAL(context.Background(), mode)
}

// shouldTriggerWALCheckpoint is the pure gate: WAL must exceed the threshold
// and the last trigger must be older than the minimum gap. Zero last means
// "never triggered" (startup) and passes the gap immediately.
func shouldTriggerWALCheckpoint(now, last time.Time, walBytes, threshold int64) bool {
	if walBytes < threshold {
		return false
	}
	if last.IsZero() {
		return true
	}
	return now.Sub(last) >= walCheckpointMinGap
}

// StartWALMaintenance launches the idle-window PASSIVE checkpoint loop. It
// exits when ctx is cancelled (the App start context), never blocks callers,
// and deliberately never escalates to TRUNCATE — the aggressive action keeps
// its single owner in the cleanup tail.
func (d *DB) StartWALMaintenance(ctx context.Context) {
	d.startWALMaintenance(ctx, walMaintenanceTick, walCheckpointThreshold)
}

// StartWALMaintenanceWithInterval is StartWALMaintenance with an injectable
// tick (tests).
func (d *DB) StartWALMaintenanceWithInterval(ctx context.Context, tick time.Duration) {
	d.startWALMaintenance(ctx, tick, walCheckpointThreshold)
}

// startWALMaintenance is the core loop with injectable tick and size
// threshold (tests lower the threshold instead of manufacturing an 8MB WAL).
func (d *DB) startWALMaintenance(ctx context.Context, tick time.Duration, threshold int64) {
	go func() {
		ticker := time.NewTicker(tick)
		defer ticker.Stop()
		var last time.Time
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				size, err := d.GetWALSize()
				if err != nil {
					logger.Warn("wal maintenance: stat WAL failed", "error", err)
					continue
				}
				now := time.Now()
				if !shouldTriggerWALCheckpoint(now, last, size, threshold) {
					continue
				}
				last = now
				busy, logFrames, ckptFrames, err := walCheckpointFn(d, "PASSIVE")
				if err != nil {
					logger.Warn("wal maintenance: PASSIVE checkpoint failed", "error", err)
					continue
				}
				if busy == 1 {
					logger.Warn("wal maintenance: PASSIVE checkpoint busy — readers hold the WAL; cleanup tail owns TRUNCATE escalation",
						"wal_bytes", size, "log_frames", logFrames)
				} else if ckptFrames > 0 {
					logger.Info("wal maintenance: PASSIVE checkpoint drained frames",
						"wal_bytes", size, "checkpointed_frames", ckptFrames)
				}
			}
		}
	}()
}
