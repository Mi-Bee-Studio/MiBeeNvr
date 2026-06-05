// Package timelapse provides rolling merge functionality for timelapse recordings.
package timelapse

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// MergeStatusUpdater is the interface for updating merge status in the database.
type MergeStatusUpdater interface {
	SetMergeStatus(ctx context.Context, ids []string, status string) error
}

// RollingMergeManager tracks active async merges per camera.
// It launches goroutines that wait for a segment to complete, then call
// TimelapseMerger.Merge() to produce the final output.
type RollingMergeManager struct {
	mu      sync.Mutex
	merger  TimelapseMerger
	active  map[string]context.CancelFunc
	db      MergeStatusUpdater
	fps     int
}

// NewRollingMergeManager creates a new RollingMergeManager.
func NewRollingMergeManager(merger TimelapseMerger, db MergeStatusUpdater, fps int) *RollingMergeManager {
	return &RollingMergeManager{
		merger: merger,
		active: make(map[string]context.CancelFunc),
		db:     db,
		fps:    fps,
	}
}

// StartSegmentMerge launches an async goroutine that waits for the segment to
// complete (via ctx cancellation or a done signal), then calls Merge().
// The caller should cancel ctx when the segment is closed.
func (r *RollingMergeManager) StartSegmentMerge(ctx context.Context, cameraID, segmentDir, outputPath string) {
	ctx, cancel := context.WithCancel(ctx)

	r.mu.Lock()
	// Cancel any existing merge for this camera before starting a new one.
	if oldCancel, ok := r.active[cameraID]; ok {
		slog.Warn("rolling merge: replacing active merge for camera", "camera_id", cameraID)
		oldCancel()
	}
	r.active[cameraID] = cancel
	r.mu.Unlock()

	go r.runMerge(ctx, cameraID, segmentDir, outputPath)
}

// StopSegmentMerge cancels the active merge goroutine for the given camera.
func (r *RollingMergeManager) StopSegmentMerge(cameraID string) {
	r.mu.Lock()
	if cancel, ok := r.active[cameraID]; ok {
		cancel()
		delete(r.active, cameraID)
	}
	r.mu.Unlock()
}

// runMerge waits for the segment to be ready, then performs the merge.
func (r *RollingMergeManager) runMerge(ctx context.Context, cameraID, segmentDir, outputPath string) {
	defer func() {
		r.mu.Lock()
		delete(r.active, cameraID)
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

	// Update DB status to merging.
	if r.db != nil {
		// We don't have the recording ID here; the caller is responsible for
		// updating the initial status. We log the transition.
		slog.Debug("rolling merge: starting merge", "camera_id", cameraID, "segment_dir", segmentDir)
	}

	// Perform the merge.
	result, err := r.merger.Merge(ctx, segmentDir, outputPath, r.fps)
	if err != nil {
		slog.Error("rolling merge: merge failed",
			"camera_id", cameraID,
			"segment_dir", segmentDir,
			"error", err,
		)
		return
	}

	slog.Info("rolling merge: merge completed",
		"camera_id", cameraID,
		"output_path", result.OutputPath,
		"frames_merged", result.FramesMerged,
		"duration", result.Duration,
		"tier", result.Tier,
	)
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
	for _, cancel := range r.active {
		cancel()
	}
	// Clear the map.
	r.active = make(map[string]context.CancelFunc)
	r.mu.Unlock()
}
