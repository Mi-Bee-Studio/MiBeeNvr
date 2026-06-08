// Package timelapse provides rolling merge functionality for timelapse recordings.
package timelapse

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"
)

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
	mu             sync.Mutex
	merger         TimelapseMerger
	active         map[string]*activeEntry
	db             MergeStatusUpdater
	fps            int
	nextID         uint64
	deleteOriginal bool
	progressMu     sync.Mutex
	progress       map[string]*progressEntry
}

func NewRollingMergeManager(merger TimelapseMerger, db MergeStatusUpdater, fps int, deleteOriginal bool) *RollingMergeManager {
	return &RollingMergeManager{
		merger:         merger,
		active:         make(map[string]*activeEntry),
		db:             db,
		fps:            fps,
		deleteOriginal: deleteOriginal,
		progress:       make(map[string]*progressEntry),
	}
}

// StartSegmentMerge launches an async goroutine that waits for the segment to
// complete (via ctx cancellation or a done signal), then calls Merge().
// The caller should cancel ctx when the segment is closed.
func (r *RollingMergeManager) StartSegmentMerge(ctx context.Context, cameraID, segmentDir, outputPath, recordingID string) {
	ctx, cancel := context.WithCancel(ctx)

	r.mu.Lock()
	// Cancel any existing merge for this camera before starting a new one.
	if old, ok := r.active[cameraID]; ok {
		slog.Warn("rolling merge: replacing active merge for camera", "camera_id", cameraID)
		old.cancel()
	}
	r.nextID++
	id := r.nextID
	r.active[cameraID] = &activeEntry{cancel: cancel, id: id}
	r.mu.Unlock()

	// Count total frames in segment dir for progress estimation.
	totalFrames := countJPGFrames(segmentDir)

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
		slog.Error("rolling merge: merge failed",
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

	slog.Info("rolling merge: merge completed",
		"camera_id", cameraID,
		"output_path", result.OutputPath,
		"frames_merged", result.FramesMerged,
		"duration", result.Duration,
		"tier", result.Tier,
	)

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
			slog.Warn("delete_original: failed to remove source frames",
				"camera_id", cameraID,
				"segment_dir", segmentDir,
				"error", err,
			)
		} else {
			slog.Info("delete_original: removed source frames",
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

// StopAll cancels all active merge goroutines.
func (r *RollingMergeManager) StopAll() {
	r.mu.Lock()
	for _, entry := range r.active {
		entry.cancel()
	}
	// Clear the map.
	r.active = make(map[string]*activeEntry)
	r.mu.Unlock()

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
	defer r.progressMu.Unlock()
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
}

// countJPGFrames counts the number of .jpg/.jpeg files in a directory.
func countJPGFrames(dir string) int {
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
		if strings.HasSuffix(name, ".jpg") || strings.HasSuffix(name, ".jpeg") {
			count++
		}
	}
	return count
}
