// Package timelapse provides rolling merge functionality for timelapse recordings.
package timelapse

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"
)

// defaultProgressCleanupDelay is how long completed/failed progress entries are kept
// before being removed from the map, allowing the UI to read the final state.
const defaultProgressCleanupDelay = 5 * time.Minute

// MergeProgressInfo represents the current progress of a merge operation.
type MergeProgressInfo struct {
	CameraID     string  `json:"camera_id"`
	Progress     int     `json:"progress"`
	Status       string  `json:"status"`
	OutputPath   string  `json:"output_path,omitempty"`
	FramesMerged int     `json:"frames_merged,omitempty"`
	Duration     float64 `json:"duration,omitempty"`
	Tier         string  `json:"tier,omitempty"`
	Error        string  `json:"error,omitempty"`
}

type MergeStatusUpdater interface {
	SetMergeStatus(ctx context.Context, ids []string, status string) error
	SetMergeResult(ctx context.Context, id string, mergePath, mergeTier string) error
	SetMergeError(ctx context.Context, ids []string, mergeError string) error
	UpdateMergeProgress(ctx context.Context, id string, progress int) error
	UpdateMergeProgressBatch(ctx context.Context, ids []string, progress int) error
}

// RollingMergeManager tracks active async merges per camera.
// It launches goroutines that wait for a segment to complete, then call
// TimelapseMerger.Merge() to produce the final output.
type activeEntry struct {
	cancel context.CancelFunc
	id     uint64
}

type progressEntry struct {
	info     MergeProgressInfo
	complete chan struct{}
}

type RollingMergeManager struct {
	mu                   sync.Mutex
	merger               TimelapseMerger
	active               map[string]*activeEntry
	db                   MergeStatusUpdater
	fps                  int
	nextID               uint64
	deleteOriginal       bool
	progressMu           sync.Mutex
	progress             map[string]*progressEntry
	progressCleanupDelay time.Duration
	// stopped is set under r.mu by StopAll. Once set, StartSegmentMerge refuses
	// to launch new merges — this is what makes the wg.Add/wg.Wait pair safe
	// from the sync.WaitGroup "Add with positive delta must happen before Wait
	// when counter is zero" rule. Without it, a StartSegmentMerge that races
	// ahead of StopAll's Wait would call wg.Add(1) after Wait has drained the
	// counter to zero (data race + potential "WaitGroup is reused before
	// previous Wait has returned" panic). The flag is read AND the Add is done
	// atomically under r.mu in StartSegmentMerge, and set under r.mu before the
	// Wait in StopAll, so StopAll's clear-then-Wait cannot observe a zero
	// counter while a racing Start is mid-Add.
	stopped bool
	// stopMu serializes concurrent StopAll calls. sync.WaitGroup forbids Add
	// after its counter has gone to zero on a Wait, so two overlapping StopAll
	// calls (one Wait drains wg to zero, a concurrent StartSegmentMerge does
	// Add(1), the second Wait races) panic with "WaitGroup is reused before
	// previous Wait has returned". This is a real production hazard when a
	// shutdown (StopAll) races with an in-flight merge start, not just a test
	// artifact. Holding stopMu across the whole cancel-clear-wait makes StopAll
	// reentrant-safe.
	stopMu sync.Mutex
	// wg tracks every runMerge goroutine so StopAll can wait for them to fully
	// exit before returning. Without this, runMerge goroutines could briefly
	// outlive StopAll and touch the merger/db after the caller (e.g. App.Stop)
	// has begun tearing those resources down (#163). Matches the
	// merge.RollingMergeCoordinator wg pattern.
	wg sync.WaitGroup
}

func NewRollingMergeManager(merger TimelapseMerger, db MergeStatusUpdater, fps int, deleteOriginal bool) *RollingMergeManager {
	return &RollingMergeManager{
		merger:               merger,
		active:               make(map[string]*activeEntry),
		db:                   db,
		fps:                  fps,
		deleteOriginal:       deleteOriginal,
		progress:             make(map[string]*progressEntry),
		progressCleanupDelay: defaultProgressCleanupDelay,
	}
}

// StartSegmentMerge launches an async goroutine that waits for the segment to
// complete (via ctx cancellation or a done signal), then calls Merge().
// The caller should cancel ctx when the segment is closed.
func (r *RollingMergeManager) StartSegmentMerge(ctx context.Context, cameraID, segmentDir, outputPath, recordingID string) {
	ctx, cancel := context.WithCancel(ctx)

	r.mu.Lock()
	// Refuse to start a new merge after StopAll has begun tearing down. The
	// stopped flag + the wg.Add below are under the same r.mu hold as StopAll's
	// set-stopped+clear, so once StopAll has set stopped and moved on to Wait,
	// no racing Start can sneak in a wg.Add(1) after the counter reaches zero
	// (which would violate sync.WaitGroup's "no Add after Wait-at-zero" rule
	// and trigger a data race / panic). This is the root cause of the
	// TestRollingMergeManager_ConcurrentStopAllAndStart flake on main CI.
	if r.stopped {
		r.mu.Unlock()
		cancel()
		slog.Debug("rolling merge: refusing to start new merge after StopAll", "camera_id", cameraID)
		return
	}
	// Cancel any existing merge for this camera before starting a new one.
	if old, ok := r.active[cameraID]; ok {
		slog.Warn("rolling merge: replacing active merge for camera", "camera_id", cameraID)
		old.cancel()
	}
	r.nextID++
	id := r.nextID
	r.active[cameraID] = &activeEntry{cancel: cancel, id: id}
	// Track the goroutine before launching it so StopAll's Wait can't race
	// with a concurrent Add (sync.WaitGroup forbids Add-after-Wait-at-zero).
	// Add stays under r.mu so StopAll's cancel+clear block observes it.
	r.wg.Add(1)
	r.mu.Unlock()

	// Count total frames in segment dir for progress estimation.
	totalFrames := countFrames(segmentDir)

	r.setProgress(cameraID, MergeProgressInfo{
		CameraID: cameraID,
		Progress: 0,
		Status:   "merging",
	})
	// Set progress to 0 and status to 'pending' in DB.
	if r.db != nil && recordingID != "" {
		if dbErr := r.db.UpdateMergeProgress(ctx, recordingID, 0); dbErr != nil {
			slog.Warn("rolling merge: failed to set initial progress in DB",
				"recording_id", recordingID, "error", dbErr)
		}
	}

	go r.runMerge(ctx, id, cameraID, segmentDir, outputPath, recordingID, totalFrames)
}

func (r *RollingMergeManager) StopSegmentMerge(cameraID string) {
	r.mu.Lock()
	if entry, ok := r.active[cameraID]; ok {
		entry.cancel()
		delete(r.active, cameraID)
	}
	r.mu.Unlock()
}

func (r *RollingMergeManager) runMerge(ctx context.Context, ownID uint64, cameraID, segmentDir, outputPath, recordingID string, totalFrames int) {
	defer r.wg.Done() // paired with r.wg.Add in StartSegmentMerge
	defer func() {
		r.mu.Lock()
		// Only delete if this goroutine's entry is still the active one.
		// A replacement StartSegmentMerge may have stored a different entry.
		if entry, ok := r.active[cameraID]; ok && entry.id == ownID {
			delete(r.active, cameraID)
		}
		r.mu.Unlock()
	}()

	// Wait for segment completion signal or cancellation.
	select {
	case <-ctx.Done():
		slog.Debug("rolling merge: cancelled before merge started", "camera_id", cameraID)
		return
	default:
	}

	// Small delay to ensure the segment file is fully written and closed.
	select {
	case <-time.After(100 * time.Millisecond):
	case <-ctx.Done():
		slog.Debug("rolling merge: cancelled during pre-merge delay", "camera_id", cameraID)
		return
	}

	// Update progress to indicate merge is in progress.
	r.setProgress(cameraID, MergeProgressInfo{
		CameraID: cameraID,
		Progress: 50,
		Status:   "merging",
	})
	// Update DB progress to indicate merge is in progress.
	if r.db != nil && recordingID != "" {
		if dbErr := r.db.UpdateMergeProgress(ctx, recordingID, 50); dbErr != nil {
			slog.Warn("rolling merge: failed to update merge progress in DB",
				"recording_id", recordingID, "error", dbErr)
		}
	}

	// Perform the merge.
	result, err := r.merger.Merge(ctx, segmentDir, outputPath, r.fps)
	if err != nil {
		slog.Error(
			"rolling merge: merge failed",
			"camera_id", cameraID,
			"segment_dir", segmentDir,
			"error", err,
		)
		// Update DB with failed status.
		if r.db != nil && recordingID != "" {
			if dbErr := r.db.SetMergeError(ctx, []string{recordingID}, err.Error()); dbErr != nil {
				slog.Warn("rolling merge: failed to set merge error in DB",
					"recording_id", recordingID, "error", dbErr)
			}
		}
		// Set failed progress.
		r.setProgress(cameraID, MergeProgressInfo{
			CameraID: cameraID,
			Progress: 0,
			Status:   "failed",
			Error:    err.Error(),
		})
		return
	}

	slog.Info(
		"rolling merge: merge completed",
		"camera_id", cameraID,
		"output_path", result.OutputPath,
		"frames_merged", result.FramesMerged,
		"duration", result.Duration,
		"tier", result.Tier,
	)

	// Post-merge verification: confirm the output MP4 was actually written and
	// is non-empty. Some mergers report success (via cmd exit code or muxer.close)
	// even when the output is absent or corrupt. This prevents delete_original
	// from destroying frames when the MP4 is unusable.
	if info, err := os.Stat(outputPath); err != nil || info.Size() == 0 {
		mergeErr := fmt.Errorf("post-merge verification failed: output file missing or empty (path=%s)", outputPath)
		slog.Error(
			"rolling merge: merge reported success but output file is missing/empty",
			"camera_id", cameraID,
			"output_path", outputPath,
			"stat_error", err,
		)
		if r.db != nil && recordingID != "" {
			if dbErr := r.db.SetMergeError(ctx, []string{recordingID}, mergeErr.Error()); dbErr != nil {
				slog.Warn("rolling merge: failed to set merge error in DB",
					"recording_id", recordingID, "error", dbErr)
			}
		}
		r.setProgress(cameraID, MergeProgressInfo{
			CameraID: cameraID,
			Progress: 0,
			Status:   "failed",
			Error:    mergeErr.Error(),
		})
		return
	}

	// Update DB with successful merge result.
	if r.db != nil && recordingID != "" {
		if dbErr := r.db.SetMergeResult(ctx, recordingID, outputPath, string(result.Tier)); dbErr != nil {
			slog.Warn("rolling merge: failed to set merge result in DB",
				"recording_id", recordingID, "error", dbErr)
		}
	}

	// Delete original source frames if configured.
	if r.deleteOriginal && result.FramesMerged > 0 {
		if err := os.RemoveAll(segmentDir); err != nil {
			slog.Warn(
				"delete_original: failed to remove source frames",
				"camera_id", cameraID,
				"segment_dir", segmentDir,
				"error", err,
			)
		} else {
			slog.Info(
				"delete_original: removed source frames",
				"camera_id", cameraID,
				"path", segmentDir,
			)
		}
	}

	// Set completed progress.
	r.setProgress(cameraID, MergeProgressInfo{
		CameraID:     cameraID,
		Progress:     100,
		Status:       "completed",
		OutputPath:   result.OutputPath,
		FramesMerged: result.FramesMerged,
		Duration:     result.Duration,
		Tier:         string(result.Tier),
	})
}

// ActiveCount returns the number of currently active merge goroutines.
func (r *RollingMergeManager) ActiveCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.active)
}

// IsActive returns true if there is an active merge for the given camera.
func (r *RollingMergeManager) IsActive(cameraID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.active[cameraID]
	return ok
}

// StopAll cancels all active merge goroutines and waits for them to fully exit.
//
// Waiting is required to honor the App.Service contract ("Stop must release all
// goroutines"): in-flight runMerge goroutines touch r.merger and r.db, which
// the caller may begin tearing down once StopAll returns (#163). Without the
// wait, those goroutines could briefly outlive the resources they reference.
//
// The cancel+clear happens under r.mu, but the Wait is OUTSIDE the lock —
// runMerge's cleanup defer also acquires r.mu, so waiting under the lock would
// deadlock. A concurrent StartSegmentMerge may add a fresh entry after the
// clear; that goroutine is tracked by wg too, so Wait still returns promptly
// (mergers honor ctx cancel) and the new entry is left in r.active for the
// next lifecycle — matching the existing "final state may have active entries"
// semantics documented in TestRollingMergeManager_ConcurrentStopAllAndStart.
//
// r.stopMu serializes overlapping StopAll calls so that a second StopAll
// cannot call r.wg.Wait() while the wg counter is between zero (drained by a
// first Wait) and a fresh Add(1) from a racing StartSegmentMerge — which would
// panic with "WaitGroup is reused before previous Wait has returned".
func (r *RollingMergeManager) StopAll() {
	r.stopMu.Lock()
	defer r.stopMu.Unlock()

	r.mu.Lock()
	// Set stopped BEFORE clearing + waiting. StartSegmentMerge checks this
	// flag under the same r.mu, so once we release the lock here no racing
	// Start can perform a wg.Add(1) after our Wait has drained the counter.
	r.stopped = true
	for _, entry := range r.active {
		entry.cancel()
	}
	// Clear the map.
	r.active = make(map[string]*activeEntry)
	r.mu.Unlock()

	// Wait for the cancelled runMerge goroutines to fully exit before clearing
	// progress. Do not acquire r.mu here (see comment above).
	r.wg.Wait()

	// Also clear progress.
	r.progressMu.Lock()
	r.progress = make(map[string]*progressEntry)
	r.progressMu.Unlock()
}

// GetProgress returns the current progress for a camera merge.
// Returns the progress info and true if a merge is or was tracked for this camera.
func (r *RollingMergeManager) GetProgress(cameraID string) (MergeProgressInfo, bool) {
	r.progressMu.Lock()
	defer r.progressMu.Unlock()
	entry, ok := r.progress[cameraID]
	if !ok {
		return MergeProgressInfo{}, false
	}
	return entry.info, true
}

// setProgress sets the progress for a camera merge.
func (r *RollingMergeManager) setProgress(cameraID string, info MergeProgressInfo) {
	r.progressMu.Lock()
	entry, ok := r.progress[cameraID]
	if !ok {
		entry = &progressEntry{
			complete: make(chan struct{}, 1),
		}
		r.progress[cameraID] = entry
	}
	entry.info = info
	// Signal completion if the merge is done.
	if info.Status == "completed" || info.Status == "failed" {
		select {
		case entry.complete <- struct{}{}:
		default:
		}
	}
	r.progressMu.Unlock()

	// Schedule cleanup after a delay so the UI can read the final state.
	// This prevents indefinite accumulation of progress entries (memory leak).
	if info.Status == "completed" || info.Status == "failed" {
		status := info.Status
		time.AfterFunc(r.progressCleanupDelay, func() {
			r.progressMu.Lock()
			// Only delete if the entry still exists with the same terminal status.
			// This prevents deleting a new merge that reuses the same cameraID.
			if cur, ok := r.progress[cameraID]; ok && cur == entry && cur.info.Status == status {
				delete(r.progress, cameraID)
			}
			r.progressMu.Unlock()
		})
	}
}

// countFrames counts frame files (.jpg/.jpeg/.h264/.h265) in a directory.
func countFrames(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	count := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := strings.ToLower(e.Name())
		if strings.HasSuffix(name, ".jpg") || strings.HasSuffix(name, ".jpeg") ||
			strings.HasSuffix(name, ".h264") || strings.HasSuffix(name, ".h265") {
			count++
		}
	}
	return count
}
