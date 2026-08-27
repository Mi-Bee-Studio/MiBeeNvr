package cleanup

// Error-path tests for ArchiveDeleter.processTask (closed DB forces every
// step to fail — non-fatal logging paths must hold) and the fragmentation
// branch of performDatabaseMaintenance. See #565.

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestProcessTask_AllStepsFail_NoPanic(t *testing.T) {
	env := newTestEnv(t)
	defer env.close(t)

	createPendingTask(t, env.db, "doomed")
	d := NewArchiveDeleter(env.db, env.store)

	// Close the DB first: every step inside processTask now errors. The worker
	// must survive (log + continue) — a cleanup task failing must never crash
	// the loop.
	env.db.Close()
	d.processTask(context.Background(), storage.ArchiveCleanupTask{CameraID: "doomed", Status: "pending"})
}

func TestProcessTask_RecordsFailureStatus(t *testing.T) {
	env := newTestEnv(t)
	defer env.close(t)

	// Recording delete fails because the recordings table was dropped.
	_, err := env.db.DB().ExecContext(context.Background(), `DROP TABLE recordings;`)
	require.NoError(t, err)
	createPendingTask(t, env.db, "broken")

	d := NewArchiveDeleter(env.db, env.store)
	d.processTask(context.Background(), storage.ArchiveCleanupTask{CameraID: "broken", Status: "pending"})

	tasks, err := env.db.ListActiveArchiveCleanupTasks(context.Background())
	require.NoError(t, err)
	for _, task := range tasks {
		if task.CameraID == "broken" {
			require.Equal(t, "failed", task.Status, "recording-delete failure should mark the task failed")
		}
	}
}

func TestPerformDatabaseMaintenance_FragmentationChurn(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	defer env.close(t)

	cfg := defaultCleanupConfig()
	cm, err := NewCleanupManager(env.db, env.store, cfg)
	require.NoError(t, err)

	// Create free pages: insert rows with ~2KB payloads, then delete most.
	payload := strings.Repeat("x", 2048)
	for i := range 200 {
		require.NoError(t, env.db.InsertRecording(ctx, &model.Recording{
			ID:        fmt.Sprintf("frag-%03d-%s", i, payload[:8]),
			CameraID:  "cam1",
			FilePath:  payload,
			Format:    model.FormatH264,
			StartedAt: time.Now().UTC(),
			EndedAt:   time.Now().UTC(),
			Duration:  1,
		}))
	}
	ids := make([]string, 0, 150)
	rows, err := env.db.DB().QueryContext(ctx, `SELECT id FROM recordings;`)
	require.NoError(t, err)
	for rows.Next() {
		var id string
		require.NoError(t, rows.Scan(&id))
		ids = append(ids, id)
	}
	require.NoError(t, rows.Close())
	if len(ids) > 50 {
		_, err := env.db.DeleteRecordingsBatch(ctx, ids[:len(ids)-50])
		require.NoError(t, err)
	}

	// Whatever branch the actual ratio selects (no-op / incremental vacuum /
	// online compaction), maintenance must complete without error.
	cm.performDatabaseMaintenance(ctx)
}
