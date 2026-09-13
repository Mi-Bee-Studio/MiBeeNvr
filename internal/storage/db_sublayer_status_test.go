package storage

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/stretchr/testify/require"
)

// Sub-layer terminal-status suite (#763): tierrec layer=1 rows are standalone
// 60s archives that never enter the merge pipeline, yet they were inserted
// with merge_status='pending' and never reached a terminal state — 98% of all
// pending rows on the production box, polluting every pending-based
// diagnostic and crowding the ghost-row sweep's LIMIT.

func seedSubRow(t *testing.T, db *DB, id string, layer int, mergeStatus string, started time.Time) {
	t.Helper()
	require.NoError(t, db.InsertRecording(context.Background(), &model.Recording{
		ID: id, CameraID: "cam-tier", Format: model.FormatH264, FilePath: "/p/" + id,
		StartedAt: started, EndedAt: started.Add(60 * time.Second),
		FileSize: 2048, MergeStatus: mergeStatus, Layer: layer,
	}))
}

// TestSublayerRowsMigratedToTerminalStatus: Init's idempotent data migration
// flips layer=1 'pending' rows to the 'sublayer' terminal status; layer=0
// rows are untouched; re-running Init is a no-op.
func TestSublayerRowsMigratedToTerminalStatus(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := context.Background()
	base := time.Date(2025, 6, 1, 10, 0, 0, 0, time.UTC)

	seedSubRow(t, db, "sub-pending", 1, "pending", base)
	seedSubRow(t, db, "sub-already-terminal", 1, "merged", base.Add(time.Minute))
	seedSubRow(t, db, "main-pending", 0, "pending", base.Add(2*time.Minute))

	// Re-run Init — the migration lives there (idempotent, every boot).
	require.NoError(t, db.Init(ctx))

	sub, err := db.GetRecording(ctx, "sub-pending")
	require.NoError(t, err)
	require.Equal(t, model.MergeStatusSublayer, sub.MergeStatus, "layer=1 pending rows must migrate to the terminal 'sublayer' status")
	require.Equal(t, 1, sub.Layer)

	untouchedTerminal, err := db.GetRecording(ctx, "sub-already-terminal")
	require.NoError(t, err)
	require.Equal(t, "merged", untouchedTerminal.MergeStatus, "migration must only touch 'pending' rows")

	mainRow, err := db.GetRecording(ctx, "main-pending")
	require.NoError(t, err)
	require.Equal(t, "pending", mainRow.MergeStatus, "layer=0 pending rows are real merge backlog — untouched")

	// Idempotent: second Init changes nothing.
	require.NoError(t, db.Init(ctx))
	subAgain, err := db.GetRecording(ctx, "sub-pending")
	require.NoError(t, err)
	require.Equal(t, model.MergeStatusSublayer, subAgain.MergeStatus)
}

// TestStalePendingSweepSkipsSublayerZombies: layer=1 'pending' zombies must
// not crowd the ghost-row sweep's LIMIT — they are healthy archives, and
// each one returned starves a real (layer=0) ghost of its sweep slot.
func TestStalePendingSweepSkipsSublayerZombies(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := context.Background()
	base := time.Date(2025, 6, 1, 10, 0, 0, 0, time.UTC)
	cutoff := base.Add(24 * time.Hour)

	// Zombies: older than the ghost so ORDER BY started_at returns them first.
	for i := range 5 {
		seedSubRow(t, db, fmt.Sprintf("zombie-%d", i), 1, "pending", base.Add(time.Duration(i)*time.Second))
	}
	seedSubRow(t, db, "real-ghost", 0, "pending", base.Add(time.Minute))

	rows, err := db.ListStalePendingRecordings(ctx, cutoff, 3)
	require.NoError(t, err)
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	require.Equal(t, []string{"real-ghost"}, ids,
		"layer=1 rows must not consume the sweep budget; the layer=0 ghost must be visible even with a tiny LIMIT")
}

// TestStalePendingSweepCoversTerminalSublayer: 'sublayer'-status rows whose
// file vanished are still ghost rows — the sweep must cover the terminal
// class too (otherwise terminal+missing becomes the next zombie class).
func TestStalePendingSweepCoversTerminalSublayer(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := context.Background()
	base := time.Date(2025, 6, 1, 10, 0, 0, 0, time.UTC)
	cutoff := base.Add(24 * time.Hour)

	seedSubRow(t, db, "sub-ghost", 1, "sublayer", base)
	seedSubRow(t, db, "sub-fresh", 1, "sublayer", base.Add(48*time.Hour))

	rows, err := db.ListStalePendingRecordings(ctx, cutoff, 100)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "sub-ghost", rows[0].ID)
}
