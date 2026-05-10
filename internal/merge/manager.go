package merge

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)

var logger = slog.Default().With("component", "merge-manager")

// MergeStatus holds the current status of the merge manager.
type MergeStatus struct {
	LastRunTime    time.Time `json:"last_run_time"`
	SegmentsMerged int       `json:"segments_merged"`
	FilesCreated   int       `json:"files_created"`
	ErrorCount     int       `json:"error_count"`
}

// MergeManager handles periodic merging of consecutive MP4 segments.
type MergeManager struct {
	mu             sync.RWMutex
	status         MergeStatus
	db             *storage.DB
	store          *storage.Manager
	getGlobalCfg   func() config.MergeConfig
	getCameraCfg   func(cameraID string) *config.MergeConfig
	cameras        func() []config.CameraConfig
}

// NewMergeManager creates a new MergeManager with the given dependencies.
// getGlobalCfg is called on each RunOnce to support config hot-reload.
// getCameraCfg returns per-camera merge config override (nil = use global).
func NewMergeManager(
	db *storage.DB,
	store *storage.Manager,
	getGlobalCfg func() config.MergeConfig,
	getCameraCfg func(cameraID string) *config.MergeConfig,
	cameras func() []config.CameraConfig,
) *MergeManager {
	return &MergeManager{
		db:           db,
		store:        store,
		getGlobalCfg: getGlobalCfg,
		getCameraCfg: getCameraCfg,
		cameras:      cameras,
	}
}

// Status returns a snapshot of the current merge status.
func (m *MergeManager) Status() MergeStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.status
}

// PendingCounts returns per-camera pending merge segment counts.
func (m *MergeManager) PendingCounts(ctx context.Context) map[string]int {
	cfg := m.getGlobalCfg()
	minAge, err := time.ParseDuration(cfg.MinSegmentAge)
	if err != nil {
		minAge = 10 * time.Minute
	}

	cameras := m.cameras()
	counts := make(map[string]int, len(cameras))
	for _, cam := range cameras {
		if !cam.Enabled {
			continue
		}
		effectiveCfg := config.ResolveMergeConfig(cfg, m.getCameraCfg(cam.ID))
		if !effectiveCfg.Enabled {
			continue
		}
		windows, err := m.db.ListCameraMergeWindows(ctx, cam.ID, minAge)
		if err != nil {
			continue
		}
		for _, w := range windows {
			counts[cam.ID] += w.SegmentCount
		}
	}
	return counts
}

func (m *MergeManager) Run(ctx context.Context) {
	cfg := m.getGlobalCfg()
	interval, err := time.ParseDuration(cfg.CheckInterval)
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
// Config is resolved fresh on each call for hot-reload support.
func (m *MergeManager) RunOnce(ctx context.Context) error {
	cfg := m.getGlobalCfg()

	minAge, err := time.ParseDuration(cfg.MinSegmentAge)
	if err != nil {
		minAge = 10 * time.Minute
	}

	cameras := m.cameras()
	var totalMerged int
	var totalSegments int
	var totalFreed int64
	var totalErrors int
	var processedSegments int

	for _, cam := range cameras {
		if !cam.Enabled {
			continue
		}
		if ctx.Err() != nil {
			break
		}

		// Resolve per-camera config via hot-reload callbacks.
		effectiveCfg := config.ResolveMergeConfig(cfg, m.getCameraCfg(cam.ID))
		if !effectiveCfg.Enabled {
			continue
		}

		merged, segments, freed, mergeErr := m.processCamera(ctx, cam.ID, minAge, effectiveCfg)
		if mergeErr != nil {
			logger.Error("merge pass error for camera", "camera_id", cam.ID, "error", mergeErr)
			totalErrors++
			continue
		}
		totalMerged += merged
		totalSegments += segments
		totalFreed += freed
		processedSegments += segments
		if processedSegments >= effectiveCfg.BatchLimit {
			logger.Info("batch limit reached, stopping merge pass", "limit", effectiveCfg.BatchLimit)
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

	// Update status under lock.
	m.mu.Lock()
	m.status.LastRunTime = time.Now()
	m.status.SegmentsMerged = totalSegments
	m.status.FilesCreated = totalMerged
	m.status.ErrorCount = totalErrors
	m.mu.Unlock()

	return nil
}
// processCamera handles all merge windows for a single camera.
// cfg is the effective merge config for this camera (resolved from global + per-camera override).
func (m *MergeManager) processCamera(ctx context.Context, cameraID string, minAge time.Duration, cfg config.MergeConfig) (merged, segments int, freed int64, err error) {
	remainingLimit := cfg.BatchLimit
	windows, err := m.db.ListCameraMergeWindows(ctx, cameraID, minAge)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("list merge windows: %w", err)
	}

	for _, win := range windows {
		if ctx.Err() != nil {
			break
		}
		if win.SegmentCount < cfg.MinSegmentsToMerge {
			continue
		}

		recs, err := m.db.ListMergeableSegments(ctx, cameraID, win.StartTime, win.EndTime)
		if err != nil {
			logger.Warn("failed to list mergeable segments", "camera_id", cameraID, "error", err)
			continue
		}
		if len(recs) < cfg.MinSegmentsToMerge {
			continue
		}

		// Group by format.
		byFormat := groupByFormat(recs)
		for format, formatRecs := range byFormat {
			if remainingLimit > 0 && len(formatRecs) > remainingLimit {
				formatRecs = formatRecs[:remainingLimit]
			}
			g, s, f := m.mergeFormatGroup(ctx, cameraID, format, formatRecs, cfg)
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
func (m *MergeManager) mergeFormatGroup(ctx context.Context, cameraID, format string, recs []*model.Recording, cfg config.MergeConfig) (merged, segments int, freed int64) {
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
		if len(group) < cfg.MinSegmentsToMerge {
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
			Merged:     false,
		}
		if err := m.db.InsertRecording(ctx, mergedRec); err != nil {
			logger.Error("failed to insert merged recording", "error", err)
			// Keep the merged file, don't delete source segments.
			continue
		}

		// Mark the new recording as merged
		if err := m.db.SetMerged(ctx, mergedRec.ID, true); err != nil {
			logger.Warn("failed to mark recording as merged", "recording_id", mergedRec.ID, "error", err)
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
