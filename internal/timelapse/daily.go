package timelapse

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// RecordingLister is the interface for listing recordings from the database.
type RecordingLister interface {
	ListRecordings(ctx context.Context, filter model.RecordingFilter) ([]model.Recording, error)
}

// DailyMergeManager handles daily merge operations for timelapse recordings.
// It wraps PeriodicMergeManager with a 24-hour interval for backward compatibility.
type DailyMergeManager struct {
	inner *PeriodicMergeManager
	loc   *time.Location
}

// NewDailyMergeManager creates a new DailyMergeManager wrapping a PeriodicMergeManager with 24h duration.
// If loc is nil, UTC is used.
func NewDailyMergeManager(store RecordingLister, updater MergeStatusUpdater, merger TimelapseMerger, fps int, dataDir string, loc *time.Location) *DailyMergeManager {
	if loc == nil {
		loc = time.UTC
	}
	return &DailyMergeManager{
		inner: NewPeriodicMergeManager(store, updater, merger, fps, dataDir, 24*time.Hour, loc),
		loc:   loc,
	}
}

// Run executes the daily merge pipeline for the given camera on the given date.
// date format: "2006-01-02" (interpreted in the configured timezone)
func (m *DailyMergeManager) Run(ctx context.Context, cameraID string, date string) error {
	// Parse date in the configured timezone so that "2024-01-03"
	// means local midnight, not UTC midnight.
	t, err := time.ParseInLocation("2006-01-02", date, m.loc)
	if err != nil {
		return fmt.Errorf("daily merge: invalid date %q: %w", date, err)
	}

	// Compute the 24-hour window using the configured timezone.
	startTime, endTime := parseMergeRange(t, 24*time.Hour, m.loc)

	// Query DB for merged timelapse segments in the date range.
	merged := true
	recordings, err := m.inner.store.ListRecordings(ctx, model.RecordingFilter{
		CameraID:  cameraID,
		Format:    model.FormatTimelapse,
		Merged:    &merged,
		StartTime: startTime,
		EndTime:   endTime,
	})
	if err != nil {
		return fmt.Errorf("daily merge: list recordings: %w", err)
	}

	// Filter to only include segments with merge_status='merged'.
	segments := filterMergedSegments(recordings)

	// Handle no segments.
	if len(segments) == 0 {
		slog.Warn(
			"daily merge: no segments found for date",
			"camera_id", cameraID,
			"date", date,
		)
		return nil
	}

	// Build output path with the old naming scheme.
	dailyPath := filepath.Join(m.inner.dataDir, cameraID, fmt.Sprintf("daily_%s.mp4", date))

	return m.inner.runMergePipeline(ctx, segments, dailyPath)
}
