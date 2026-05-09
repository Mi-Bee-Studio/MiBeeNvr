package merge

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)

var logger = slog.Default().With("component", "merge-manager")

// MergeManager handles periodic merging of consecutive MP4 segments.
type MergeManager struct {
	db      *storage.DB
	store   *storage.Manager
	cfg     config.MergeConfig
	cameras func() []config.CameraConfig
}

// NewMergeManager creates a new MergeManager with the given dependencies.
func NewMergeManager(db *storage.DB, store *storage.Manager, cfg config.MergeConfig, cameras func() []config.CameraConfig) *MergeManager {
	return &MergeManager{
		db:      db,
		store:   store,
		cfg:     cfg,
		cameras: cameras,
	}
}

// Run starts the periodic merge loop. It blocks until ctx is cancelled.
func (m *MergeManager) Run(ctx context.Context) {
	interval, err := time.ParseDuration(m.cfg.CheckInterval)
	if err != nil || interval <= 0 {
		interval = time.Hour
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Run once immediately
	m.RunOnce(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.RunOnce(ctx)
		}
	}
}

// RunOnce performs a single merge pass across all enabled cameras.
// It enforces a batch limit on total segments processed per run.
func (m *MergeManager) RunOnce(ctx context.Context) error {
	minAge, err := time.ParseDuration(m.cfg.MinSegmentAge)
	if err != nil {
		minAge = 10 * time.Minute
	}

	cameras := m.cameras()
	var totalMerged int
	var totalSegments int
	var totalFreed int64
	var processedSegments int

	for _, cam := range cameras {
		if !cam.Enabled {
			continue
		}
		if ctx.Err() != nil {
			break
		}

		merged, segments, freed, mergeErr := m.processCamera(ctx, cam.ID, minAge, m.cfg.BatchLimit-processedSegments)
		if mergeErr != nil {
			logger.Error("merge pass error for camera", "camera_id", cam.ID, "error", mergeErr)
			continue
		}
		totalMerged += merged
		totalSegments += segments
		totalFreed += freed
		processedSegments += segments
		if processedSegments >= m.cfg.BatchLimit {
			logger.Info("batch limit reached, stopping merge pass", "limit", m.cfg.BatchLimit)
			break
		}
	}

	if totalMerged > 0 {
		logger.Info("merge pass complete",
			"merged_groups", totalMerged,
			"merged_segments", totalSegments,
			"freed_bytes", totalFreed,
		)
	}

	return nil
}

// processCamera handles all merge windows for a single camera.
// remainingLimit caps the number of segments that may be processed; 0 means unlimited.
func (m *MergeManager) processCamera(ctx context.Context, cameraID string, minAge time.Duration, remainingLimit int) (merged, segments int, freed int64, err error) {
	windows, err := m.db.ListCameraMergeWindows(ctx, cameraID, minAge)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("list merge windows: %w", err)
	}

	for _, win := range windows {
		if ctx.Err() != nil {
			break
		}
		if win.SegmentCount < m.cfg.MinSegmentsToMerge {
			continue
		}

		recs, err := m.db.ListMergeableSegments(ctx, cameraID, win.StartTime, win.EndTime)
		if err != nil {
			logger.Warn("failed to list mergeable segments", "camera_id", cameraID, "error", err)
			continue
		}
		if len(recs) < m.cfg.MinSegmentsToMerge {
			continue
		}

		// Group by format.
		byFormat := groupByFormat(recs)
		for format, formatRecs := range byFormat {
			if remainingLimit > 0 && len(formatRecs) > remainingLimit {
				formatRecs = formatRecs[:remainingLimit]
			}
			g, s, f := m.mergeFormatGroup(ctx, cameraID, format, formatRecs)
			merged += g
			segments += s
			freed += f
			if remainingLimit > 0 {
				remainingLimit -= s
			}
			if remainingLimit == 0 {
				break
			}
		}
	}

	return merged, segments, freed, nil
}

// mergeFormatGroup parses segments, groups by SPS/PPS, and merges eligible groups.
func (m *MergeManager) mergeFormatGroup(ctx context.Context, cameraID, format string, recs []*model.Recording) (merged, segments int, freed int64) {
	// Parse all segments.
	type parsedRec struct {
		rec    *model.Recording
		info   *SegmentInfo
		spsKey []byte // SPS bytes for grouping
		ppsKey []byte // PPS bytes for grouping
	}

	var parsed []parsedRec
	for _, rec := range recs {
		info, err := ParseSegment(rec.FilePath)
		if err != nil {
			logger.Warn("failed to parse segment, skipping", "file_path", rec.FilePath, "error", err)
			continue
		}
		if info.Codec != format {
			continue
		}
		parsed = append(parsed, parsedRec{
			rec:    rec,
			info:   info,
			spsKey: info.SPS,
			ppsKey: info.PPS,
		})
	}

	// Group by SPS/PPS bytes.
	type spsGroupKey struct {
		sps []byte
		pps []byte
	}
	groups := make(map[string][]parsedRec)
	for _, p := range parsed {
		key := spsGroupKey{sps: p.spsKey, pps: p.ppsKey}
		keyStr := string(key.sps) + "\x00" + string(key.pps) + "\x00" + string(p.info.VPS)
		groups[keyStr] = append(groups[keyStr], p)
	}

	for _, group := range groups {
		if len(group) < m.cfg.MinSegmentsToMerge {
			continue
		}

		// Estimate merged size from source file sizes.
		var estSize int64
		var segmentInfos []*SegmentInfo
		var recordings []*model.Recording
		for _, g := range group {
			estSize += g.rec.FileSize
			segmentInfos = append(segmentInfos, g.info)
			recordings = append(recordings, g.rec)
		}

		// Check disk space — need at least 1.1x estimated merged size free.
		total, used, err := m.store.GetDiskUsage()
		if err != nil {
			logger.Warn("failed to get disk usage", "error", err)
			continue
		}
		freeSpace := total - used
		required := estSize * 11 / 10 // 1.1x safety margin
		if freeSpace < required {
			logger.Warn("insufficient disk space for merge", "camera_id", cameraID, "needed", required, "free", freeSpace)
			continue
		}

		// Create output file via store.
		tempPath, finalPath, err := m.store.CreateSegment(cameraID, format)
		if err != nil {
			logger.Warn("failed to create merge output segment", "error", err)
			continue
		}

		if err := MergeMP4Segments(segmentInfos, tempPath); err != nil {
			logger.Error("failed to merge MP4 segments", "camera_id", cameraID, "error", err)
			os.Remove(tempPath)
			continue
		}

		// Verify merged file exists and has content.
		fi, err := os.Stat(tempPath)
		if err != nil || fi.Size() == 0 {
			logger.Error("merged file is empty or missing", "temp_path", tempPath)
			os.Remove(tempPath)
			continue
		}

		// Atomic rename.
		if err := m.store.CloseSegment(tempPath, finalPath); err != nil {
			logger.Error("failed to finalize merged segment", "error", err)
			os.Remove(tempPath)
			continue
		}

		// Calculate merged metadata.
		var totalDuration float64
		var totalFrames int
		for _, si := range segmentInfos {
			totalDuration += si.TotalDuration.Seconds()
			totalFrames += si.SampleCount
		}
		startTime := recordings[0].StartedAt
		endTime := recordings[len(recordings)-1].EndedAt

		// Insert new recording.
		mergedRec := &model.Recording{
			ID:         fmt.Sprintf("%d", time.Now().UnixNano()),
			CameraID:   cameraID,
			FilePath:   finalPath,
			Format:     model.Format(format),
			StartedAt:  startTime,
			EndedAt:    endTime,
			Duration:   totalDuration,
			FileSize:   fi.Size(),
			FrameCount: totalFrames,
			Pinned:     false,
		}
		if err := m.db.InsertRecording(ctx, mergedRec); err != nil {
			logger.Error("failed to insert merged recording", "error", err)
			// Keep the merged file, don't delete source segments.
			continue
		}

		// Delete old recordings from DB and files from disk.
		ids := make([]string, len(recordings))
		for i, r := range recordings {
			ids[i] = r.ID
		}
		_, err = m.db.DeleteRecordingsBatch(ctx, ids)
		if err != nil {
			logger.Warn("failed to batch delete old recordings", "error", err)
		}

		var oldSize int64
		for _, r := range recordings {
			oldSize += r.FileSize
			m.store.DeleteFile(r.FilePath)
		}

		logger.Info("merged segments",
			"camera_id", cameraID,
			"segments", len(recordings),
			"duration_s", totalDuration,
			"size_bytes", fi.Size(),
			"freed_bytes", oldSize,
		)

		merged++
		segments += len(recordings)
		freed += oldSize
	}

	return merged, segments, freed
}

// groupByFormat partitions recordings by their format string.
func groupByFormat(recs []*model.Recording) map[string][]*model.Recording {
	m := make(map[string][]*model.Recording)
	for _, r := range recs {
		f := string(r.Format)
		m[f] = append(m[f], r)
	}
	return m
}
