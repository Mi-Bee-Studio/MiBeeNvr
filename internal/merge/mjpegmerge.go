package merge

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)

// MergeMJPEGSegments merges multiple MJPEG segment directories into a single
// merged segment directory by moving (not copying) JPEG files from each source
// segment into a new output directory. Source directories are deleted after
// all files have been moved.
func MergeMJPEGSegments(ctx context.Context, segments []*model.Recording, store *storage.Manager, cameraID string) (*model.Recording, error) {
	if len(segments) == 0 {
		return nil, fmt.Errorf("no segments to merge")
	}

	// Step 1: Create output directory via store.CreateSegment.
	tempPath, finalPath, err := store.CreateSegment(cameraID, string(model.FormatMJPEG))
	if err != nil {
		return nil, fmt.Errorf("create merged segment: %w", err)
	}

	// Track timing and counts from all source segments.
	var earliestStarted time.Time
	var latestEnded time.Time
	var totalDuration float64
	var totalFrameCount int

	for _, seg := range segments {
		if seg.StartedAt.Before(earliestStarted) || earliestStarted.IsZero() {
			earliestStarted = seg.StartedAt
		}
		if seg.EndedAt.After(latestEnded) {
			latestEnded = seg.EndedAt
		}
		totalDuration += seg.Duration
		totalFrameCount += seg.FrameCount
	}

	// Step 2: Move files from each source segment directory into the output directory.
	for _, seg := range segments {
		srcDir := seg.FilePath

		entries, err := os.ReadDir(srcDir)
		if err != nil {
			return nil, fmt.Errorf("read segment dir %q: %w", srcDir, err)
		}

		// Sort by name for deterministic ordering.
		sort.Slice(entries, func(i, j int) bool {
			return entries[i].Name() < entries[j].Name()
		})

		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}

			srcFile := filepath.Join(srcDir, entry.Name())
			dstFile := filepath.Join(tempPath, entry.Name())

			if err := os.Rename(srcFile, dstFile); err != nil {
				return nil, fmt.Errorf("move file %q to %q: %w", srcFile, dstFile, err)
			}
		}

		// Step 3: Delete the now-empty source directory.
		if err := os.Remove(srcDir); err != nil {
			// Non-fatal: log but don't fail the merge.
			// Source directory might not be empty if there were subdirectories.
		}
	}

	// Step 4: Calculate total size via filepath.Walk on the output directory.
	var totalSize int64
	filepath.Walk(tempPath, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			totalSize += info.Size()
		}
		return nil
	})

	// Step 5: Close the segment (atomic rename from temp to final).
	if err := store.CloseSegment(tempPath, finalPath); err != nil {
		return nil, fmt.Errorf("close merged segment: %w", err)
	}

	// Step 6: Build the merged recording metadata.
	merged := &model.Recording{
		ID:         fmt.Sprintf("%d", time.Now().UnixNano()),
		CameraID:   cameraID,
		FilePath:   finalPath,
		Format:     model.FormatMJPEG,
		StartedAt:  earliestStarted,
		EndedAt:    latestEnded,
		Duration:   totalDuration,
		FileSize:   totalSize,
		FrameCount: totalFrameCount,
	}

	return merged, nil
}
