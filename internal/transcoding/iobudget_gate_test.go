package transcoding

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/iobudget"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/stretchr/testify/require"
)

// fakeBudget records Wait calls for assertion.
type fakeBudget struct {
	consumer string
	bytes    []int64
}

func (f *fakeBudget) Wait(ctx context.Context, consumer string, n int64) error {
	f.consumer = consumer
	f.bytes = append(f.bytes, n)
	return nil
}

func TestWaitTranscodeIOBudgetNilIsNoOp(t *testing.T) {
	SetIOBudget(nil)
	require.NoError(t, waitTranscodeIOBudget(context.Background(), 1<<20))
}

func TestWaitTranscodeIOBudgetBillsTranscodeConsumer(t *testing.T) {
	fake := &fakeBudget{}
	SetIOBudget(fake)
	t.Cleanup(func() { SetIOBudget(nil) })

	require.NoError(t, waitTranscodeIOBudget(context.Background(), 4096))
	require.Equal(t, iobudget.ConsumerTranscode, fake.consumer)
	require.Equal(t, []int64{4096}, fake.bytes)
}

// TestRunWorkerChargesIOBudget verifies the #848 wiring end to end: a task
// run through runWorker bills 2× the input file size to the shared budget
// under the transcode consumer, before ffmpeg is spawned.
func TestRunWorkerChargesIOBudget(t *testing.T) {
	db := newTestQueueDB(t)
	q := newTestQueue(t, db, 1)

	dir := t.TempDir()
	payload := make([]byte, 8192)
	for i := range payload {
		payload[i] = byte(i)
	}
	input := filepath.Join(dir, "cam_seg.mp4")
	require.NoError(t, os.WriteFile(input, payload, 0o644))

	task := &storage.TranscodeTask{
		CameraID:     "cam-test",
		RecordingID:  "rec-1",
		InputPath:    input,
		InputFormat:  "h265",
		OutputPath:   input + ".transcoded.mp4",
		OutputFormat: "h264",
	}
	require.NoError(t, db.EnqueueTask(context.Background(), task))
	require.NotZero(t, task.ID)

	fake := &fakeBudget{}
	SetIOBudget(fake)
	t.Cleanup(func() { SetIOBudget(nil) })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	q.runWorker(ctx, task)

	require.Equal(t, iobudget.ConsumerTranscode, fake.consumer)
	require.Equal(t, []int64{int64(len(payload)) * 2}, fake.bytes,
		"task must bill input size × 2 (read + estimated output)")

	got, err := db.GetTaskByID(context.Background(), task.ID)
	require.NoError(t, err)
	require.Equal(t, "completed", got.Status, "task should still complete normally under a non-blocking budget")
}

// TestRunWorkerCancelsWhenBudgetWaitInterrupted verifies shutdown semantics:
// a budget wait that fails with the worker context error cancels the task
// instead of running it unthrottled.
func TestRunWorkerCancelsWhenBudgetWaitInterrupted(t *testing.T) {
	db := newTestQueueDB(t)
	q := newTestQueue(t, db, 1)

	dir := t.TempDir()
	input := filepath.Join(dir, "cam_seg.mp4")
	require.NoError(t, os.WriteFile(input, make([]byte, 1024), 0o644))

	task := &storage.TranscodeTask{
		CameraID:     "cam-test",
		RecordingID:  "rec-1",
		InputPath:    input,
		InputFormat:  "h265",
		OutputPath:   input + ".transcoded.mp4",
		OutputFormat: "h264",
	}
	require.NoError(t, db.EnqueueTask(context.Background(), task))

	SetIOBudget(errBudget{})
	t.Cleanup(func() { SetIOBudget(nil) })

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already-expired worker context
	q.runWorker(ctx, task)

	got, err := db.GetTaskByID(context.Background(), task.ID)
	require.NoError(t, err)
	require.Equal(t, "cancelled", got.Status)
}

type errBudget struct{}

func (errBudget) Wait(ctx context.Context, consumer string, n int64) error {
	return ctx.Err()
}
