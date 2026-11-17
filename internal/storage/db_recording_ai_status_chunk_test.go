package storage

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

// Regression test for #1173. Cleanup's BatchDeleteRecordingsWithFiles hands
// this method the FULL expired set of a camera in one call, and an audio-only
// camera (no merge consumer, born-terminal rows) accumulates more expired
// rows than SQLite's SQLITE_MAX_VARIABLE_NUMBER (32766 on modernc builds).
// The method used to build one WHERE id IN (...) with a placeholder per id,
// so the prepare failed with "too many SQL variables" and the whole deletion
// batch aborted on every cleanup cycle — retention deletion never converged
// for that camera. The IN list must be chunked (same 500-id rail as
// GetRecordingsByPathSet).
func TestBatchGetRecordingAIStatusIDSetOverVariableLimit(t *testing.T) {
	dir := t.TempDir()
	db, err := New(filepath.Join(dir, "test.db"))
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, db.Init(ctx))
	defer db.Close()

	ids := make([]string, 40000)
	for i := range ids {
		ids[i] = "rec-" + strconv.Itoa(i)
	}

	statuses, err := db.BatchGetRecordingAIStatus(ctx, ids)
	require.NoError(t, err)
	require.NotNil(t, statuses)
	require.Empty(t, statuses) // no such rows — empty result, not a prepare error
}
