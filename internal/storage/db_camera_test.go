package storage

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestUpdateCameraEncoding verifies the single-column runtime backfill for the
// encoding column (issue #112). UpsertCamera is a full-row overwrite, so a
// dedicated UpdateCameraEncoding is needed to persist a recorder-resolved codec
// without clobbering unrelated columns.
func TestUpdateCameraEncoding(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test_enc.db")
	db, err := New(dbPath)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, db.Init(ctx))
	t.Cleanup(func() { _ = db.Close() })

	const camID = "cam-enc"
	// Insert a camera with empty encoding (the auto-detect / ESP32 MiBeeCam case).
	require.NoError(t, db.UpsertCamera(ctx, camID, "Enc Cam", "onvif", "", "", "", "", "", "", "", ""))

	// Initially empty.
	enc, err := db.GetCameraEncoding(ctx, camID)
	require.NoError(t, err)
	require.Equal(t, "", enc)

	// Runtime-resolved JPEG via the single-column UPDATE (mirrors ensureEncoding).
	require.NoError(t, db.UpdateCameraEncoding(ctx, camID, "jpeg"))
	enc, err = db.GetCameraEncoding(ctx, camID)
	require.NoError(t, err)
	require.Equal(t, "jpeg", enc)

	// stream_encoding (ONVIF uppercase form) round-trip.
	require.NoError(t, db.UpdateCameraStreamEncoding(ctx, camID, "H265"))
}

// TestUpdateCameraEncoding_IdempotentOnMissingRow confirms the UPDATE is a
// no-op (not an error) when the camera row doesn't exist — matching
// UpdateCameraStableID's contract. ensureEncoding is best-effort and must not
// fail hard on a race where the camera was removed between probe and persist.
func TestUpdateCameraEncoding_IdempotentOnMissingRow(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test_enc2.db")
	db, err := New(dbPath)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, db.Init(ctx))
	t.Cleanup(func() { _ = db.Close() })

	// No row exists — UPDATE affects 0 rows, no error.
	require.NoError(t, db.UpdateCameraEncoding(ctx, "never-existed", "h264"))
	require.NoError(t, db.UpdateCameraStreamEncoding(ctx, "never-existed", "H264"))

	enc, err := db.GetCameraEncoding(ctx, "never-existed")
	require.NoError(t, err)
	require.Equal(t, "", enc, "GetCameraEncoding returns empty for a missing row, not an error")
}
