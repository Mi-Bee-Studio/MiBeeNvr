package storage

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// SweepOrphanRecordingRows deletes recording rows whose backing file with the
// given suffix has already disappeared from disk.
//
// Rationale: a service restart in the middle of a rolling append-bucket merge
// can leave the bucket's temporary file cleaned up while its DB row survives
// (production 2026-09-25: 164 rows pointing at *.tmp files that no longer
// existed — every one of them 404'd on playback). This sweep is the startup
// backstop: rows whose file still exists are left untouched (they may belong
// to a live writer), only rows with a vanished file are removed.
//
// suffix must start with '.' (e.g. ".tmp"); archived rows are skipped — their
// file_path semantics belong to the offload layer, not local disk.
func (d *DB) SweepOrphanRecordingRows(ctx context.Context, suffix string) (int64, error) {
	if !strings.HasPrefix(suffix, ".") {
		return 0, fmt.Errorf("sweep suffix must start with '.': %q", suffix)
	}
	orphans, err := d.collectVanishedSuffixRows(ctx, suffix)
	if err != nil {
		return 0, err
	}
	if len(orphans) == 0 {
		return 0, nil
	}

	var removed int64
	for len(orphans) > 0 {
		batch := orphans
		if len(batch) > 500 {
			batch = batch[:500]
		}
		orphans = orphans[len(batch):]
		placeholders := strings.Repeat("?,", len(batch))
		placeholder := "(" + strings.TrimSuffix(placeholders, ",") + ")"
		args := make([]interface{}, len(batch))
		for i, id := range batch {
			args[i] = id
		}
		if err := RetryOnBusy(ctx, func() error {
			res, err := d.db.ExecContext(ctx, "DELETE FROM recordings WHERE id IN "+placeholder, args...)
			if err != nil {
				return err
			}
			n, err := res.RowsAffected()
			if err == nil {
				removed += n
			}
			return nil
		}); err != nil {
			return removed, fmt.Errorf("sweep delete: %w", err)
		}
	}
	return removed, nil
}

// collectVanishedSuffixRows returns the ids of suffix-matching rows whose
// backing file no longer exists.
func (d *DB) collectVanishedSuffixRows(ctx context.Context, suffix string) ([]string, error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT id, file_path FROM recordings
		WHERE archived = 0
		  AND merge_status IN ('merged', 'pending', 'merging')
		  AND file_path IS NOT NULL AND file_path != ''
		  AND file_path LIKE '%' || ?`, suffix)
	if err != nil {
		return nil, fmt.Errorf("sweep query: %w", err)
	}
	defer rows.Close()
	var orphans []string
	for rows.Next() {
		var id, path string
		if err := rows.Scan(&id, &path); err != nil {
			return nil, fmt.Errorf("sweep scan: %w", err)
		}
		if _, err := os.Stat(path); os.IsNotExist(err) {
			orphans = append(orphans, id)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sweep iterate: %w", err)
	}
	return orphans, nil
}
