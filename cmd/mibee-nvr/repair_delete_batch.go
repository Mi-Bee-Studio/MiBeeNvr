package main

// repair_delete_batch.go — chunked batch deletion for `repair delete-by-format`
// (#753). The historical execute loop issued one transaction per row
// (measured 1-3 rows/s against a live server — each DELETE pays its own WAL
// commit and writer-lock round trip). WAL+NORMAL makes multi-row deletes in
// one transaction nearly free, so candidates now ride
// storage.DeleteRecordingsBatch in bounded chunks with a per-row fallback
// that pinpoints bad rows when a batch statement fails.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/iobudget"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)

// repairDeleteChunkSize bounds the batch DELETE: large enough to amortize
// the transaction overhead, small enough that the writer lock is held well
// under the 15s busy_timeout while the server records concurrently.
const repairDeleteChunkSize = 300

// repairIOBudget paces the CLI's file-reclaim I/O against the same shared
// budget the server uses (#751). nil = off (default). Built from the YAML
// io.budget_bytes_per_sec in cmdRepair so a CLI mass-delete against a live
// server yields exactly like the server's own cleanup would.
var repairIOBudget iobudget.Limiter

// repairChunkSleepFn is the legacy fixed inter-chunk pause (seam for tests).
// It only runs when the I/O budget is OFF — with a budget installed, billing
// paces the loop and the fixed sleep is skipped (#755).
var repairChunkSleepFn = func(ctx context.Context) {
	select {
	case <-time.After(20 * time.Millisecond):
	case <-ctx.Done():
	}
}

// Seams for call-pattern and failure-injection tests (#753 TDD).
var (
	repairDeleteBatchFn = func(ctx context.Context, db *storage.DB, ids []string) ([]string, error) {
		return db.DeleteRecordingsBatch(ctx, ids)
	}
	repairDeleteOneFn = func(ctx context.Context, db *storage.DB, id string) error {
		return db.DeleteRecording(ctx, id)
	}
)

// deleteCandidatesInChunks deletes candidates DB-first in chunks of
// repairDeleteChunkSize (single transaction per chunk), then reclaims the
// files of every DB-deleted row (best-effort RemoveAll on file_path and a
// distinct merge_path sibling). A failing batch statement falls back to
// per-row deletes for that chunk so one bad row cannot poison the rest.
// progress(deleted, total) fires once per chunk. Returns rows deleted, rows
// that failed their DB delete, and the freed byte total.
func deleteCandidatesInChunks(ctx context.Context, db *storage.DB, candidates []model.Recording,
	progress func(deleted, total int),
) (deleted, failed int, freedBytes int64) {
	total := len(candidates)
	// Directory-locality grouping (#755): reclaim same-directory recordings
	// back-to-back for ext4 metadata cache/journal coalescing. Chunking is
	// unaffected (order within chunks is not semantic).
	sorted := make([]model.Recording, len(candidates))
	copy(sorted, candidates)
	sort.Slice(sorted, func(i, j int) bool {
		return filepath.Dir(sorted[i].FilePath) < filepath.Dir(sorted[j].FilePath)
	})
	budgetAborted := false
	// Attribute the chunked deletes to repair (#759 txn sources).
	ctx = storage.WithTxnSource(ctx, "repair")
	for start := 0; start < total; start += repairDeleteChunkSize {
		if ctx.Err() != nil {
			break
		}
		end := min(start+repairDeleteChunkSize, total)
		chunk := sorted[start:end]

		ids := make([]string, len(chunk))
		for i, r := range chunk {
			ids[i] = r.ID
		}
		ok := chunk
		if _, err := repairDeleteBatchFn(ctx, db, ids); err != nil {
			fmt.Fprintf(os.Stderr, "  batch delete failed (%v) — retrying chunk row by row\n", err)
			ok = ok[:0]
			for _, r := range chunk {
				if ctx.Err() != nil {
					break
				}
				if err := repairDeleteOneFn(ctx, db, r.ID); err != nil {
					fmt.Fprintf(os.Stderr, "  DB DELETE FAILED %s: %v\n", r.ID, err)
					failed++
					continue
				}
				ok = append(ok, r)
			}
		}

		for _, r := range ok {
			freedBytes += r.FileSize
			deleted++
			// Bill the reclaim to the shared budget (#751) before touching
			// disk. On error (canceled ctx) stop billing but keep removing —
			// pacing never changes deletion semantics.
			if repairIOBudget != nil && !budgetAborted {
				if err := repairIOBudget.Wait(ctx, iobudget.ConsumerRepair, r.FileSize); err != nil {
					fmt.Fprintf(os.Stderr, "  io budget wait aborted (%v) — continuing unthrottled\n", err)
					budgetAborted = true
				}
			}
			// Best-effort file removal, after the DB row is gone (source of
			// truth first; an orphan file is recoverable, a dangling row is not).
			if r.FilePath != "" {
				_ = os.RemoveAll(r.FilePath)
			}
			if r.MergePath != "" && r.MergePath != r.FilePath {
				_ = os.RemoveAll(r.MergePath)
			}
		}
		progress(deleted, total)

		// Throttle between chunks — bounded I/O burst while the server runs.
		// With the I/O budget active the fixed sleep is skipped: billing
		// paces the loop (#755).
		if repairIOBudget == nil {
			repairChunkSleepFn(ctx)
		} else {
			select {
			case <-ctx.Done():
			default:
			}
		}
	}
	return deleted, failed, freedBytes
}
