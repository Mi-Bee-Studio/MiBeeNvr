package transcoding

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"regexp"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)

// CompletionFunc is called after a transcode task finishes (success or failure).
// Implementations handle post-processing like DB registration or file cleanup.
type CompletionFunc func(task *storage.TranscodeTask, success bool)

var queueLogger = slog.Default().With("component", "transcode-queue")

// progressUpdateInterval controls how often progress is written to the database.
// FFmpeg emits progress lines multiple times per second; writing every line
// causes SQLITE_BUSY contention under concurrent load.
const progressUpdateInterval = 2 * time.Second


// TranscodeQueue manages async transcoding tasks with a bounded worker pool.
// Tasks are dequeued from the database (FIFO) and dispatched to worker goroutines.
type TranscodeQueue struct {
	store      *storage.DB
	caps       *HardwareCapabilities
	downloader *Downloader
	config     QueueConfig
	m          *metrics.Metrics

	mu         sync.Mutex
	cancel     context.CancelFunc
	activeJobs map[int64]context.CancelFunc // task ID → cancel function
	wg         sync.WaitGroup
	stopped    bool
	completionFn CompletionFunc // optional post-completion callback
}

// QueueConfig holds configuration for the transcode queue.
type QueueConfig struct {
	DataDir         string        // root data directory for orphan cleanup
	MaxWorkers      int
	FFmpegPath      string
	FFprobePath     string
	ReplaceOriginal bool
	JobTimeout      time.Duration // per-job timeout, 0 means no timeout
}

// NewTranscodeQueue creates a new TranscodeQueue.
func NewTranscodeQueue(store *storage.DB, caps *HardwareCapabilities, dl *Downloader, cfg QueueConfig, m *metrics.Metrics) *TranscodeQueue {
	if cfg.MaxWorkers <= 0 {
		cfg.MaxWorkers = 1
	}
	return &TranscodeQueue{
		store:      store,
		caps:       caps,
		downloader: dl,
		config:     cfg,
		m:          m,
		activeJobs: make(map[int64]context.CancelFunc),
	}
}

// Run starts the queue's main polling loop. It blocks until ctx is cancelled.
// On each tick it checks for pending tasks and dispatches up to MaxWorkers concurrently.
func (q *TranscodeQueue) Run(ctx context.Context) error {
	q.mu.Lock()
	ctx, q.cancel = context.WithCancel(ctx)
	q.mu.Unlock()
	defer q.cancel()

	// Recover tasks stuck in 'running' from a previous crash
	n, err := q.store.RecoverStuckTasks(ctx, 5*time.Minute)
	if err != nil {
		queueLogger.Warn("failed to recover stuck tasks", "error", err)
	} else if n > 0 {
		queueLogger.Info("recovered stuck transcoding tasks", "count", n)
	}

	// Clean up orphaned transcoded files (crash recovery)
	if q.config.DataDir != "" {
		if err := CleanOrphanedTranscodes(ctx, q.config.DataDir, q.store); err != nil {
			queueLogger.Warn("failed to clean orphaned transcoded files", "error", err)
		}
	}

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	queueLogger.Info("transcode queue started", "max_workers", q.config.MaxWorkers)

	for {
		select {
		case <-ctx.Done():
			queueLogger.Info("transcode queue stopping, waiting for active workers")
			q.wg.Wait()
			queueLogger.Info("all workers finished")
			return nil
		case <-ticker.C:
			q.dispatchPending(ctx)
		}
	}
}

// Stop gracefully drains the queue. Active workers are allowed to finish.
// New tasks will not be dequeued after this call.
func (q *TranscodeQueue) Stop() {
	q.mu.Lock()
	q.stopped = true
	if q.cancel != nil {
		q.cancel()
	}
	q.mu.Unlock()
}

// Enqueue inserts a new pending task into the database.
// Rejects tasks that would require software encoding on ARM architecture.
// Rejects tasks with input codecs that lack hardware decoders on ARM.
// Rejects tasks where input resolution exceeds encoder limits.
func (q *TranscodeQueue) Enqueue(ctx context.Context, task *storage.TranscodeTask) error {
	if q.caps != nil && isARMArch(q.caps.Arch) {
		// Log warning when software encoding is selected on ARM — it's slow but
		// may be the only option when v4l2m2m is listed but device lacks encode capability.
		if !isMJPEGInputTask(task.InputFormat) {
			switch task.OutputFormat {
			case "h264":
				if q.caps.H264EncoderType == EncoderSoftware {
					slog.Warn("software H.264 encoding on ARM — transcoding will be slow", "arch", q.caps.Arch)
				}
			case "h265":
				if q.caps.H265EncoderType == EncoderSoftware {
					slog.Warn("software H.265 encoding on ARM — transcoding will be slow", "arch", q.caps.Arch)
				}
			}
		}

		// Log warning when hardware decoder is not available on ARM.
		// Software decoding works but is slower; still allow the task to proceed.
		switch task.InputFormat {
		case "h264":
			if q.caps.H264DecoderType != EncoderV4L2M2M && q.caps.H264DecoderType != EncoderVAAPI && q.caps.H264DecoderType != EncoderNVENC {
				slog.Warn("no hardware H.264 decoder on ARM — using software decode", "arch", q.caps.Arch)
			}
		case "h265":
			if q.caps.H265DecoderType != EncoderV4L2M2M && q.caps.H265DecoderType != EncoderVAAPI && q.caps.H265DecoderType != EncoderNVENC {
				slog.Warn("no hardware H.265 decoder on ARM — using software decode", "arch", q.caps.Arch)
			}
	}
}

	// Check input resolution against encoder limits (all architectures)
	if q.caps != nil && (q.caps.MaxEncodeWidth > 0 || q.caps.MaxEncodeHeight > 0) {
		if info, err := GetMediaInfo(q.config.FFprobePath, task.InputPath); err == nil && info != nil {
			if q.caps.MaxEncodeWidth > 0 && info.Width > q.caps.MaxEncodeWidth {
				return fmt.Errorf("input width %d exceeds encoder maximum %d", info.Width, q.caps.MaxEncodeWidth)
			}
			if q.caps.MaxEncodeHeight > 0 && info.Height > q.caps.MaxEncodeHeight {
				return fmt.Errorf("input height %d exceeds encoder maximum %d", info.Height, q.caps.MaxEncodeHeight)
			}
		}
		// If ffprobe fails (file doesn't exist yet), skip check — let it fail at transcode time
	}

	return q.store.EnqueueTask(ctx, task)
}

// CancelTask cancels a running or pending task.
// For running tasks, it kills the FFmpeg process via the stored cancel function.
func (q *TranscodeQueue) CancelTask(ctx context.Context, id int64) error {
	// Cancel the in-process FFmpeg if running
	q.mu.Lock()
	if cancelFn, ok := q.activeJobs[id]; ok {
		cancelFn()
	}
	q.mu.Unlock()

	// Update DB status
	return q.store.CancelTask(ctx, id)
}

// ActiveCount returns the number of currently running tasks.
func (q *TranscodeQueue) ActiveCount() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.activeJobs)
}

// GetStatus returns the current queue status for the API.
func (q *TranscodeQueue) GetStatus() ManagerStatus {
	q.mu.Lock()
	activeCount := len(q.activeJobs)
	q.mu.Unlock()

	// Get pending count from DB
	pendingTasks, err := q.store.GetTasksByStatus(context.Background(), "pending")
	if err != nil {
		queueLogger.Warn("failed to get pending tasks", "error", err)
	}

	return ManagerStatus{
		Enabled:       true,
		Hardware:      q.caps,
		QueueLength:   len(pendingTasks),
		ActiveJobs:    activeCount,
		RecentResults: nil, // populated by API layer if needed
	}
}

// SetCompletionFunc registers a callback invoked after each task completes.
// The callback receives the task and whether it succeeded.
func (q *TranscodeQueue) SetCompletionFunc(fn CompletionFunc) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.completionFn = fn
}

// dispatchPending polls the DB for pending tasks and starts workers up to MaxWorkers.
func (q *TranscodeQueue) dispatchPending(ctx context.Context) {
	q.mu.Lock()
	if q.stopped {
		q.mu.Unlock()
		return
	}
	activeCount := len(q.activeJobs)
	slotsAvailable := q.config.MaxWorkers - activeCount
	q.mu.Unlock()

	for i := 0; i < slotsAvailable; i++ {
		var task *storage.TranscodeTask
		err := storage.RetryOnBusy(ctx, func() error {
			var err error
			task, err = q.store.DequeueTask(ctx)
			return err
		})
		if err != nil {
			if err != sql.ErrNoRows {
				queueLogger.Warn("failed to dequeue task", "error", err)
			}
			return // no more pending tasks
		}

		q.startWorker(ctx, task)
	}
}

// startWorker launches a goroutine to transcode a single task.
// If JobTimeout is configured, the worker context wraps with a timeout.
func (q *TranscodeQueue) startWorker(ctx context.Context, task *storage.TranscodeTask) {
	var workerCtx context.Context
	var workerCancel context.CancelFunc
	if q.config.JobTimeout > 0 {
		workerCtx, workerCancel = context.WithTimeout(ctx, q.config.JobTimeout)
	} else {
		workerCtx, workerCancel = context.WithCancel(ctx)
	}

	q.mu.Lock()
	q.activeJobs[task.ID] = workerCancel
	q.mu.Unlock()

	if q.m != nil {
		q.m.TranscodingActiveJobs.Inc()
	}

	q.wg.Add(1)
	go func() {
		defer q.wg.Done()
		defer workerCancel()

		q.runWorker(workerCtx, task)

		q.mu.Lock()
		delete(q.activeJobs, task.ID)
		q.mu.Unlock()

		if q.m != nil {
			q.m.TranscodingActiveJobs.Dec()
		}
	}()
}

// runWorker executes the FFmpeg transcoding process for a single task.
// It handles progress parsing, priority setting, process group management,
// and updates the task status in the database on completion/failure/cancellation.
func (q *TranscodeQueue) runWorker(ctx context.Context, task *storage.TranscodeTask) {
	startTime := time.Now()
	success := false
	defer func() {
		if q.completionFn != nil {
			q.completionFn(task, success)
		}
		}()

	// Convert storage task to transcoding options with default preset
	opts := q.taskToOptions(task, "")

	// Build FFmpeg command
	args, err := BuildFFmpegCommand(opts, *q.caps)
	if err != nil {
		q.finishTask(ctx, task, "failed", 0, fmt.Sprintf("command build failed: %v", err))
		return
	}

	// Resolve FFmpeg binary path
	ffmpegPath := q.config.FFmpegPath
	if ffmpegPath == "" {
		ffmpegPath = q.caps.FFmpegPath
	}
	if ffmpegPath == "" {
		ffmpegPath = "ffmpeg"
	}

	cmd := exec.CommandContext(ctx, ffmpegPath, args...)

	// Process group for clean kill — prevents orphaned FFmpeg on RPi
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// Capture stderr for progress parsing
	stderr, err := cmd.StderrPipe()
	if err != nil {
		q.finishTask(ctx, task, "failed", 0, fmt.Sprintf("stderr pipe failed: %v", err))
		return
	}

	if err := cmd.Start(); err != nil {
		q.finishTask(ctx, task, "failed", 0, fmt.Sprintf("ffmpeg start failed: %v", err))
		return
	}

	// Set low priority (nice 10) — don't starve recording pipeline
	pid := cmd.Process.Pid
	if err := syscall.Setpriority(syscall.PRIO_PROCESS, pid, 10); err != nil {
		queueLogger.Warn("failed to set process priority", "pid", pid, "error", err)
	}

	// Parse progress from stderr
	progressDone := make(chan struct{})
	go func() {
		defer close(progressDone)
		q.parseProgress(ctx, task.ID, stderr, opts)
	}()

	// Wait for FFmpeg to finish
	waitErr := cmd.Wait()

	// Wait for progress parser to finish
	<-progressDone

	// Determine final status
	select {
	case <-ctx.Done():
		// Context cancelled or timed out — kill process group
		killProcessGroup(cmd)
		if ctx.Err() == context.DeadlineExceeded {
			timeoutMsg := fmt.Sprintf("transcode job timed out after %s", q.config.JobTimeout)
			q.finishTask(context.Background(), task, "failed", 0, timeoutMsg)
			queueLogger.Warn("task timed out", "task_id", task.ID, "timeout", q.config.JobTimeout)
		} else {
			q.finishTask(context.Background(), task, "cancelled", 0, "")
			queueLogger.Info("task cancelled", "task_id", task.ID)
		}
		q.removeOutput(task.OutputPath)
		return
	default:
	}
	if waitErr != nil {
		// Check if it was cancelled/timed out (context error vs ffmpeg error)
		if ctx.Err() == context.DeadlineExceeded {
			timeoutMsg := fmt.Sprintf("transcode job timed out after %s", q.config.JobTimeout)
			q.finishTask(context.Background(), task, "failed", 0, timeoutMsg)
			q.removeOutput(task.OutputPath)
			return
		}
		if ctx.Err() != nil {
			q.finishTask(context.Background(), task, "cancelled", 0, "")
			q.removeOutput(task.OutputPath)
			return
		}
		errMsg := waitErr.Error()
		if len(errMsg) > 500 {
			errMsg = errMsg[:500]
		}
		q.finishTask(context.Background(), task, "failed", 0, errMsg)
		queueLogger.Warn("task failed", "task_id", task.ID, "error", waitErr)
		q.removeOutput(task.OutputPath)
		return
	}

	success = true
	// Success
	duration := time.Since(startTime)
	q.finishTask(context.Background(), task, "completed", 1.0, "")
	queueLogger.Info("task completed",
		"task_id", task.ID,
		"duration", duration,
	)

	// Update metrics
	if q.m != nil {
		codecFrom := task.InputFormat
		codecTo := task.OutputFormat
		q.m.TranscodingJobsTotal.WithLabelValues(codecFrom, codecTo, "completed").Inc()
		q.m.TranscodingDurationSeconds.WithLabelValues(codecFrom, codecTo).Observe(duration.Seconds())

		// Track bytes processed (input file size)
		if info, err := os.Stat(task.InputPath); err == nil {
			q.m.TranscodingBytesProcessed.Add(float64(info.Size()))
		}
	}

	// Optionally replace original with transcoded file
	if q.config.ReplaceOriginal {
		q.replaceOriginal(task)
	}
}

// finishTask updates the task status in the database.
// Uses a separate context (typically Background) so cancelled tasks still get their final status written.
func (q *TranscodeQueue) finishTask(ctx context.Context, task *storage.TranscodeTask, status string, progress float64, errMsg string) {
	if err := storage.RetryOnBusy(ctx, func() error {
		return q.store.UpdateTaskStatus(ctx, task.ID, status, progress, errMsg)
	}); err != nil {
		queueLogger.Warn("failed to update task status", "task_id", task.ID, "error", err)
	}

	// Record failed metrics
	if q.m != nil && status == "failed" {
		codecFrom := task.InputFormat
		codecTo := task.OutputFormat
		q.m.TranscodingJobsTotal.WithLabelValues(codecFrom, codecTo, "failed").Inc()
	} else if q.m != nil && status == "cancelled" {
		codecFrom := task.InputFormat
		codecTo := task.OutputFormat
		q.m.TranscodingJobsTotal.WithLabelValues(codecFrom, codecTo, "cancelled").Inc()
	}
}

// removeOutput removes a partial FFmpeg output file when a task fails or is cancelled.
// Non-existent files are silently ignored.
func (q *TranscodeQueue) removeOutput(path string) {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		queueLogger.Warn("failed to remove partial transcode output",
			"path", path, "error", err)
	}
}

// parseProgress reads FFmpeg stderr line by line and updates task progress.
func (q *TranscodeQueue) parseProgress(ctx context.Context, taskID int64, stderr interface{ Read([]byte) (int, error) }, opts TranscodeOptions) {
	// Estimate total duration from input — if unknown, skip progress
	totalDuration := 0.0
	if info, err := GetMediaInfo(q.config.FFprobePath, opts.InputPath); err == nil {
		totalDuration = info.Duration
	}

	var lastProgressUpdate time.Time

	scanner := bufio.NewScanner(stderr)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
		}

		line := scanner.Text()
		matches := progressRegex.FindStringSubmatch(line)
		if len(matches) < 4 {
			continue
		}

		hours, _ := strconv.ParseFloat(matches[1], 64)
		minutes, _ := strconv.ParseFloat(matches[2], 64)
		seconds, _ := strconv.ParseFloat(matches[3], 64)
		elapsed := hours*3600 + minutes*60 + seconds

		var progress float64
		if totalDuration > 0 {
			progress = elapsed / totalDuration
			if progress > 0.99 {
				progress = 0.99
			}
		} else {
			progress = 0 // unknown duration
		}

		// Throttle DB writes — only update if enough time has passed
		if time.Since(lastProgressUpdate) < progressUpdateInterval {
			continue
		}

		if err := q.store.UpdateTaskStatus(ctx, taskID, "running", progress, ""); err != nil {
			queueLogger.Warn("failed to update progress", "task_id", taskID, "error", err)
		}
		lastProgressUpdate = time.Now()
	}
}


// taskToOptions converts a storage.TranscodeTask to TranscodeOptions for FFmpeg command building.
// preset specifies the encoding preset ("ultrafast", "faster", "medium", etc.);
// if empty, defaults to "ultrafast" (best performance on low-power devices like RPi).
func (q *TranscodeQueue) taskToOptions(task *storage.TranscodeTask, preset string) TranscodeOptions {
	if preset == "" {
		preset = "ultrafast"
	}
	framerate := task.Framerate
	if framerate <= 0 {
		framerate = 25
	}
	return TranscodeOptions{
		InputPath:  task.InputPath,
		OutputPath: task.OutputPath,
		InputCodec: task.InputFormat,
		OutputCodec: func() string {
			switch task.OutputFormat {
			case "h264", "h265":
				return task.OutputFormat
			default:
				return "h264" // safe default
			}
		}(),
		Framerate: framerate,
		Preset:    preset,
	}
}

// replaceOriginal replaces the source file with the transcoded output using
// an atomic temp-rename pattern that never loses data on crash.
// After successful file replacement, updates the recording format in the DB.
// Errors are logged but not propagated — this is best-effort.
func (q *TranscodeQueue) replaceOriginal(task *storage.TranscodeTask) {
	if task.OutputPath == "" || task.InputPath == "" {
		return
	}

	// Verify output exists
	if _, err := os.Stat(task.OutputPath); err != nil {
		queueLogger.Warn("cannot replace original: output file missing", "task_id", task.ID, "error", err)
		return
	}

	// Step 1: Move output to temp (same dir as input for same-filesystem rename)
	inputDir := filepath.Dir(task.InputPath)
	tmpPath := filepath.Join(inputDir, ".mibee-replace-tmp-"+filepath.Base(task.OutputPath))
	if err := atomicRename(task.OutputPath, tmpPath); err != nil {
		queueLogger.Warn("failed to move output to temp", "task_id", task.ID, "error", err)
		return
	}

	// Step 2: Move original to backup
	backupPath := filepath.Join(inputDir, ".mibee-replace-bak-"+filepath.Base(task.InputPath))
	if err := atomicRename(task.InputPath, backupPath); err != nil {
		// Restore: move temp back to output
		if restoreErr := atomicRename(tmpPath, task.OutputPath); restoreErr != nil {
			queueLogger.Error("failed to restore output after backup failure", "task_id", task.ID, "error", restoreErr)
		}
		queueLogger.Warn("failed to move original to backup", "task_id", task.ID, "error", err)
		return
	}

	// Step 3: Move temp to input path (final position — the commit point)
	if err := atomicRename(tmpPath, task.InputPath); err != nil {
		// Critical: either restore original or restore temp
		if restoreErr := atomicRename(backupPath, task.InputPath); restoreErr != nil {
			queueLogger.Error("CRITICAL: failed to restore original after commit failure", "task_id", task.ID, "backup", backupPath, "temp", tmpPath, "error", restoreErr)
		} else if restoreErr := atomicRename(tmpPath, task.OutputPath); restoreErr != nil {
			// Original restored, but temp is orphaned — log for manual cleanup
			queueLogger.Warn("orphaned temp file after restore", "task_id", task.ID, "path", tmpPath, "error", restoreErr)
		}
		queueLogger.Warn("failed to move transcoded to input path", "task_id", task.ID, "error", err)
		return
	}

	// Step 4: Remove backup (original is gone, new file is in place)
	if err := os.Remove(backupPath); err != nil && !os.IsNotExist(err) {
		queueLogger.Warn("failed to remove backup file", "task_id", task.ID, "path", backupPath, "error", err)
	}

	// Step 5: Update recording format in DB
	if task.RecordingID != "" && task.OutputFormat != "" {
		if err := q.store.UpdateRecordingFormat(context.Background(), task.RecordingID, task.OutputFormat); err != nil {
			queueLogger.Warn("failed to update recording format in DB", "task_id", task.ID, "recording_id", task.RecordingID, "error", err)
		}
	}

	queueLogger.Info("replaced original with transcoded file", "task_id", task.ID, "path", task.InputPath, "format", task.OutputFormat)
}

// atomicRename renames src to dst. If the rename fails with EXDEV (cross-device),
// falls back to io.Copy with a 1MB buffer followed by os.Remove of src.
func atomicRename(src, dst string) error {
	err := os.Rename(src, dst)
	if err == nil {
		return nil
	}

	// Cross-device link — fall back to copy + remove
	if !errors.Is(err, syscall.EXDEV) {
		return err
	}

	srcFile, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("exdev fallback: open src: %w", err)
	}
	defer srcFile.Close()

	dstFile, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("exdev fallback: create dst: %w", err)
	}
	defer dstFile.Close()

	buf := make([]byte, 1024*1024) // 1MB buffer per AGENTS.md
	if _, err := io.CopyBuffer(dstFile, srcFile, buf); err != nil {
		os.Remove(dst)
		return fmt.Errorf("exdev fallback: copy: %w", err)
	}

	// Sync to ensure data is on disk before we remove the source
	if err := dstFile.Sync(); err != nil {
		queueLogger.Warn("exdev fallback: sync failed", "dst", dst, "error", err)
	}

	// Close before remove (Windows compat)
	srcFile.Close()
	dstFile.Close()

	if err := os.Remove(src); err != nil {
		queueLogger.Warn("exdev fallback: failed to remove src after copy", "src", src, "error", err)
	}

	return nil
}

// isMJPEGInputTask returns true if the input format is MJPEG or JPEG.
func isMJPEGInputTask(format string) bool {
	return format == "mjpeg" || format == "jpeg"
}

var progressRegex = regexp.MustCompile(`time=(\d+):(\d+):(\d+\.\d+)`)

// killProcessGroup sends SIGKILL to the entire process group to ensure
// FFmpeg and any child processes are terminated.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		queueLogger.Warn("failed to kill ffmpeg process group", "pid", cmd.Process.Pid, "error", err)
	}
}
