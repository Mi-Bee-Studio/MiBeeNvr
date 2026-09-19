package transcoding

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/stretchr/testify/require"
)

// TestNewTranscodeQueuePendingCapDefaults pins the #848 normalization:
// zero means the bounded default, negative opts out.
func TestNewTranscodeQueuePendingCapDefaults(t *testing.T) {
	db := newTestQueueDB(t)
	base := newTestQueue(t, db, 1)

	for _, tc := range []struct {
		configured int
		want       int
	}{{0, defaultMaxPendingTasks}, {-1, -1}, {5, 5}} {
		q := NewTranscodeQueue(db, base.caps, base.downloader, QueueConfig{
			MaxWorkers:      1,
			FFmpegPath:      base.config.FFmpegPath,
			FFprobePath:     base.config.FFprobePath,
			MaxPendingTasks: tc.configured,
		}, base.m)
		require.Equal(t, tc.want, q.config.MaxPendingTasks, "configured %d", tc.configured)
	}
}

func TestEnqueuePendingCap(t *testing.T) {
	db := newTestQueueDB(t)
	q := newTestQueue(t, db, 1)
	q.config.MaxPendingTasks = 2

	ctx := context.Background()
	dir := t.TempDir()
	newTask := func(n string) *storage.TranscodeTask {
		input := filepath.Join(dir, n+".mp4")
		return &storage.TranscodeTask{
			CameraID:     "cam-test",
			RecordingID:  n,
			InputPath:    input,
			InputFormat:  "h265",
			OutputPath:   input + ".transcoded.mp4",
			OutputFormat: "h264",
		}
	}

	require.NoError(t, q.Enqueue(ctx, newTask("rec-1")))
	require.NoError(t, q.Enqueue(ctx, newTask("rec-2")))

	err := q.Enqueue(ctx, newTask("rec-3"))
	require.ErrorContains(t, err, "transcode queue full")

	// Negative cap = unlimited — the escape hatch still enqueues.
	q.config.MaxPendingTasks = -1
	require.NoError(t, q.Enqueue(ctx, newTask("rec-3")))

	pending, err := db.CountTasksByStatus(ctx, "pending")
	require.NoError(t, err)
	require.Equal(t, int64(3), pending)
}
