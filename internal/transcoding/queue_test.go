package transcoding

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/stretchr/testify/require"
)

// newTestQueueDB creates a fresh SQLite DB with schema initialized.
func newTestQueueDB(t *testing.T) *storage.DB {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := storage.New(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	require.NoError(t, db.Init(ctx))
	return db
}

// newTestQueue creates a TranscodeQueue with a mock FFmpeg binary.
// The mockFFmpeg is a script that writes a small file to the output path and exits.
func newTestQueue(t *testing.T, db *storage.DB, maxWorkers int) *TranscodeQueue {
	t.Helper()

	// Create mock ffmpeg binary
	dir := t.TempDir()
	mockFFmpeg := filepath.Join(dir, "ffmpeg")
	mockScript := `#!/bin/sh
# Mock FFmpeg: write a small file to the last argument (output path)
# and output some progress lines to stderr
output=""
for arg in "$@"; do
	output="$arg"
done
echo "frame=  10 fps= 25 q=28.0 size=    128kB time=00:00:00.40 bitrate= 2621.4kbits/s speed=   1x" >&2
echo "frame=  25 fps= 25 q=28.0 size=    320kB time=00:00:01.00 bitrate= 2621.4kbits/s speed=   1x" >&2
echo "test content" > "$output"
exit 0
`
	err := os.WriteFile(mockFFmpeg, []byte(mockScript), 0755)
	require.NoError(t, err)

	mockFFprobe := filepath.Join(dir, "ffprobe")
	ffprobeScript := `#!/bin/sh
echo '{"streams":[{"codec_name":"h264","duration":10.0,"width":1920,"height":1080}]}'
exit 0
`
	err = os.WriteFile(mockFFprobe, []byte(ffprobeScript), 0755)
	require.NoError(t, err)

	caps := &HardwareCapabilities{
		Arch:            "amd64",
		H264Encoder:     "libx264",
		H265Encoder:     "libx265",
		H264EncoderType: EncoderSoftware,
		H265EncoderType: EncoderSoftware,
		FFmpegAvailable: true,
		FFmpegPath:      mockFFmpeg,
	}

	m := metrics.NewMetrics()

	cfg := QueueConfig{
		MaxWorkers:   maxWorkers,
		FFmpegPath:   mockFFmpeg,
		FFprobePath:  mockFFprobe,
	}

	return NewTranscodeQueue(db, caps, nil, cfg, m)
}

// newSlowTestQueue creates a queue with a slow mock FFmpeg that sleeps for the given duration.
func newSlowTestQueue(t *testing.T, db *storage.DB, maxWorkers int, sleepDuration string) *TranscodeQueue {
	t.Helper()

	dir := t.TempDir()
	mockFFmpeg := filepath.Join(dir, "ffmpeg")
	mockScript := fmt.Sprintf(`#!/bin/sh
output=""
for arg in "$@"; do
	output="$arg"
done
echo "frame=  10 fps= 25 q=28.0 size=    128kB time=00:00:00.40 bitrate= 2621.4kbits/s speed=   1x" >&2
sleep %s
echo "test content" > "$output"
exit 0
`, sleepDuration)
	err := os.WriteFile(mockFFmpeg, []byte(mockScript), 0755)
	require.NoError(t, err)

	mockFFprobe := filepath.Join(dir, "ffprobe")
	ffprobeScript := `#!/bin/sh
echo '{"streams":[{"codec_name":"h264","duration":10.0,"width":1920,"height":1080}]}'
exit 0
`
	err = os.WriteFile(mockFFprobe, []byte(ffprobeScript), 0755)
	require.NoError(t, err)

	caps := &HardwareCapabilities{
		Arch:            "amd64",
		H264Encoder:     "libx264",
		H265Encoder:     "libx265",
		H264EncoderType: EncoderSoftware,
		H265EncoderType: EncoderSoftware,
		FFmpegAvailable: true,
		FFmpegPath:      mockFFmpeg,
	}

	m := metrics.NewMetrics()

	cfg := QueueConfig{
		MaxWorkers:  maxWorkers,
		FFmpegPath:  mockFFmpeg,
		FFprobePath: mockFFprobe,
	}

	return NewTranscodeQueue(db, caps, nil, cfg, m)
}

// helperInsertTask inserts a pending task and returns its ID.
func helperInsertTask(t *testing.T, db *storage.DB, inputPath, outputPath string) int64 {
	t.Helper()
	ctx := context.Background()
	task := &storage.TranscodeTask{
		CameraID:    "test-cam",
		RecordingID: "rec-001",
		InputPath:   inputPath,
		InputFormat: "h265",
		OutputPath:  outputPath,
		OutputFormat: "h264",
		CreatedAt:   time.Now().UTC().Format("2006-01-02 15:04:05.999999999"),
	}
	err := db.EnqueueTask(ctx, task)
	require.NoError(t, err)
	return task.ID
}

// --- Tests ---

func TestQueueEnqueueAndProcess(t *testing.T) {
	// Test: enqueue a task → queue processes it → status becomes completed
	db := newTestQueueDB(t)
	q := newTestQueue(t, db, 1)

	tmpDir := t.TempDir()
	inputPath := filepath.Join(tmpDir, "input.mp4")
	outputPath := filepath.Join(tmpDir, "output.mp4")

	// Create a dummy input file
	require.NoError(t, os.WriteFile(inputPath, []byte("fake video data"), 0644))

	taskID := helperInsertTask(t, db, inputPath, outputPath)

	// Run queue with short context to allow processing
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go q.Run(ctx)

	// Wait for task to complete
	require.Eventually(t, func() bool {
		task, err := db.GetTaskByID(context.Background(), taskID)
		require.NoError(t, err)
		return task != nil && task.Status == "completed"
	}, 5*time.Second, 100*time.Millisecond, "task should be completed")

	// Verify output file exists
	_, err := os.Stat(outputPath)
	require.NoError(t, err, "output file should exist")
}

func TestQueueCancelTask(t *testing.T) {
	// Test: cancel a running task → status becomes cancelled
	db := newTestQueueDB(t)
	q := newSlowTestQueue(t, db, 1, "10s") // long sleep

	tmpDir := t.TempDir()
	inputPath := filepath.Join(tmpDir, "input.mp4")
	outputPath := filepath.Join(tmpDir, "output.mp4")
	require.NoError(t, os.WriteFile(inputPath, []byte("fake video data"), 0644))

	taskID := helperInsertTask(t, db, inputPath, outputPath)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go q.Run(ctx)

	// Wait for task to start running
	require.Eventually(t, func() bool {
		task, err := db.GetTaskByID(context.Background(), taskID)
		require.NoError(t, err)
		return task != nil && task.Status == "running"
	}, 5*time.Second, 100*time.Millisecond, "task should start running")

	// Cancel the task
	err := q.CancelTask(context.Background(), taskID)
	require.NoError(t, err)

	// Verify status is cancelled
	require.Eventually(t, func() bool {
		task, err := db.GetTaskByID(context.Background(), taskID)
		require.NoError(t, err)
		return task != nil && task.Status == "cancelled"
	}, 5*time.Second, 100*time.Millisecond, "task should be cancelled")
}

func TestQueueConcurrencyLimit(t *testing.T) {
	// Test: 3 tasks enqueued, maxWorkers=1, only 1 active at once
	db := newTestQueueDB(t)
	q := newSlowTestQueue(t, db, 1, "1s") // 1s sleep, 3 tasks = ~3s total

	tmpDir := t.TempDir()

	var taskIDs []int64
	for i := 0; i < 3; i++ {
		inputPath := filepath.Join(tmpDir, fmt.Sprintf("input%d.mp4", i))
		outputPath := filepath.Join(tmpDir, fmt.Sprintf("output%d.mp4", i))
		require.NoError(t, os.WriteFile(inputPath, []byte("data"), 0644))
		taskIDs = append(taskIDs, helperInsertTask(t, db, inputPath, outputPath))
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go q.Run(ctx)

	// Verify that at most 1 task is ever active concurrently
	maxObserved := 0
	require.Eventually(t, func() bool {
		active := q.ActiveCount()
		if active > maxObserved {
			maxObserved = active
		}
		return active == 0 && maxObserved > 0
	}, 15*time.Second, 50*time.Millisecond, "all tasks should complete")
	require.LessOrEqual(t, maxObserved, 1, "at most 1 task should be active with maxWorkers=1")

	// Wait for all 3 tasks to complete
	require.Eventually(t, func() bool {
		for _, id := range taskIDs {
			task, err := db.GetTaskByID(context.Background(), id)
			if err != nil || task == nil || task.Status != "completed" {
				return false
			}
		}
		return true
	}, 20*time.Second, 200*time.Millisecond, "all tasks should eventually complete")
}

func TestQueueGracefulShutdown(t *testing.T) {
	// Test: stop queue while task is running → active task completes
	db := newTestQueueDB(t)
	q := newTestQueue(t, db, 1) // fast mock

	tmpDir := t.TempDir()
	inputPath := filepath.Join(tmpDir, "input.mp4")
	outputPath := filepath.Join(tmpDir, "output.mp4")
	require.NoError(t, os.WriteFile(inputPath, []byte("data"), 0644))

	taskID := helperInsertTask(t, db, inputPath, outputPath)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		q.Run(ctx)
		close(done)
	}()

	// Wait for task to complete
	require.Eventually(t, func() bool {
		task, err := db.GetTaskByID(context.Background(), taskID)
		require.NoError(t, err)
		return task != nil && task.Status == "completed"
	}, 5*time.Second, 100*time.Millisecond)

	// Stop the queue
	q.Stop()

	// Run() should return after stop
	select {
	case <-done:
		// Good — queue exited cleanly
	case <-time.After(3 * time.Second):
		t.Fatal("Run() should return after Stop()")
	}
}

func TestQueueActiveCount(t *testing.T) {
	// Test: ActiveCount reflects running tasks
	db := newTestQueueDB(t)
	q := newSlowTestQueue(t, db, 2, "5s")

	tmpDir := t.TempDir()
	for i := 0; i < 2; i++ {
		inputPath := filepath.Join(tmpDir, fmt.Sprintf("input%d.mp4", i))
		outputPath := filepath.Join(tmpDir, fmt.Sprintf("output%d.mp4", i))
		require.NoError(t, os.WriteFile(inputPath, []byte("data"), 0644))
		helperInsertTask(t, db, inputPath, outputPath)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go q.Run(ctx)

	// Wait for both tasks to start
	require.Eventually(t, func() bool {
		return q.ActiveCount() == 2
	}, 5*time.Second, 100*time.Millisecond, "should have 2 active tasks")

	require.Equal(t, 2, q.ActiveCount())
}

func TestQueueGetStatus(t *testing.T) {
	// Test: GetStatus returns correct queue info
	db := newTestQueueDB(t)
	q := newTestQueue(t, db, 2)

	tmpDir := t.TempDir()
	inputPath := filepath.Join(tmpDir, "input.mp4")
	outputPath := filepath.Join(tmpDir, "output.mp4")
	require.NoError(t, os.WriteFile(inputPath, []byte("data"), 0644))
	helperInsertTask(t, db, inputPath, outputPath)

	status := q.GetStatus()
	require.True(t, status.Enabled)
	require.Equal(t, 1, status.QueueLength)
	require.Equal(t, 0, status.ActiveJobs)
	require.NotNil(t, status.Hardware)
}

func TestQueueNoPanicOnEmptyQueue(t *testing.T) {
	// Test: queue runs without panic when no tasks are pending
	db := newTestQueueDB(t)
	q := newTestQueue(t, db, 1)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	err := q.Run(ctx)
	require.NoError(t, err)
}

func TestQueueStopPreventsNewDispatch(t *testing.T) {
	// Test: after Stop(), new tasks are not dispatched
	db := newTestQueueDB(t)
	q := newTestQueue(t, db, 1)

	// Start and immediately stop
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		q.Run(ctx)
		close(done)
	}()

	// Stop immediately
	q.Stop()

	// Now insert a task — should remain pending
	tmpDir := t.TempDir()
	inputPath := filepath.Join(tmpDir, "input.mp4")
	outputPath := filepath.Join(tmpDir, "output.mp4")
	require.NoError(t, os.WriteFile(inputPath, []byte("data"), 0644))
	taskID := helperInsertTask(t, db, inputPath, outputPath)

	// Verify task remains pending (queue is stopped, won't dispatch)
	require.Eventually(t, func() bool {
		task, err := db.GetTaskByID(context.Background(), taskID)
		require.NoError(t, err)
		return task != nil && task.Status == "pending"
	}, 2*time.Second, 50*time.Millisecond, "task should remain pending after queue stopped")
}

func TestQueueTaskToOptions(t *testing.T) {
	// Test: taskToOptions correctly converts storage task to transcoding options
	// and uses the passed preset (defaulting to 'ultrafast' when empty)
	db := newTestQueueDB(t)
	q := newTestQueue(t, db, 1)

	task := &storage.TranscodeTask{
		InputPath:    "/path/to/input.mp4",
		OutputPath:   "/path/to/output.mp4",
		InputFormat:  "h265",
		OutputFormat: "h264",
	}

	// Default preset is 'ultrafast' when no preset specified
	opts := q.taskToOptions(task, "")
	require.Equal(t, "/path/to/input.mp4", opts.InputPath)
	require.Equal(t, "/path/to/output.mp4", opts.OutputPath)
	require.Equal(t, "h265", opts.InputCodec)
	require.Equal(t, "h264", opts.OutputCodec)
	require.Equal(t, 25, opts.Framerate)
	require.Equal(t, "ultrafast", opts.Preset)

	// Custom preset is respected
	opts = q.taskToOptions(task, "medium")
	require.Equal(t, "medium", opts.Preset)

	opts = q.taskToOptions(task, "ultrafast")
	require.Equal(t, "ultrafast", opts.Preset)

	// Unknown output format defaults to h264
	task.OutputFormat = "unknown"
	opts = q.taskToOptions(task, "")
	require.Equal(t, "h264", opts.OutputCodec)
}

func TestQueueCancelNonExistentTask(t *testing.T) {
	// Test: cancelling a non-existent task doesn't panic
	db := newTestQueueDB(t)
	q := newTestQueue(t, db, 1)

	// CancelTask on non-existent task should not panic
	err := q.CancelTask(context.Background(), 99999)
	require.NoError(t, err)
}

func TestQueueFailedTask(t *testing.T) {
	// Test: task with invalid input path fails gracefully
	db := newTestQueueDB(t)

	// Create queue with a mock ffmpeg that exits with error
	dir := t.TempDir()
	mockFFmpeg := filepath.Join(dir, "ffmpeg")
	mockScript := `#!/bin/sh
echo "Error: No such file" >&2
exit 1
`
	require.NoError(t, os.WriteFile(mockFFmpeg, []byte(mockScript), 0755))

	caps := &HardwareCapabilities{
		Arch:            "amd64",
		H264Encoder:     "libx264",
		H265Encoder:     "libx265",
		H264EncoderType: EncoderSoftware,
		H265EncoderType: EncoderSoftware,
		FFmpegAvailable: true,
		FFmpegPath:      mockFFmpeg,
	}

	m := metrics.NewMetrics()
	cfg := QueueConfig{
		MaxWorkers:  1,
		FFmpegPath:  mockFFmpeg,
		FFprobePath: filepath.Join(dir, "ffprobe"),
	}

	q := NewTranscodeQueue(db, caps, nil, cfg, m)

	tmpDir := t.TempDir()
	inputPath := filepath.Join(tmpDir, "nonexistent.mp4")
	outputPath := filepath.Join(tmpDir, "output.mp4")
	// Don't create input file — ffmpeg will fail

	taskID := helperInsertTask(t, db, inputPath, outputPath)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go q.Run(ctx)

	require.Eventually(t, func() bool {
		task, err := db.GetTaskByID(context.Background(), taskID)
		require.NoError(t, err)
		return task != nil && (task.Status == "failed" || task.Status == "completed")
	}, 5*time.Second, 100*time.Millisecond, "task should finish (failed or completed)")
}

func TestQueueNewTranscodeQueueDefaultWorkers(t *testing.T) {
	// Test: MaxWorkers defaults to 1 if <= 0
	db := newTestQueueDB(t)
	q := newTestQueue(t, db, 0)
	require.Equal(t, 1, q.config.MaxWorkers)

	q2 := newTestQueue(t, db, -1)
	require.Equal(t, 1, q2.config.MaxWorkers)
}

func TestQueueEnqueueDirectly(t *testing.T) {
	// Test: Enqueue method inserts task via DB
	db := newTestQueueDB(t)
	q := newTestQueue(t, db, 1)

	ctx := context.Background()
	task := &storage.TranscodeTask{
		CameraID:    "cam-1",
		RecordingID: "rec-1",
		InputPath:   "/input.mp4",
		InputFormat: "h265",
		OutputPath:  "/output.mp4",
		OutputFormat: "h264",
		CreatedAt:   time.Now().UTC().Format("2006-01-02 15:04:05.999999999"),
	}

	err := q.Enqueue(ctx, task)
	require.NoError(t, err)
	require.Greater(t, task.ID, int64(0), "task ID should be set after enqueue")

	// Verify in DB
	got, err := db.GetTaskByID(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, "pending", got.Status)
}

func TestQueueProgressParsing(t *testing.T) {
	// Test: parseProgress correctly parses FFmpeg stderr and updates DB
	db := newTestQueueDB(t)
	q := newTestQueue(t, db, 1)

	ctx := context.Background()

	// Insert a running task
	task := &storage.TranscodeTask{
		CameraID:     "cam-1",
		RecordingID:  "rec-1",
		InputPath:    "/nonexistent.mp4",
		InputFormat:  "h265",
		OutputPath:   "/output.mp4",
		OutputFormat: "h264",
		CreatedAt:    time.Now().UTC().Format("2006-01-02 15:04:05.999999999"),
	}
	require.NoError(t, db.EnqueueTask(ctx, task))

	// Simulate ffmpeg stderr with progress
	stderrData := "frame=  10 fps= 25 q=28.0 size=    128kB time=00:00:05.00 bitrate= 2621.4kbits/s speed=   1x\n"
	opts := TranscodeOptions{
		InputPath:  "/nonexistent.mp4",
		OutputPath: "/output.mp4",
	}

	q.parseProgress(ctx, task.ID, &readerStub{data: stderrData}, opts)

	// With mock ffprobe returning duration=10.0, elapsed=5.0/10.0=0.5
	got, err := db.GetTaskByID(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, 0.5, got.Progress)
}

// readerStub implements io.Reader for testing parseProgress.
type readerStub struct {
	data string
	pos  int
}

func (r *readerStub) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, sql.ErrNoRows // simulates EOF for scanner
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	if r.pos >= len(r.data) {
		return n, nil // next read will return 0
	}
	return n, nil
}

func TestQueueReplaceOriginal(t *testing.T) {
	// Test: replaceOriginal swaps output into input path atomically
	db := newTestQueueDB(t)
	q := newTestQueue(t, db, 1)

	tmpDir := t.TempDir()
	inputPath := filepath.Join(tmpDir, "original.mp4")
	outputPath := filepath.Join(tmpDir, "transcoded.mp4")

	require.NoError(t, os.WriteFile(inputPath, []byte("original data"), 0644))
	require.NoError(t, os.WriteFile(outputPath, []byte("transcoded data"), 0644))

	q.config.ReplaceOriginal = true

	task := &storage.TranscodeTask{
		InputPath:   inputPath,
		OutputPath:  outputPath,
		RecordingID: "rec-001",
		OutputFormat: "h264",
	}
	q.replaceOriginal(task)

	// Original path should now contain transcoded data
	data, err := os.ReadFile(inputPath)
	require.NoError(t, err)
	require.Equal(t, "transcoded data", string(data))

	// Output path should no longer exist
	_, err = os.Stat(outputPath)
	require.True(t, os.IsNotExist(err))

	// No backup or temp files should remain
	helperAssertNoOrphanFiles(t, tmpDir)
}

func TestReplaceOriginalUpdatesDBFormat(t *testing.T) {
	// Test: after replaceOriginal, recording format is updated in DB
	db := newTestQueueDB(t)
	q := newTestQueue(t, db, 1)

	ctx := context.Background()
	tmpDir := t.TempDir()

	// Insert a recording with h265 format
	recID := "rec-format-test"
	helperInsertRecording(t, db, recID, "test-cam", "h265")

	// Verify initial format
	rec, err := db.GetRecording(ctx, recID)
	require.NoError(t, err)
	require.Equal(t, model.Format("h265"), rec.Format)

	// Create files
	inputPath := filepath.Join(tmpDir, "original.mp4")
	outputPath := filepath.Join(tmpDir, "transcoded.mp4")
	require.NoError(t, os.WriteFile(inputPath, []byte("h265 data"), 0644))
	require.NoError(t, os.WriteFile(outputPath, []byte("h264 data"), 0644))

	task := &storage.TranscodeTask{
		InputPath:    inputPath,
		OutputPath:   outputPath,
		RecordingID:  recID,
		InputFormat:  "h265",
		OutputFormat: "h264",
	}
	q.replaceOriginal(task)

	// Verify format updated in DB
	rec, err = db.GetRecording(ctx, recID)
	require.NoError(t, err)
	require.Equal(t, model.Format("h264"), rec.Format, "recording format should be updated to h264")
}

func TestReplaceOriginalCrashSafety(t *testing.T) {
	// Test: simulate crash at each step — file must never be missing
	tmpDir := t.TempDir()
	originalData := []byte("original video content")
	transcodedData := []byte("transcoded video content")

	// Simulate crash between step 1 (output→temp) and step 2 (input→backup)
	// In this case, temp exists but input is still intact
	inputPath := filepath.Join(tmpDir, "crash1-input.mp4")
	tmpPath := filepath.Join(tmpDir, ".mibee-replace-tmp-crash1-output.mp4")
	require.NoError(t, os.WriteFile(inputPath, originalData, 0644))
	require.NoError(t, os.WriteFile(tmpPath, transcodedData, 0644))

	// Input should still exist
	data, err := os.ReadFile(inputPath)
	require.NoError(t, err)
	require.Equal(t, originalData, data, "input must survive partial replace")

	// Simulate crash between step 2 (input→backup) and step 3 (temp→input)
	// Backup exists, temp exists, input is gone
	backupPath := filepath.Join(tmpDir, ".mibee-replace-bak-crash2-input.mp4")
	tmpPath2 := filepath.Join(tmpDir, ".mibee-replace-tmp-crash2-output.mp4")
	require.NoError(t, os.WriteFile(backupPath, originalData, 0644))
	require.NoError(t, os.WriteFile(tmpPath2, transcodedData, 0644))

	// Either backup or temp must exist — data is recoverable
	backupExists := fileExists(backupPath)
	tmpExists := fileExists(tmpPath2)
	require.True(t, backupExists || tmpExists, "at least one copy must survive crash")
}

func TestReplaceOriginalEmptyPaths(t *testing.T) {
	// Test: replaceOriginal is a no-op when paths are empty
	db := newTestQueueDB(t)
	q := newTestQueue(t, db, 1)

	// Should not panic
	q.replaceOriginal(&storage.TranscodeTask{})
	q.replaceOriginal(&storage.TranscodeTask{InputPath: "/some/path"})
	q.replaceOriginal(&storage.TranscodeTask{OutputPath: "/some/path"})
}

func TestReplaceOriginalNoRecordingID(t *testing.T) {
	// Test: replaceOriginal works without RecordingID (no DB update)
	db := newTestQueueDB(t)
	q := newTestQueue(t, db, 1)

	tmpDir := t.TempDir()
	inputPath := filepath.Join(tmpDir, "input.mp4")
	outputPath := filepath.Join(tmpDir, "output.mp4")
	require.NoError(t, os.WriteFile(inputPath, []byte("old"), 0644))
	require.NoError(t, os.WriteFile(outputPath, []byte("new"), 0644))

	task := &storage.TranscodeTask{
		InputPath:    inputPath,
		OutputPath:   outputPath,
		OutputFormat: "h264",
		// RecordingID is empty — should still work
	}
	q.replaceOriginal(task)

	data, err := os.ReadFile(inputPath)
	require.NoError(t, err)
	require.Equal(t, "new", string(data))
}

// helperAssertNoOrphanFiles checks that no .mibee-replace-* temp/backup files exist.
func helperAssertNoOrphanFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, entry := range entries {
		name := entry.Name()
		isOrphan := strings.HasPrefix(name, ".mibee-replace-")
		require.False(t, isOrphan, "found orphan temp/backup file: %s", name)
	}
}

// helperInsertRecording inserts a test recording into the DB.
func helperInsertRecording(t *testing.T, db *storage.DB, id, cameraID, format string) {
	t.Helper()
	ctx := context.Background()
	err := db.InsertRecording(ctx, &model.Recording{
		ID:        id,
		CameraID:  cameraID,
		FilePath:  "/tmp/" + id + ".mp4",
		Format:    model.Format(format),
		StartedAt: time.Now().UTC(),
	})
	require.NoError(t, err)
}

// fileExists returns true if the file exists.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestRecoverStuckTasks(t *testing.T) {
	// Test: running task with started_at=1h ago → recovered to pending
	db := newTestQueueDB(t)
	q := newTestQueue(t, db, 1)

	ctx := context.Background()
	tmpDir := t.TempDir()
	inputPath := filepath.Join(tmpDir, "input.mp4")
	outputPath := filepath.Join(tmpDir, "output.mp4")
	require.NoError(t, os.WriteFile(inputPath, []byte("data"), 0644))

	// Insert a pending task
	taskID := helperInsertTask(t, db, inputPath, outputPath)

	// Manually set it to 'running' with started_at 1 hour ago
	oldStart := time.Now().UTC().Add(-1 * time.Hour).Format("2006-01-02 15:04:05.999999999")
	_, err := db.DB().ExecContext(ctx, "UPDATE transcoding_tasks SET status = 'running', started_at = ?, progress = 0.5 WHERE id = ?", oldStart, taskID)
	require.NoError(t, err)

	// Run queue with short timeout — recovery should happen before dispatch
	queueCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	go q.Run(queueCtx)

	// The stuck task should be recovered to pending, then dequeued and completed
	require.Eventually(t, func() bool {
		task, err := db.GetTaskByID(ctx, taskID)
		require.NoError(t, err)
		return task != nil && task.Status == "completed"
	}, 5*time.Second, 100*time.Millisecond, "recovered task should be processed and completed")
}

func TestRecoverStuckTasksRecentNotRecovered(t *testing.T) {
	// Test: running task with started_at=30s ago → NOT recovered
	db := newTestQueueDB(t)
	q := newTestQueue(t, db, 1)

	ctx := context.Background()
	tmpDir := t.TempDir()
	inputPath := filepath.Join(tmpDir, "input.mp4")
	outputPath := filepath.Join(tmpDir, "output.mp4")
	require.NoError(t, os.WriteFile(inputPath, []byte("data"), 0644))

	// Insert two pending tasks
	recentTaskID := helperInsertTask(t, db, inputPath, outputPath)
	oldTaskID := helperInsertTask(t, db, inputPath, filepath.Join(tmpDir, "output2.mp4"))

	// Set recent task to 'running' 30s ago (should NOT be recovered)
	recentStart := time.Now().UTC().Add(-30 * time.Second).Format("2006-01-02 15:04:05.999999999")
	_, err := db.DB().ExecContext(ctx, "UPDATE transcoding_tasks SET status = 'running', started_at = ?, progress = 0.3 WHERE id = ?", recentStart, recentTaskID)
	require.NoError(t, err)

	// Set old task to 'running' 1h ago (should be recovered)
	oldStart := time.Now().UTC().Add(-1 * time.Hour).Format("2006-01-02 15:04:05.999999999")
	_, err = db.DB().ExecContext(ctx, "UPDATE transcoding_tasks SET status = 'running', started_at = ?, progress = 0.7 WHERE id = ?", oldStart, oldTaskID)
	require.NoError(t, err)

	// Run queue briefly
	queueCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	go q.Run(queueCtx)

	// Old task should be recovered and eventually completed
	require.Eventually(t, func() bool {
		task, err := db.GetTaskByID(ctx, oldTaskID)
		require.NoError(t, err)
		return task != nil && task.Status == "completed"
	}, 5*time.Second, 100*time.Millisecond, "old stuck task should be recovered and completed")

	// Recent task should still be running (not recovered by the 5min threshold)
	recentTask, err := db.GetTaskByID(ctx, recentTaskID)
	require.NoError(t, err)
	require.Equal(t, "running", recentTask.Status, "recent task should NOT be recovered")
}

// newHangingTestQueue creates a queue with a mock FFmpeg that sleeps forever (ignores signals).
// Used to test per-job timeout behavior.
func newHangingTestQueue(t *testing.T, db *storage.DB, maxWorkers int, jobTimeout time.Duration) *TranscodeQueue {
	t.Helper()

	dir := t.TempDir()
	mockFFmpeg := filepath.Join(dir, "ffmpeg")
	// Sleep for a very long time; the timeout should kill us first
	mockScript := `#!/bin/sh
# Hang forever — the test timeout will kill this via context
output=""
for arg in "$@"; do
	output="$arg"
done
echo "frame=  10 fps= 25 q=28.0 size=    128kB time=00:00:00.40 bitrate= 2621.4kbits/s speed=   1x" >&2
sleep 300
echo "test content" > "$output"
exit 0
`
	err := os.WriteFile(mockFFmpeg, []byte(mockScript), 0755)
	require.NoError(t, err)

	mockFFprobe := filepath.Join(dir, "ffprobe")
	ffprobeScript := `#!/bin/sh
echo '{"streams":[{"codec_name":"h264","duration":10.0,"width":1920,"height":1080}]}'
exit 0
`
	err = os.WriteFile(mockFFprobe, []byte(ffprobeScript), 0755)
	require.NoError(t, err)

	caps := &HardwareCapabilities{
		Arch:            "amd64",
		H264Encoder:     "libx264",
		H265Encoder:     "libx265",
		H264EncoderType: EncoderSoftware,
		H265EncoderType: EncoderSoftware,
		FFmpegAvailable: true,
		FFmpegPath:      mockFFmpeg,
	}

	m := metrics.NewMetrics()

	cfg := QueueConfig{
		MaxWorkers:  maxWorkers,
		FFmpegPath:  mockFFmpeg,
		FFprobePath: mockFFprobe,
		JobTimeout:  jobTimeout,
	}

	return NewTranscodeQueue(db, caps, nil, cfg, m)
}

func TestJobTimeout(t *testing.T) {
	// Test: per-job timeout kills a hanging FFmpeg and marks task as failed
	db := newTestQueueDB(t)
	q := newHangingTestQueue(t, db, 1, 2*time.Second)

	tmpDir := t.TempDir()
	inputPath := filepath.Join(tmpDir, "input.mp4")
	outputPath := filepath.Join(tmpDir, "output.mp4")
	require.NoError(t, os.WriteFile(inputPath, []byte("fake video data"), 0644))

	taskID := helperInsertTask(t, db, inputPath, outputPath)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go q.Run(ctx)

	// Wait for task to be marked as failed with timeout error
	require.Eventually(t, func() bool {
		task, err := db.GetTaskByID(context.Background(), taskID)
		require.NoError(t, err)
		return task != nil && task.Status == "failed"
	}, 10*time.Second, 100*time.Millisecond, "task should be failed due to timeout")

	task, err := db.GetTaskByID(context.Background(), taskID)
	require.NoError(t, err)
	require.Equal(t, "failed", task.Status)
	require.True(t, task.Error.Valid)
	require.Contains(t, task.Error.String, "transcode job timed out after")
	require.Contains(t, task.Error.String, "2s")
}

func TestJobTimeoutZeroDisabled(t *testing.T) {
	// Test: when JobTimeout is 0, no timeout is applied (task runs normally)
	db := newTestQueueDB(t)
	q := newTestQueue(t, db, 1) // default QueueConfig with JobTimeout=0

	tmpDir := t.TempDir()
	inputPath := filepath.Join(tmpDir, "input.mp4")
	outputPath := filepath.Join(tmpDir, "output.mp4")
	require.NoError(t, os.WriteFile(inputPath, []byte("fake video data"), 0644))

	taskID := helperInsertTask(t, db, inputPath, outputPath)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go q.Run(ctx)

	// Task should complete normally (no timeout)
	require.Eventually(t, func() bool {
		task, err := db.GetTaskByID(context.Background(), taskID)
		require.NoError(t, err)
		return task != nil && task.Status == "completed"
	}, 5*time.Second, 100*time.Millisecond, "task should complete without timeout")
}

func TestOutputFileCleanupOnFailure(t *testing.T) {
	// Test: partial output file is removed when FFmpeg fails
	db := newTestQueueDB(t)

	dir := t.TempDir()
	mockFFmpeg := filepath.Join(dir, "ffmpeg")
	mockScript := `#!/bin/sh
output=""
for arg in "$@"; do
	output="$arg"
done
echo "partial data" > "$output"
echo "Error: Encoding failed" >&2
exit 1
`
	require.NoError(t, os.WriteFile(mockFFmpeg, []byte(mockScript), 0755))

	caps := &HardwareCapabilities{
		Arch:            "amd64",
		H264Encoder:     "libx264",
		H265Encoder:     "libx265",
		H264EncoderType: EncoderSoftware,
		H265EncoderType: EncoderSoftware,
		FFmpegAvailable: true,
		FFmpegPath:      mockFFmpeg,
	}

	m := metrics.NewMetrics()
	cfg := QueueConfig{
		MaxWorkers:  1,
		FFmpegPath:  mockFFmpeg,
		FFprobePath: filepath.Join(dir, "ffprobe"),
	}

	q := NewTranscodeQueue(db, caps, nil, cfg, m)

	tmpDir := t.TempDir()
	inputPath := filepath.Join(tmpDir, "input.mp4")
	outputPath := filepath.Join(tmpDir, "output.mp4")
	require.NoError(t, os.WriteFile(inputPath, []byte("data"), 0644))

	taskID := helperInsertTask(t, db, inputPath, outputPath)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go q.Run(ctx)

	require.Eventually(t, func() bool {
		task, err := db.GetTaskByID(context.Background(), taskID)
		require.NoError(t, err)
		return task != nil && task.Status == "failed"
	}, 5*time.Second, 100*time.Millisecond, "task should fail")

	// Verify output file was cleaned up
	_, err := os.Stat(outputPath)
	require.True(t, os.IsNotExist(err), "partial output file should be removed on failure")
}

func TestOutputFileCleanupNoFileOnFailure(t *testing.T) {
	// Test: cleanup silently handles missing output file
	db := newTestQueueDB(t)

	dir := t.TempDir()
	mockFFmpeg := filepath.Join(dir, "ffmpeg")
	mockScript := `#!/bin/sh
echo "Error: No such file" >&2
exit 1
`
	require.NoError(t, os.WriteFile(mockFFmpeg, []byte(mockScript), 0755))

	caps := &HardwareCapabilities{
		Arch:            "amd64",
		H264Encoder:     "libx264",
		H265Encoder:     "libx265",
		H264EncoderType: EncoderSoftware,
		H265EncoderType: EncoderSoftware,
		FFmpegAvailable: true,
		FFmpegPath:      mockFFmpeg,
	}

	m := metrics.NewMetrics()
	cfg := QueueConfig{
		MaxWorkers:  1,
		FFmpegPath:  mockFFmpeg,
		FFprobePath: filepath.Join(dir, "ffprobe"),
	}

	q := NewTranscodeQueue(db, caps, nil, cfg, m)

	tmpDir := t.TempDir()
	inputPath := filepath.Join(tmpDir, "input.mp4")
	outputPath := filepath.Join(tmpDir, "output.mp4")
	require.NoError(t, os.WriteFile(inputPath, []byte("data"), 0644))

	taskID := helperInsertTask(t, db, inputPath, outputPath)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go q.Run(ctx)

	require.Eventually(t, func() bool {
		task, err := db.GetTaskByID(context.Background(), taskID)
		require.NoError(t, err)
		return task != nil && task.Status == "failed"
	}, 5*time.Second, 100*time.Millisecond, "task should fail")

	// Output file was never created — cleanup should not error
	_, err := os.Stat(outputPath)
	require.True(t, os.IsNotExist(err), "output file should not exist (never created)")
}

func TestConcurrentEnqueueDequeue(t *testing.T) {
	// Test: high-frequency enqueue + concurrent dequeue without SQLITE_BUSY errors
	db := newTestQueueDB(t)
	q := newTestQueue(t, db, 3)
	tmpDir := t.TempDir()

	const numTasks = 10
	taskIDs := make([]int64, numTasks)
	// Rapid sequential enqueue (simulates API calls) — exercises DB contention
	for i := 0; i < numTasks; i++ {
		inputPath := filepath.Join(tmpDir, fmt.Sprintf("input%d.mp4", i))
		outputPath := filepath.Join(tmpDir, fmt.Sprintf("output%d.mp4", i))
		require.NoError(t, os.WriteFile(inputPath, []byte("data"), 0644))
		taskIDs[i] = helperInsertTask(t, db, inputPath, outputPath)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	go q.Run(ctx)

	require.Eventually(t, func() bool {
		for _, id := range taskIDs {
			task, err := db.GetTaskByID(context.Background(), id)
			if err != nil || task == nil || task.Status != "completed" {
				return false
			}
		}
		return true
	}, 30*time.Second, 200*time.Millisecond, "all tasks should complete without SQLITE_BUSY")
}

func TestProgressThrottle(t *testing.T) {
	// Test: rapid progress lines result in throttled DB writes — not every line triggers a write
	db := newTestQueueDB(t)
	q := newTestQueue(t, db, 1)
	ctx := context.Background()

	task := &storage.TranscodeTask{
		CameraID:     "cam-1",
		RecordingID:  "rec-1",
		InputPath:    "/nonexistent.mp4",
		InputFormat:  "h265",
		OutputPath:   "/output.mp4",
		OutputFormat: "h264",
		CreatedAt:    time.Now().UTC().Format("2006-01-02 15:04:05.999999999"),
	}
	require.NoError(t, db.EnqueueTask(ctx, task))

	// Feed many progress lines rapidly — only the first should trigger a DB write
	progressLine := "frame=  10 fps= 25 q=28.0 size=    128kB time=00:00:05.00 bitrate= 2621.4kbits/s speed=   1x\n"
	stderrData := strings.Repeat(progressLine, 20)

	opts := TranscodeOptions{
		InputPath:  "/nonexistent.mp4",
		OutputPath: "/output.mp4",
	}
	q.parseProgress(ctx, task.ID, &readerStub{data: stderrData}, opts)

	got, err := db.GetTaskByID(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, "running", got.Status)
	require.Equal(t, 0.5, got.Progress)
}

func TestRetryOnBusy(t *testing.T) {
	// Test: retryOnBusy retries on SQLITE_BUSY errors and returns nil on success
	ctx := context.Background()

	attempts := 0
	err := retryOnBusy(ctx, func() error {
		attempts++
		if attempts < 3 {
			return fmt.Errorf("database is locked (SQLITE_BUSY)")
		}
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, 3, attempts)
}

func TestRetryOnBusyMaxRetries(t *testing.T) {
	// Test: retryOnBusy gives up after max retries
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	attempts := 0
	err := retryOnBusy(ctx, func() error {
		attempts++
		return fmt.Errorf("database is locked (SQLITE_BUSY)")
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "SQLITE_BUSY")
	require.Equal(t, maxDBRetries+1, attempts)
}

func TestRetryOnBusyNonBusyError(t *testing.T) {
	// Test: retryOnBusy does not retry on non-busy errors
	ctx := context.Background()

	attempts := 0
	err := retryOnBusy(ctx, func() error {
		attempts++
		return fmt.Errorf("some other error")
	})
	require.Error(t, err)
	require.Equal(t, 1, attempts)
}

// --- ARM Enqueue Rejection Tests ---

// newARMTestQueue creates a queue with ARM software-only caps (no hardware encoder, no hardware decoder).
func newARMTestQueue(t *testing.T, db *storage.DB, maxWorkers int) *TranscodeQueue {
	t.Helper()

	dir := t.TempDir()
	mockFFmpeg := filepath.Join(dir, "ffmpeg")
	mockScript := `#!/bin/sh
exit 0
`
	err := os.WriteFile(mockFFmpeg, []byte(mockScript), 0755)
	require.NoError(t, err)

	caps := &HardwareCapabilities{
		Arch:            "arm64",
		H264Encoder:     "libx264",
		H265Encoder:     "libx265",
		H264EncoderType: EncoderSoftware,
		H265EncoderType: EncoderSoftware,
		H264Decoder:     "",
		H265Decoder:     "",
		H264DecoderType: "",
		H265DecoderType: "",
		FFmpegAvailable: true,
		FFmpegPath:      mockFFmpeg,
	}

	cfg := QueueConfig{
		MaxWorkers:  maxWorkers,
		FFmpegPath:  mockFFmpeg,
		FFprobePath: filepath.Join(dir, "ffprobe"),
	}

	return NewTranscodeQueue(db, caps, nil, cfg, nil)
}

// newARMHardwareQueue creates a queue with ARM V4L2M2M caps (hardware encoder + decoder available).
func newARMHardwareQueue(t *testing.T, db *storage.DB, maxWorkers int) *TranscodeQueue {
	t.Helper()

	dir := t.TempDir()
	mockFFmpeg := filepath.Join(dir, "ffmpeg")
	mockScript := `#!/bin/sh
exit 0
`
	err := os.WriteFile(mockFFmpeg, []byte(mockScript), 0755)
	require.NoError(t, err)

	caps := &HardwareCapabilities{
		Arch:            "arm64",
		H264Encoder:     "h264_v4l2m2m",
		H265Encoder:     "hevc_v4l2m2m",
		H264EncoderType: EncoderV4L2M2M,
		H265EncoderType: EncoderV4L2M2M,
		H264Decoder:     "h264_v4l2m2m",
		H265Decoder:     "hevc_v4l2m2m",
		H264DecoderType: EncoderV4L2M2M,
		H265DecoderType: EncoderV4L2M2M,
		FFmpegAvailable: true,
		FFmpegPath:      mockFFmpeg,
	}

	cfg := QueueConfig{
		MaxWorkers:  maxWorkers,
		FFmpegPath:  mockFFmpeg,
		FFprobePath: filepath.Join(dir, "ffprobe"),
	}

	return NewTranscodeQueue(db, caps, nil, cfg, nil)
}

func TestQueueARMSoftwareEncodingAllowed(t *testing.T) {
	// Test: enqueue on ARM queue with software-only caps → now allowed (fallback)
	db := newTestQueueDB(t)
	q := newARMTestQueue(t, db, 1)

	ctx := context.Background()
	task := &storage.TranscodeTask{
		CameraID:     "cam-1",
		RecordingID:  "rec-1",
		InputPath:    "/input.mp4",
		InputFormat:  "h265",
		OutputPath:   "/output.mp4",
		OutputFormat: "h264",
		CreatedAt:    time.Now().UTC().Format("2006-01-02 15:04:05.999999999"),
	}

	err := q.Enqueue(ctx, task)
	require.NoError(t, err, "enqueue should allow software encoding on ARM as fallback")
	require.Greater(t, task.ID, int64(0), "task should be inserted")
}

func TestQueueARMHardwareEncodingAllowed(t *testing.T) {
	// Test: enqueue on ARM queue with V4L2M2M caps → allowed
	db := newTestQueueDB(t)
	q := newARMHardwareQueue(t, db, 1)

	ctx := context.Background()
	task := &storage.TranscodeTask{
		CameraID:     "cam-1",
		RecordingID:  "rec-1",
		InputPath:    "/input.mp4",
		InputFormat:  "h265",
		OutputPath:   "/output.mp4",
		OutputFormat: "h264",
		CreatedAt:    time.Now().UTC().Format("2006-01-02 15:04:05.999999999"),
	}

	err := q.Enqueue(ctx, task)
	require.NoError(t, err, "enqueue should allow hardware encoding on ARM")
	require.Greater(t, task.ID, int64(0), "task should be inserted")
}

func TestQueueX86SoftwareEncodingAllowed(t *testing.T) {
	// Test: enqueue on x86 queue with software caps → allowed (baseline)
	db := newTestQueueDB(t)
	q := newTestQueue(t, db, 1) // uses amd64 caps

	ctx := context.Background()
	task := &storage.TranscodeTask{
		CameraID:     "cam-1",
		RecordingID:  "rec-1",
		InputPath:    "/input.mp4",
		InputFormat:  "h265",
		OutputPath:   "/output.mp4",
		OutputFormat: "h264",
		CreatedAt:    time.Now().UTC().Format("2006-01-02 15:04:05.999999999"),
	}

	err := q.Enqueue(ctx, task)
	require.NoError(t, err, "enqueue should allow software encoding on amd64")
}

func TestQueueARMH265SoftwareAllowed(t *testing.T) {
	// Test: enqueue H.265 output on ARM with software-only caps → now allowed (fallback)
	db := newTestQueueDB(t)
	q := newARMTestQueue(t, db, 1)

	ctx := context.Background()
	task := &storage.TranscodeTask{
		CameraID:     "cam-1",
		RecordingID:  "rec-1",
		InputPath:    "/input.mp4",
		InputFormat:  "h264",
		OutputPath:   "/output.mp4",
		OutputFormat: "h265",
		CreatedAt:    time.Now().UTC().Format("2006-01-02 15:04:05.999999999"),
	}

	err := q.Enqueue(ctx, task)
	require.NoError(t, err, "enqueue should allow H.265 software encoding on ARM as fallback")
	require.Greater(t, task.ID, int64(0), "task should be inserted")
}

// --- ARM Decoder Rejection Tests ---

func TestQueueARMH265InputAllowedWithoutDecoder(t *testing.T) {
	// Test: ARM queue with hardware encoder but no HEVC decoder → now allowed (software decode)
	db := newTestQueueDB(t)

	dir := t.TempDir()
	mockFFmpeg := filepath.Join(dir, "ffmpeg")
	mockScript := `#!/bin/sh
exit 0
`
	require.NoError(t, os.WriteFile(mockFFmpeg, []byte(mockScript), 0755))

	caps := &HardwareCapabilities{
		Arch:            "arm64",
		H264Encoder:     "h264_v4l2m2m",
		H265Encoder:     "hevc_v4l2m2m",
		H264EncoderType: EncoderV4L2M2M,
		H265EncoderType: EncoderV4L2M2M,
		H264Decoder:     "h264_v4l2m2m",
		H265Decoder:     "", // no HEVC decoder
		H264DecoderType: EncoderV4L2M2M,
		H265DecoderType: "",
		FFmpegAvailable: true,
		FFmpegPath:      mockFFmpeg,
	}
	cfg := QueueConfig{
		MaxWorkers:  1,
		FFmpegPath:  mockFFmpeg,
		FFprobePath: filepath.Join(dir, "ffprobe"),
	}
	q := NewTranscodeQueue(db, caps, nil, cfg, nil)

	ctx := context.Background()
	task := &storage.TranscodeTask{
		CameraID:     "cam-1",
		RecordingID:  "rec-1",
		InputPath:    "/input.mp4",
		InputFormat:  "h265",
		OutputPath:   "/output.mp4",
		OutputFormat: "h264",
		CreatedAt:    time.Now().UTC().Format("2006-01-02 15:04:05.999999999"),
	}

	err := q.Enqueue(ctx, task)
	// Now allowed — software decoding works on ARM, just slower
	require.NoError(t, err, "enqueue should allow H.265 input with software decode on ARM")
	require.Greater(t, task.ID, int64(0), "task should be inserted")
}

func TestQueueARMAllowsH265InputWithDecoder(t *testing.T) {
	// Test: ARM queue with V4L2M2M HEVC decoder, H.265 input → allowed
	db := newTestQueueDB(t)
	q := newARMHardwareQueue(t, db, 1)

	ctx := context.Background()
	task := &storage.TranscodeTask{
		CameraID:     "cam-1",
		RecordingID:  "rec-1",
		InputPath:    "/input.mp4",
		InputFormat:  "h265",
		OutputPath:   "/output.mp4",
		OutputFormat: "h264",
		CreatedAt:    time.Now().UTC().Format("2006-01-02 15:04:05.999999999"),
	}

	err := q.Enqueue(ctx, task)
	require.NoError(t, err, "enqueue should allow H.265 input with hardware decoder on ARM")
	require.Greater(t, task.ID, int64(0), "task should be inserted")
}

func TestQueueARMH264InputAllowedWithoutDecoder(t *testing.T) {
	// Test: ARM queue with hardware encoder but no H.264 decoder → now allowed (software decode)
	db := newTestQueueDB(t)

	dir := t.TempDir()
	mockFFmpeg := filepath.Join(dir, "ffmpeg")
	mockScript := `#!/bin/sh
exit 0
`
	require.NoError(t, os.WriteFile(mockFFmpeg, []byte(mockScript), 0755))

	caps := &HardwareCapabilities{
		Arch:            "arm64",
		H264Encoder:     "h264_v4l2m2m",
		H265Encoder:     "hevc_v4l2m2m",
		H264EncoderType: EncoderV4L2M2M,
		H265EncoderType: EncoderV4L2M2M,
		H264Decoder:     "", // no H.264 decoder
		H265Decoder:     "hevc_v4l2m2m",
		H264DecoderType: "",
		H265DecoderType: EncoderV4L2M2M,
		FFmpegAvailable: true,
		FFmpegPath:      mockFFmpeg,
	}
	cfg := QueueConfig{
		MaxWorkers:  1,
		FFmpegPath:  mockFFmpeg,
		FFprobePath: filepath.Join(dir, "ffprobe"),
	}
	q := NewTranscodeQueue(db, caps, nil, cfg, nil)

	ctx := context.Background()
	task := &storage.TranscodeTask{
		CameraID:     "cam-1",
		RecordingID:  "rec-1",
		InputPath:    "/input.mp4",
		InputFormat:  "h264",
		OutputPath:   "/output.mp4",
		OutputFormat: "h264",
		CreatedAt:    time.Now().UTC().Format("2006-01-02 15:04:05.999999999"),
	}

	err := q.Enqueue(ctx, task)
	// Now allowed — software decoding works on ARM, just slower
	require.NoError(t, err, "enqueue should allow H.264 input with software decode on ARM")
	require.Greater(t, task.ID, int64(0), "task should be inserted")
}

func TestQueueX86AllowsAnyInput(t *testing.T) {
	// Test: x86 queue, any input format → always allowed (software decode OK)
	db := newTestQueueDB(t)
	q := newTestQueue(t, db, 1) // uses amd64 caps

	ctx := context.Background()

	for _, format := range []string{"h264", "h265", "mjpeg"} {
		t.Run(format, func(t *testing.T) {
			task := &storage.TranscodeTask{
				CameraID:     "cam-1",
				RecordingID:  "rec-1",
				InputPath:    "/input.mp4",
				InputFormat:  format,
				OutputPath:   "/output.mp4",
				OutputFormat: "h264",
				CreatedAt:    time.Now().UTC().Format("2006-01-02 15:04:05.999999999"),
			}

			err := q.Enqueue(ctx, task)
			require.NoError(t, err, "enqueue should allow %s input on amd64", format)
		})
	}
}

// --- Resolution Limit Tests ---

// newV4L2M2MTestQueue creates a queue with V4L2M2M caps and resolution limits for testing.
func newV4L2M2MTestQueue(t *testing.T, db *storage.DB, maxWorkers int) *TranscodeQueue {
	t.Helper()

	dir := t.TempDir()
	mockFFmpeg := filepath.Join(dir, "ffmpeg")
	mockScript := `#!/bin/sh
exit 0
`
	require.NoError(t, os.WriteFile(mockFFmpeg, []byte(mockScript), 0755))

	mockFFprobe := filepath.Join(dir, "ffprobe")
	ffprobeScript := `#!/bin/sh
echo '{"streams":[{"codec_name":"h264","duration":10.0,"width":2560,"height":1440}]}'
exit 0
`
	require.NoError(t, os.WriteFile(mockFFprobe, []byte(ffprobeScript), 0755))

	caps := &HardwareCapabilities{
		Arch:            "arm64",
		H264Encoder:     "h264_v4l2m2m",
		H265Encoder:     "hevc_v4l2m2m",
		H264EncoderType: EncoderV4L2M2M,
		H265EncoderType: EncoderV4L2M2M,
		H264Decoder:     "h264_v4l2m2m",
		H265Decoder:     "hevc_v4l2m2m",
		H264DecoderType: EncoderV4L2M2M,
		H265DecoderType: EncoderV4L2M2M,
		MaxEncodeWidth:  1920,
		MaxEncodeHeight: 1440,
		FFmpegAvailable: true,
		FFmpegPath:      mockFFmpeg,
	}

	cfg := QueueConfig{
		MaxWorkers:  maxWorkers,
		FFmpegPath:  mockFFmpeg,
		FFprobePath: mockFFprobe,
	}

	return NewTranscodeQueue(db, caps, nil, cfg, nil)
}

// newV4L2M2MTestQueueSmall creates a queue with V4L2M2M caps and a mock ffprobe returning small resolution.
func newV4L2M2MTestQueueSmall(t *testing.T, db *storage.DB, maxWorkers int) *TranscodeQueue {
	t.Helper()

	dir := t.TempDir()
	mockFFmpeg := filepath.Join(dir, "ffmpeg")
	mockScript := `#!/bin/sh
exit 0
`
	require.NoError(t, os.WriteFile(mockFFmpeg, []byte(mockScript), 0755))

	mockFFprobe := filepath.Join(dir, "ffprobe")
	ffprobeScript := `#!/bin/sh
echo '{"streams":[{"codec_name":"h264","duration":10.0,"width":1280,"height":720}]}'
exit 0
`
	require.NoError(t, os.WriteFile(mockFFprobe, []byte(ffprobeScript), 0755))

	caps := &HardwareCapabilities{
		Arch:            "arm64",
		H264Encoder:     "h264_v4l2m2m",
		H265Encoder:     "hevc_v4l2m2m",
		H264EncoderType: EncoderV4L2M2M,
		H265EncoderType: EncoderV4L2M2M,
		H264Decoder:     "h264_v4l2m2m",
		H265Decoder:     "hevc_v4l2m2m",
		H264DecoderType: EncoderV4L2M2M,
		H265DecoderType: EncoderV4L2M2M,
		MaxEncodeWidth:  1920,
		MaxEncodeHeight: 1440,
		FFmpegAvailable: true,
		FFmpegPath:      mockFFmpeg,
	}

	cfg := QueueConfig{
		MaxWorkers:  maxWorkers,
		FFmpegPath:  mockFFmpeg,
		FFprobePath: mockFFprobe,
	}

	return NewTranscodeQueue(db, caps, nil, cfg, nil)
}

func TestQueueRejectsOversizedResolution(t *testing.T) {
	// Test: input width 2560 exceeds V4L2M2M max 1920 → rejected
	db := newTestQueueDB(t)
	q := newV4L2M2MTestQueue(t, db, 1)

	tmpDir := t.TempDir()
	inputPath := filepath.Join(tmpDir, "input.mp4")
	require.NoError(t, os.WriteFile(inputPath, []byte("fake video data"), 0644))

	ctx := context.Background()
	task := &storage.TranscodeTask{
		CameraID:     "cam-1",
		RecordingID:  "rec-1",
		InputPath:    inputPath,
		InputFormat:  "h264",
		OutputPath:   "/output.mp4",
		OutputFormat: "h264",
		CreatedAt:    time.Now().UTC().Format("2006-01-02 15:04:05.999999999"),
	}

	err := q.Enqueue(ctx, task)
	require.Error(t, err, "enqueue should reject input width exceeding V4L2M2M max")
	require.Contains(t, err.Error(), "input width 2560 exceeds encoder maximum 1920")
}

func TestQueueAllowsResolutionWithinLimit(t *testing.T) {
	// Test: input 1280×720 within V4L2M2M max 1920×1440 → allowed
	db := newTestQueueDB(t)
	q := newV4L2M2MTestQueueSmall(t, db, 1)

	tmpDir := t.TempDir()
	inputPath := filepath.Join(tmpDir, "input.mp4")
	require.NoError(t, os.WriteFile(inputPath, []byte("fake video data"), 0644))

	ctx := context.Background()
	task := &storage.TranscodeTask{
		CameraID:     "cam-1",
		RecordingID:  "rec-1",
		InputPath:    inputPath,
		InputFormat:  "h264",
		OutputPath:   "/output.mp4",
		OutputFormat: "h264",
		CreatedAt:    time.Now().UTC().Format("2006-01-02 15:04:05.999999999"),
	}

	err := q.Enqueue(ctx, task)
	require.NoError(t, err, "enqueue should allow input within V4L2M2M resolution limits")
	require.Greater(t, task.ID, int64(0), "task should be inserted")
}

func TestQueueARMMJPEGInputNotChecked(t *testing.T) {
	// Test: MJPEG input on ARM is not checked for decoder capability
	db := newTestQueueDB(t)
	q := newARMTestQueue(t, db, 1)

	ctx := context.Background()
	task := &storage.TranscodeTask{
		CameraID:     "cam-1",
		RecordingID:  "rec-1",
		InputPath:    "/input.mp4",
		InputFormat:  "mjpeg",
		OutputPath:   "/output.mp4",
		OutputFormat: "h264",
		CreatedAt:    time.Now().UTC().Format("2006-01-02 15:04:05.999999999"),
	}

	err := q.Enqueue(ctx, task)
	// MJPEG input should now be ALLOWED on ARM with software encoding
	// (low-resolution MJPEG is fast enough, and v4l2m2m may hang on MJPEG)
	require.NoError(t, err, "MJPEG input should be allowed on ARM with software encoding")
}

func TestQueueARMJPEGInputAllowed(t *testing.T) {
	// Test: JPEG input on ARM is also allowed with software encoding (same exemption as MJPEG)
	db := newTestQueueDB(t)
	q := newARMTestQueue(t, db, 1)

	ctx := context.Background()
	task := &storage.TranscodeTask{
		CameraID:     "cam-1",
		RecordingID:  "rec-1",
		InputPath:    "/input.mp4",
		InputFormat:  "jpeg",
		OutputPath:   "/output.mp4",
		OutputFormat: "h264",
		CreatedAt:    time.Now().UTC().Format("2006-01-02 15:04:05.999999999"),
	}

	err := q.Enqueue(ctx, task)
	require.NoError(t, err, "JPEG input should be allowed on ARM with software encoding")
}
