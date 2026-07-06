package merge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"math"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
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

// mergeLock is a per-camera mutex for serializing concurrent merges.
type mergeLock struct {
	mu sync.Mutex
}

// MergeManager handles periodic merging of consecutive MP4 segments.
type MergeManager struct {
	mu           sync.RWMutex
	status       MergeStatus
	db           *storage.DB
	store        *storage.Manager
	getGlobalCfg func() config.MergeConfig
	getCameraCfg func(cameraID string) *config.MergeConfig
	cameras      func() []config.CameraConfig
	mergeLocks   sync.Map // map[string]*mergeLock — per-camera merge mutex
	metrics      *metrics.Metrics
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
	m *metrics.Metrics,
) *MergeManager {
	return &MergeManager{
		db:           db,
		store:        store,
		getGlobalCfg: getGlobalCfg,
		getCameraCfg: getCameraCfg,
		cameras:      cameras,
		metrics:      m,
	}
}

// acquireMergeLock attempts to acquire a non-blocking per-camera merge lock.
// Returns a release function and true if the lock was acquired.
// Returns nil, false if another merge is already in progress for this camera.
func (m *MergeManager) acquireMergeLock(cameraID string) (release func(), ok bool) {
	lock := &mergeLock{}
	actual, _ := m.mergeLocks.LoadOrStore(cameraID, lock)
	ml := actual.(*mergeLock)

	if !ml.mu.TryLock() {
		return nil, false
	}

	return func() {
		ml.mu.Unlock()
		m.mergeLocks.Delete(cameraID)
	}, true
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
		logger.Info(
			"merge pass complete",
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

// MergeCamera performs a single merge pass for the given camera.
// It resolves the effective merge config (global + per-camera override) and delegates to processCamera.
// Returns an error if merge is already in progress for this camera or if the merge pass fails.
func (m *MergeManager) MergeCamera(ctx context.Context, cameraID string) error {
	cfg := m.getGlobalCfg()

	minAge, err := time.ParseDuration(cfg.MinSegmentAge)
	if err != nil {
		minAge = 10 * time.Minute
	}

	effectiveCfg := config.ResolveMergeConfig(cfg, m.getCameraCfg(cameraID))
	if !effectiveCfg.Enabled {
		return nil
	}

	_, _, _, mergeErr := m.processCamera(ctx, cameraID, minAge, effectiveCfg)
	if mergeErr != nil {
		logger.Warn("merge pass error for camera", "camera_id", cameraID, "error", mergeErr)
		return mergeErr
	}

	return nil
}

// processCamera handles all merge windows for a single camera.
// cfg is the effective merge config for this camera (resolved from global + per-camera override).
// It acquires a per-camera merge lock to prevent concurrent merges for the same camera.
// It also updates pending merge segment metrics for this camera.
func (m *MergeManager) processCamera(ctx context.Context, cameraID string, minAge time.Duration, cfg config.MergeConfig) (merged, segments int, freed int64, err error) {
	release, ok := m.acquireMergeLock(cameraID)
	if !ok {
		return 0, 0, 0, fmt.Errorf("merge already in progress for camera %s", cameraID)
	}
	defer release()
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

	// Mark singleton pending segments as merged — they're hour-boundary orphans
	// that will never be merged because their window has only 1 segment.
	var singletons []*model.Recording
	if err := storage.RetryOnBusy(ctx, func() (err error) {
		singletons, err = m.db.ListSingletonPendingRecordings(ctx, cameraID, minAge)
		return
	}); err != nil {
		logger.Warn("failed to list singleton pending recordings", "camera_id", cameraID, "error", err)
	} else if len(singletons) > 0 {
		ids := make([]string, len(singletons))
		for i, r := range singletons {
			ids[i] = r.ID
		}
		if err := storage.RetryOnBusy(ctx, func() error {
			return m.db.SetMergeStatus(ctx, ids, model.MergeStatusMerged)
		}); err != nil {
			logger.Warn("failed to mark singletons as merged", "camera_id", cameraID, "error", err)
		} else {
			logger.Info("marked singleton segments as merged", "camera_id", cameraID, "count", len(singletons))
		}
	}

	return merged, segments, freed, nil
}

// mergeFormatGroup parses segments, groups by SPS/PPS, and merges eligible groups.
// For MJPEG format, it skips ParseSegment and calls MergeMJPEGSegments directly.
func (m *MergeManager) mergeFormatGroup(ctx context.Context, cameraID, format string, recs []*model.Recording, cfg config.MergeConfig) (merged, segments int, freed int64) {
	// Timelapse recordings are JPEG directories with no mergeable output — skip entirely.
	if format == string(model.FormatTimelapse) {
		logger.Debug("skipping timelapse recordings (not mergeable)", "camera_id", cameraID, "count", len(recs))
		return 0, 0, 0
	}

	// MJPEG segments are directories containing JPEG files — ParseSegment only handles MP4.
	if format == string(model.FormatMJPEG) {
		return m.mergeMJPEGGroup(ctx, cameraID, recs, cfg)
	}

	// AVI segments are files containing MJPEG+G.711 data — ParseSegment only handles MP4.
	if format == string(model.FormatAVI) {
		return m.mergeAVIGroup(ctx, cameraID, recs, cfg)
	}

	// Parse all segments.
	type parsedRec struct {
		rec    *model.Recording
		info   *SegmentInfo
		spsKey []byte // SPS bytes for grouping
		ppsKey []byte // PPS bytes for grouping
	}

	var parsed []parsedRec
	var parseFailedIDs []string
	for _, rec := range recs {
		info, err := ParseSegment(rec.FilePath)
		if err != nil {
			logger.Warn("failed to parse segment, marking as failed", "recording_id", rec.ID, "file_path", rec.FilePath, "error", err)
			parseFailedIDs = append(parseFailedIDs, rec.ID)
			continue
		}
		if info.Codec != format {
			continue
		}

		// Validate audio config consistency.
		if info.HasAudio && len(info.AudioConfig) == 0 && info.AudioCodec != "g711" && info.AudioCodec != "opus" {
			logger.Warn("audio config mismatch: hasAudio=true but audioConfig is empty, disabling audio",
				"recording_id", rec.ID, "file_path", rec.FilePath, "audio_codec", info.AudioCodec)
			info.HasAudio = false
		}

		parsed = append(parsed, parsedRec{
			rec:    rec,
			info:   info,
			spsKey: info.SPS,
			ppsKey: info.PPS,
		})
	}

	// Mark parse-failed recordings permanently.
	if len(parseFailedIDs) > 0 {
		if err := storage.RetryOnBusy(ctx, func() error {
			return m.db.SetMergeStatus(ctx, parseFailedIDs, model.MergeStatusFailed)
		}); err != nil {
			logger.Warn("failed to mark parse-failed segments", "error", err)
		} else {
			logger.Info("marked parse-failed segments as merge_status=failed", "count", len(parseFailedIDs))
		}
	}

	// Group by SPS/PPS bytes using SHA-256 hash to avoid null-byte collisions.
	groups := make(map[string][]parsedRec)
	for _, p := range parsed {
		h := sha256.New()
		h.Write(p.spsKey)
		h.Write(p.ppsKey)
		h.Write(p.info.VPS)
		keyStr := hex.EncodeToString(h.Sum(nil))
		groups[keyStr] = append(groups[keyStr], p)
	}

	var smallGroupIDs []string
	for _, group := range groups {
		if len(group) < cfg.MinSegmentsToMerge {
			for _, g := range group {
				smallGroupIDs = append(smallGroupIDs, g.rec.ID)
			}
			continue
		}

		mergeStart := time.Now()

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
			m.metrics.RecordMergeFailure("disk_space")
			continue
		}
		freeSpace := total - used
		required := estSize * 11 / 10 // 1.1x safety margin
		if freeSpace < required {
			logger.Warn("insufficient disk space for merge", "camera_id", cameraID, "needed", required, "free", freeSpace)
			m.metrics.RecordMergeFailure("disk_space")
			continue
		}

		// Create output file via store.
		tempPath, finalPath, err := m.store.CreateSegment(cameraID, format)
		if err != nil {
			logger.Warn("failed to create merge output segment", "error", err)
			m.metrics.RecordMergeFailure("io_error")
			continue
		}

		if err := MergeMP4Segments(ctx, segmentInfos, tempPath); err != nil {
			logger.Error("failed to merge MP4 segments", "camera_id", cameraID, "error", err)
			os.Remove(tempPath)
			m.metrics.RecordMergeFailure("merge_error")
			continue
		}

		// Verify merged file exists and has content.
		fi, err := os.Stat(tempPath)
		if err != nil || fi.Size() == 0 {
			logger.Error("merged file is empty or missing", "temp_path", tempPath)
			os.Remove(tempPath)
			m.metrics.RecordMergeFailure("empty_output")
			continue
		}

		// Atomic rename.
		if err := m.store.CloseSegment(tempPath, finalPath); err != nil {
			logger.Error("failed to finalize merged segment", "error", err)
			os.Remove(tempPath)
			m.metrics.RecordMergeFailure("io_error")
			continue
		}

		// Calculate merged metadata.
		var totalDuration float64
		var totalFrames int
		for _, si := range segmentInfos {
			totalDuration += si.TotalDuration.Seconds()
			totalFrames += si.SampleCount
		}
		totalDuration = math.Round(totalDuration*1000) / 1000
		recStartTime := recordings[0].StartedAt
		recEndTime := recordings[len(recordings)-1].EndedAt

		// Insert new recording.
		mergedRec := &model.Recording{
			ID:         strconv.FormatInt(time.Now().UnixNano(), 10),
			CameraID:   cameraID,
			FilePath:   finalPath,
			Format:     model.Format(format),
			StartedAt:  recStartTime,
			EndedAt:    recEndTime,
			Duration:   totalDuration,
			FileSize:   fi.Size(),
			FrameCount: totalFrames,
			Merged:     true,
		}

		// Atomic: insert merged recording + delete old recordings in single transaction
		ids := make([]string, len(recordings))
		for i, r := range recordings {
			ids[i] = r.ID
		}
		if err := storage.RetryOnBusy(ctx, func() error {
			return m.db.MergeAndReplaceRecordings(ctx, mergedRec, ids)
		}); err != nil {
			logger.Error("failed to merge and replace recordings", "camera_id", cameraID, "error", err)
			// Delete the merged file on DB failure (it's useless without a DB record)
			os.Remove(finalPath)
			m.metrics.RecordMergeFailure("db_error")
			continue
		}

		// Record successful merge metrics.
		m.metrics.RecordMergeSuccess(time.Since(mergeStart), fi.Size())

		// Only delete old segment files AFTER successful DB transaction
		var oldSize int64
		for _, r := range recordings {
			oldSize += r.FileSize
			m.store.DeleteFile(r.FilePath)
		}

		logger.Info(
			"merged segments",
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

	// Mark undersized SPS/PPS groups as permanently failed.
	if len(smallGroupIDs) > 0 {
		if err := storage.RetryOnBusy(ctx, func() error {
			return m.db.SetMergeStatus(ctx, smallGroupIDs, model.MergeStatusFailed)
		}); err != nil {
			logger.Warn("failed to mark undersized group segments", "error", err)
		} else {
			logger.Info("marked undersized SPS/PPS group segments as merge_status=failed", "count", len(smallGroupIDs))
		}
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

// mergeMJPEGGroup merges MJPEG segment directories into a single merged directory.
// MJPEG segments cannot be parsed by ParseSegment (MP4-only), so this path
// collects all MJPEG recordings and delegates to MergeMJPEGSegments.
func (m *MergeManager) mergeMJPEGGroup(ctx context.Context, cameraID string, recs []*model.Recording, cfg config.MergeConfig) (merged, segments int, freed int64) {
	if len(recs) < cfg.MinSegmentsToMerge {
		return 0, 0, 0
	}

	mergeStart := time.Now()

	// Estimate merged size from source recordings.
	var estSize int64
	for _, r := range recs {
		estSize += r.FileSize
	}

	// Check disk space.
	total, used, err := m.store.GetDiskUsage()
	if err != nil {
		logger.Warn("failed to get disk usage", "error", err)
		m.metrics.RecordMergeFailure("disk_space")
		return 0, 0, 0
	}
	freeSpace := total - used
	required := estSize * 11 / 10
	if freeSpace < required {
		logger.Warn("insufficient disk space for MJPEG merge", "camera_id", cameraID, "needed", required, "free", freeSpace)
		m.metrics.RecordMergeFailure("disk_space")
		return 0, 0, 0
	}

	// Delegate to MergeMJPEGSegments — handles file moves and output dir creation.
	mergedRec, sourceDirs, err := MergeMJPEGSegments(ctx, recs, m.store, cameraID)
	if err != nil {
		logger.Error("failed to merge MJPEG segments", "camera_id", cameraID, "error", err)
		m.metrics.RecordMergeFailure("merge_error")
		return 0, 0, 0
	}

	// Mark as merged.
	mergedRec.Merged = true

	// Atomic: insert merged recording + delete old recordings in single transaction.
	ids := make([]string, len(recs))
	for i, r := range recs {
		ids[i] = r.ID
	}
	if err := storage.RetryOnBusy(ctx, func() error {
		return m.db.MergeAndReplaceRecordings(ctx, mergedRec, ids)
	}); err != nil {
		logger.Error("failed to merge and replace MJPEG recordings", "camera_id", cameraID, "error", err)
		// Clean up orphaned merged directory, but keep source dirs for recovery.
		os.RemoveAll(mergedRec.FilePath)
		m.metrics.RecordMergeFailure("db_error")
		return 0, 0, 0
	}

	// Only delete source directories AFTER successful DB transaction.
	for _, dir := range sourceDirs {
		os.RemoveAll(dir)
	}
	// Record successful merge metrics.
	m.metrics.RecordMergeSuccess(time.Since(mergeStart), mergedRec.FileSize)

	logger.Info(
		"merged MJPEG segments",
		"camera_id", cameraID,
		"segments", len(recs),
		"duration_s", mergedRec.Duration,
		"size_bytes", mergedRec.FileSize,
		"freed_bytes", estSize,
	)

	return 1, len(recs), estSize
}

// mergeAVIGroup merges AVI recording files into a single merged AVI file.
// AVI segments are single files containing MJPEG+G.711 data — ParseSegment only handles MP4.
func (m *MergeManager) mergeAVIGroup(ctx context.Context, cameraID string, recs []*model.Recording, cfg config.MergeConfig) (merged, segments int, freed int64) {
	if len(recs) < cfg.MinSegmentsToMerge {
		return 0, 0, 0
	}

	mergeStart := time.Now()

	// Estimate merged size from source recordings.
	var estSize int64
	for _, r := range recs {
		estSize += r.FileSize
	}

	// Check disk space.
	total, used, err := m.store.GetDiskUsage()
	if err != nil {
		logger.Warn("failed to get disk usage", "error", err)
		m.metrics.RecordMergeFailure("disk_space")
		return 0, 0, 0
	}
	freeSpace := total - used
	required := estSize * 11 / 10
	if freeSpace < required {
		logger.Warn("insufficient disk space for AVI merge", "camera_id", cameraID, "needed", required, "free", freeSpace)
		m.metrics.RecordMergeFailure("disk_space")
		return 0, 0, 0
	}

	// Delegate to MergeAVISegments — handles streaming merge and output file creation.
	mergedRec, sourcePaths, err := MergeAVISegments(ctx, recs, m.store, cameraID)
	if err != nil {
		logger.Error("failed to merge AVI segments", "camera_id", cameraID, "error", err)
		m.metrics.RecordMergeFailure("merge_error")
		return 0, 0, 0
	}

	mergedRec.Merged = true

	// Atomic: insert merged recording + delete old recordings in single transaction.
	ids := make([]string, len(recs))
	for i, r := range recs {
		ids[i] = r.ID
	}
	if err := storage.RetryOnBusy(ctx, func() error {
		return m.db.MergeAndReplaceRecordings(ctx, mergedRec, ids)
	}); err != nil {
		logger.Error("failed to merge and replace AVI recordings", "camera_id", cameraID, "error", err)
		os.Remove(mergedRec.FilePath)
		m.metrics.RecordMergeFailure("db_error")
		return 0, 0, 0
	}

	// Only delete source files AFTER successful DB transaction.
	for _, path := range sourcePaths {
		m.store.DeleteFile(path)
	}

	m.metrics.RecordMergeSuccess(time.Since(mergeStart), mergedRec.FileSize)

	logger.Info(
		"merged AVI segments",
		"camera_id", cameraID,
		"segments", len(recs),
		"duration_s", mergedRec.Duration,
		"size_bytes", mergedRec.FileSize,
		"freed_bytes", estSize,
	)

	return 1, len(recs), estSize
}
