package storage

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func newTestDB(t *testing.T) *DB {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := New(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	require.NoError(t, db.Init(ctx))
	return db
}

func TestGetFeatureFlags_DefaultValues(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	flags, err := db.GetFeatureFlags(ctx)
	require.NoError(t, err)
	require.Equal(t, true, flags["protocol.xiaomi"])
	require.Equal(t, true, flags["protocol.rtsp"])
	require.Equal(t, true, flags["protocol.http"])
	require.Equal(t, true, flags["protocol.onvif"])
}

func TestSetAndGetFeatureFlag(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	// Override a default
	err := db.SetFeatureFlag(ctx, "protocol.xiaomi", false)
	require.NoError(t, err)

	val, err := db.GetFeatureFlag(ctx, "protocol.xiaomi", true)
	require.NoError(t, err)
	require.False(t, val)

	// Set back to true
	err = db.SetFeatureFlag(ctx, "protocol.xiaomi", true)
	require.NoError(t, err)

	val, err = db.GetFeatureFlag(ctx, "protocol.xiaomi", false)
	require.NoError(t, err)
	require.True(t, val)
}

func TestGetFeatureFlag_NonExistent(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	val, err := db.GetFeatureFlag(ctx, "protocol.nonexistent", true)
	require.NoError(t, err)
	require.True(t, val)

	val, err = db.GetFeatureFlag(ctx, "protocol.nonexistent", false)
	require.NoError(t, err)
	require.False(t, val)
}

func TestSetFeatureFlag_NewKey(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	err := db.SetFeatureFlag(ctx, "custom.flag", true)
	require.NoError(t, err)

	val, err := db.GetFeatureFlag(ctx, "custom.flag", false)
	require.NoError(t, err)
	require.True(t, val)

	// Verify it appears in GetFeatureFlags
	flags, err := db.GetFeatureFlags(ctx)
	require.NoError(t, err)
	require.True(t, flags["custom.flag"])
}
