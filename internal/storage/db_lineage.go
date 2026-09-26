package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// merge_lineage (schema v40) records fold provenance: every recording ID
// consumed by a rolling/batch fold maps to the merged row that swallowed it.
// The recordings themselves are deleted at fold time (#458 design), so a
// stale ID (bookmark, SPA snapshot, shared link) otherwise has no owner. The
// lineage row resolves it EXACTLY — no time-window heuristics and no camera
// special-casing: the ambiguity that defeats the covering-time fallback
// (#915) cannot occur, because the fold that consumed the ID left the answer.

// recordMergeLineageInTx maintains that mapping inside an ongoing fold
// transaction (atomic with the source-row deletion, mirroring
// migrateAIEventsInTx):
//   - every consumedID (source segment or an intermediate bucket that is
//     being folded further) is mapped to targetID;
//   - lineage rows that pointed at a consumed bucket are re-targeted to
//     targetID, so a lookup always lands on the live row no matter how many
//     times the chain re-folded.
func recordMergeLineageInTx(ctx context.Context, tx *sql.Tx, targetID, cameraID string, consumedIDs []string) error {
	if len(consumedIDs) == 0 {
		return nil
	}

	for _, chunk := range chunkIDs(consumedIDs, batchUpdateChunkSize) {
		placeholders := make([]string, len(chunk))
		args := make([]interface{}, 0, len(chunk)+1)
		args = append(args, targetID)
		for i, id := range chunk {
			placeholders[i] = "?"
			args = append(args, id)
		}
		q := "UPDATE merge_lineage SET merged_id=? WHERE merged_id IN (" + strings.Join(placeholders, ",") + ")"
		if _, err := tx.ExecContext(ctx, q, args...); err != nil {
			return fmt.Errorf("rebind merge lineage: %w", err)
		}
	}

	now := timeToDB(time.Now().UTC())
	for _, chunk := range chunkIDs(consumedIDs, batchUpdateChunkSize) {
		var sb strings.Builder
		args := make([]interface{}, 0, len(chunk)*4)
		sb.WriteString("INSERT OR REPLACE INTO merge_lineage(source_id, merged_id, camera_id, created_at) VALUES ")
		for i, id := range chunk {
			if i > 0 {
				sb.WriteByte(',')
			}
			sb.WriteString("(?,?,?,?)")
			args = append(args, id, targetID, cameraID, now)
		}
		if _, err := tx.ExecContext(ctx, sb.String(), args...); err != nil {
			return fmt.Errorf("record merge lineage: %w", err)
		}
	}
	return nil
}

// FindLineageTarget returns the merged recording ID that consumed sourceID,
// or "" when no fold consumed it.
func (d *DB) FindLineageTarget(ctx context.Context, sourceID string) (string, error) {
	defer d.observeQuery("FindLineageTarget", time.Now())
	var target string
	err := d.readConn().QueryRowContext(ctx, `SELECT merged_id FROM merge_lineage WHERE source_id=?`, sourceID).Scan(&target)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("find lineage target: %w", err)
	}
	return target, nil
}

// purgeLineageByTargetIDs deletes lineage rows whose target row was deleted.
// Called from every recordings-deletion path (retention batches, orphan
// sweep) so the table tracks the lifecycle of its targets, not wall-clock
// guesses.
func (d *DB) purgeLineageByTargetIDs(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	var removed int64
	for len(ids) > 0 {
		batch := ids
		if len(batch) > 500 {
			batch = batch[:500]
		}
		ids = ids[len(batch):]
		placeholders := strings.Repeat("?,", len(batch))
		placeholder := "(" + strings.TrimSuffix(placeholders, ",") + ")"
		args := make([]interface{}, len(batch))
		for i, id := range batch {
			args[i] = id
		}
		if err := RetryOnBusy(ctx, func() error {
			res, err := d.db.ExecContext(ctx, "DELETE FROM merge_lineage WHERE merged_id IN "+placeholder, args...)
			if err != nil {
				return err
			}
			if n, err := res.RowsAffected(); err == nil {
				removed += n
			}
			return nil
		}); err != nil {
			return fmt.Errorf("purge merge lineage: %w", err)
		}
	}
	_ = removed
	return nil
}
