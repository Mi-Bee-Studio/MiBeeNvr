package cleanup

// Tests for the dark-segment cleanup strategy (dark_segments.go): recordings
// marked merge_status='dark' are deleted after a 1h grace period. Both sides
// of the grace boundary are asserted. See #565.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/stretchr/testify/require"
)

// insertDarkRecording inserts a recording with merge_status='dark' and a file
// on disk, ended at the given time.
func insertDarkRecording(t *testing.T, env *testEnv, id string, endedAt time.Time) {
	t.Helper()
	ctx := context.Background()
	env.insertTestRecording(t, id, "cam1", "cam1/"+id+".mp4", endedAt, false)
	require.NoError(t, env.db.SetMergeStatus(ctx, []string{id}, "dark"))
	rec, err := env.db.GetRecording(ctx, id)
	require.NoError(t, err)
	require.NotNil(t, rec)
	require.Equal(t, model.MergeStatusDark, rec.MergeStatus, "fixture setup: recording must be dark before cleanup runs")
}

func TestDarkSegmentCleanup_AfterGraceDeleted(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	defer env.close(t)

	// Ended 2h ago — beyond the 1h grace period.
	insertDarkRecording(t, env, "dark-old", time.Now().UTC().Add(-2*time.Hour))

	cfg := defaultCleanupConfig()
	cm, err := NewCleanupManager(env.db, env.store, cfg)
	require.NoError(t, err)
	require.NoError(t, cm.RunOnce(ctx))

	require.False(t, recordingExists(t, env, "dark-old"), "dark segment past grace should be deleted from DB")
	_, err = os.Stat(filepath.Join(env.store.RootDir(), "cam1", "dark-old.mp4"))
	require.True(t, os.IsNotExist(err), "dark segment file past grace should be deleted")
}

func TestDarkSegmentCleanup_WithinGraceKept(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	defer env.close(t)

	// Ended 10m ago — inside the 1h grace period, still briefly visible.
	insertDarkRecording(t, env, "dark-fresh", time.Now().UTC().Add(-10*time.Minute))

	cfg := defaultCleanupConfig()
	cm, err := NewCleanupManager(env.db, env.store, cfg)
	require.NoError(t, err)
	require.NoError(t, cm.RunOnce(ctx))

	require.True(t, recordingExists(t, env, "dark-fresh"), "dark segment within grace must survive one cleanup cycle")
}
