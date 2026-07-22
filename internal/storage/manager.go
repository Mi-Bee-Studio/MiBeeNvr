package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// Manager handles file system storage for camera recordings.
// It provides atomic writes via a .tmp → rename pattern.
type Manager struct {
	rootDir string
	metrics *metrics.Metrics
	mu      sync.Mutex

	// Health tracking (per-camera)
	healthMu      sync.Mutex
	cameraHealths map[string]*cameraHealth

	// tempPath → cameraID mapping for WriteFrame/CloseSegment health tracking.
	segmentCameraMap map[string]string
	segMapMu         sync.RWMutex

	// Optional event bus for health state change notifications
	eventBus *event.EventBus
}

// NewManager creates a new storage Manager and ensures the root directory exists.
func NewManager(rootDir string, opts ...*metrics.Metrics) (*Manager, error) {
	var m *metrics.Metrics
	if len(opts) > 0 {
		m = opts[0]
	}
	if rootDir == "" {
		return nil, fmt.Errorf("storage: root directory path must not be empty")
	}
	if err := os.MkdirAll(rootDir, 0o755); err != nil {
		return nil, fmt.Errorf("storage: failed to create root directory %q: %w", rootDir, err)
	}
	return &Manager{
		rootDir:          rootDir,
		metrics:          m,
		cameraHealths:    make(map[string]*cameraHealth),
		segmentCameraMap: make(map[string]string),
	}, nil
}

// RootDir returns the root directory path.
func (m *Manager) RootDir() string {
	return m.rootDir
}

// EnsureCameraDir creates the directory for a camera if it doesn't exist.
func (m *Manager) EnsureCameraDir(cameraID string) error {
	dir := filepath.Join(m.rootDir, cameraID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("storage: failed to create camera dir %q: %w", dir, err)
	}
	return nil
}

// CreateSegment creates a new recording segment.
// For format "h264": creates a .tmp file for writing MP4 data.
// For formats "mjpeg" and "timelapse": creates a .tmp directory for writing JPEG frames.
// Returns the temp path (for writing) and the suggested final path (for CloseSegment).
func (m *Manager) CreateSegment(cameraID string, format string) (tempPath string, finalPath string, err error) {
	if err := m.EnsureCameraDir(cameraID); err != nil {
		m.recordWriteFailure(cameraID)
		return "", "", err
	}

	now := time.Now()
	// Time-bucketed layout: cameraID/YYYYMM/DD/HH/segment.mp4
	// Reduces per-directory file count from thousands (flat layout) to
	// ~120/hour, dramatically improving ext4 readdir/stat performance on
	// slow USB HDD storage. Old flat-layout files coexist until naturally
	// aged out by retention.
	hourDir := filepath.Join(m.rootDir, cameraID, now.Format("200601"), now.Format("02"), now.Format("15"))
	if err := os.MkdirAll(hourDir, 0o755); err != nil {
		m.recordWriteFailure(cameraID)
		return "", "", fmt.Errorf("storage: failed to create hour bucket dir: %w", err)
	}
	ts := now.Format("20060102_150405")
	uuid := strconv.FormatInt(now.UnixNano(), 10)

	switch strings.ToLower(format) {
	case "h264", "h265":
		tempPath = filepath.Join(hourDir, uuid+".tmp")
		finalPath = filepath.Join(hourDir, fmt.Sprintf("%s_%s_%s.mp4", cameraID, ts, uuid))
		f, err := os.Create(tempPath)
		if err != nil {
			m.recordWriteFailure(cameraID)
			return "", "", fmt.Errorf("storage: failed to create temp file: %w", err)
		}
		f.Close()

	case "mjpeg", "timelapse":
		tempPath = filepath.Join(hourDir, uuid+".tmp")
		finalPath = filepath.Join(hourDir, fmt.Sprintf("%s_%s_%s", cameraID, ts, uuid))

		if err := os.MkdirAll(tempPath, 0o755); err != nil {
			m.recordWriteFailure(cameraID)
			return "", "", fmt.Errorf("storage: failed to create temp dir: %w", err)
		}

	case "avi":
		tempPath = filepath.Join(hourDir, uuid+".tmp")
		finalPath = filepath.Join(hourDir, fmt.Sprintf("%s_%s_%s.avi", cameraID, ts, uuid))
		f, err := os.Create(tempPath)
		if err != nil {
			m.recordWriteFailure(cameraID)
			return "", "", fmt.Errorf("storage: failed to create temp file: %w", err)
		}
		f.Close()
	default:
		return "", "", fmt.Errorf("storage: unsupported format %q", format)
	}

	// Register tempPath → cameraID mapping for WriteFrame health tracking.
	m.segMapMu.Lock()
	m.segmentCameraMap[tempPath] = cameraID
	m.segMapMu.Unlock()

	return tempPath, finalPath, nil
}

// CloseSegment atomically finalizes a segment by syncing and renaming .tmp to final path.
func (m *Manager) CloseSegment(tempPath, finalPath string) error {
	// Check if temp is a directory (MJPEG) or file (H.264)
	info, err := os.Stat(tempPath)
	if err != nil {
		m.RecordWriteFailureForPath(tempPath)
		m.unregisterTempPath(tempPath)
		return fmt.Errorf("storage: temp path not found: %w", err)
	}

	if info.IsDir() {
		// Sync the directory for MJPEG
		dirFd, err := os.Open(tempPath)
		if err != nil {
			m.RecordWriteFailureForPath(tempPath)
			m.unregisterTempPath(tempPath)
			return fmt.Errorf("storage: cannot open temp dir for sync: %w", err)
		}
		if err := dirFd.Sync(); err != nil {
			dirFd.Close()
			m.RecordWriteFailureForPath(tempPath)
			return fmt.Errorf("storage: failed to sync temp dir: %w", err)
		}
		dirFd.Close()

		// Atomic rename of directory
		if err := os.Rename(tempPath, finalPath); err != nil {
			m.RecordWriteFailureForPath(tempPath)
			m.unregisterTempPath(tempPath)
			return fmt.Errorf("storage: failed to rename temp dir to final: %w", err)
		}
	} else {
		// Sync and close the file for H.264
		f, err := os.OpenFile(tempPath, os.O_WRONLY, 0)
		if err != nil {
			m.RecordWriteFailureForPath(tempPath)
			m.unregisterTempPath(tempPath)
			return fmt.Errorf("storage: cannot open temp file for sync: %w", err)
		}
		if err := f.Sync(); err != nil {
			f.Close()
			m.RecordWriteFailureForPath(tempPath)
			return fmt.Errorf("storage: failed to sync temp file: %w", err)
		}
		f.Close()

		// Atomic rename
		if err := os.Rename(tempPath, finalPath); err != nil {
			m.RecordWriteFailureForPath(tempPath)
			m.unregisterTempPath(tempPath)
			return fmt.Errorf("storage: failed to rename temp file to final: %w", err)
		}
	}

	// Success — unregister mapping.
	m.unregisterTempPath(tempPath)
	return nil
}

// WriteFrame writes data to a segment's temp path.
// For H.264: appends data to the temp file.
// For MJPEG: creates a timestamped .jpg file in the temp directory.
func (m *Manager) WriteFrame(tempPath string, data []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	info, err := os.Stat(tempPath)
	if err != nil {
		m.RecordWriteFailureForPath(tempPath)
		return 0, fmt.Errorf("storage: temp path not accessible: %w", err)
	}

	if info.IsDir() {
		// MJPEG: write individual JPEG file with timestamp name
		ts := time.Now().Format("20060102_150405.000")
		jpgPath := filepath.Join(tempPath, ts+".jpg")
		if err := os.WriteFile(jpgPath, data, 0o644); err != nil {
			m.RecordWriteFailureForPath(tempPath)
			return 0, fmt.Errorf("storage: failed to write JPEG frame: %w", err)
		}
		m.RecordWriteSuccessForPath(tempPath)
		return 0, nil
	}

	// H.264: append to temp file
	f, err := os.OpenFile(tempPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		m.RecordWriteFailureForPath(tempPath)
		return 0, fmt.Errorf("storage: failed to open temp file for writing: %w", err)
	}
	defer f.Close()

	n, err := f.Write(data)
	if err != nil {
		m.RecordWriteFailureForPath(tempPath)
		return n, fmt.Errorf("storage: write failed: %w", err)
	}

	m.RecordWriteSuccessForPath(tempPath)
	return n, nil
}

// unregisterTempPath removes the tempPath → cameraID mapping.
// Safe for concurrent use.
func (m *Manager) unregisterTempPath(tempPath string) {
	m.segMapMu.Lock()
	delete(m.segmentCameraMap, tempPath)
	m.segMapMu.Unlock()
}

// ListFiles lists all recording files (non-.tmp) for a camera.
func (m *Manager) ListFiles(cameraID string) ([]string, error) {
	cameraDir := filepath.Join(m.rootDir, cameraID)

	// Return an explicit error for a nonexistent camera directory —
	// filepath.WalkDir would otherwise silently return an empty result.
	if _, statErr := os.Stat(cameraDir); statErr != nil {
		return nil, fmt.Errorf("storage: cannot read camera dir %q: %w", cameraDir, statErr)
	}

	var files []string
	err := filepath.WalkDir(cameraDir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil // skip inaccessible entries
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		// Skip temp files and hidden files
		if strings.HasSuffix(name, ".tmp") || strings.HasPrefix(name, ".") {
			return nil
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("storage: cannot walk camera dir %q: %w", cameraDir, err)
	}
	return files, nil
}

// ListSegments lists all recording segment entries for a camera. Unlike
// ListFiles (which returns only leaf files — e.g. the individual .jpg frames
// inside an MJPEG segment directory), ListSegments returns one entry per
// segment: directories for MJPEG/timelapse recordings, files (.mp4/.avi) for
// H.264/H.265/AVI recordings. Temp (.tmp) and hidden entries are skipped.
//
// Use this (not ListFiles) when you need to count or inspect segments — the
// segment directory IS the unit of a recording segment, regardless of format.
func (m *Manager) ListSegments(cameraID string) ([]string, error) {
	cameraDir := filepath.Join(m.rootDir, cameraID)

	// Return an explicit error for a nonexistent camera directory —
	// filepath.WalkDir would otherwise silently return an empty result.
	if _, statErr := os.Stat(cameraDir); statErr != nil {
		return nil, fmt.Errorf("storage: cannot read camera dir %q: %w", cameraDir, statErr)
	}

	var segments []string
	err := filepath.WalkDir(cameraDir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil // skip inaccessible entries
		}
		// Skip temp and hidden entries at any level.
		name := d.Name()
		if strings.HasSuffix(name, ".tmp") || strings.HasPrefix(name, ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		// A segment entry is either:
		//   - a directory whose name matches the MJPEG/timelapse segment pattern
		//     "{cameraID}_{timestamp}_{uuid}" (no extension), OR
		//   - a file whose name matches the mp4/avi segment pattern
		//     "{cameraID}_{timestamp}_{uuid}.{ext}".
		// Both contain an underscore-delimited timestamp + uuid. The recording
		// tree is YYYYMM/DD/HH/<segment>, so segments live exactly three levels
		// below the camera dir. We detect them by the naming pattern rather than
		// by depth so this stays robust to tree-layout changes.
		if isSegmentEntry(name) {
			segments = append(segments, path)
			if d.IsDir() {
				return filepath.SkipDir // don't descend into the segment dir
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("storage: cannot walk camera dir %q: %w", cameraDir, err)
	}
	return segments, nil
}

// isSegmentEntry reports whether name looks like a recording segment entry
// (MJPEG/timelapse dir or mp4/avi file). It matches the segment naming pattern
// produced by CreateSegment: "{cameraID}_{timestamp}_{uuid}" optionally with an
// extension. This is intentionally a structural check (≥2 underscores, the
// middle field parses as digits) rather than a strict format match, so it
// tolerates cameraID characters and uuid variations.
func isSegmentEntry(name string) bool {
	// Must have at least the {cameraID}_{ts}_{uuid} shape → ≥2 underscores.
	first := strings.IndexByte(name, '_')
	if first < 0 {
		return false
	}
	rest := name[first+1:]
	second := strings.IndexByte(rest, '_')
	if second < 0 {
		return false
	}
	// The timestamp field (between the first two underscores) should be all digits.
	ts := rest[:second]
	if len(ts) == 0 {
		return false
	}
	for i := range ts {
		if ts[i] < '0' || ts[i] > '9' {
			return false
		}
	}
	return true
}

// ListCameraDirEntries returns all entries in a camera's storage directory.
func (m *Manager) ListCameraDirEntries(cameraID string) ([]os.DirEntry, error) {
	cameraDir := filepath.Join(m.rootDir, cameraID)
	entries, err := os.ReadDir(cameraDir)
	if err != nil {
		return nil, fmt.Errorf("storage: cannot read camera dir %q: %w", cameraID, err)
	}
	return entries, nil
}

// GetFileSize returns the size of a file in bytes.
func (m *Manager) GetFileSize(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, fmt.Errorf("storage: cannot stat %q: %w", path, err)
	}
	return info.Size(), nil
}

// DeleteFile removes a file or directory from disk.
// For directory-based recordings (MJPEG, timelapse), it uses os.RemoveAll.
func (m *Manager) DeleteFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("storage: failed to stat %q: %w", path, err)
	}
	if info.IsDir() {
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("storage: failed to remove directory %q: %w", path, err)
		}
	} else {
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("storage: failed to delete %q: %w", path, err)
		}
	}
	return nil
}

// DeleteCameraDir removes the entire directory for a camera.
func (m *Manager) DeleteCameraDir(cameraID string) error {
	dir := filepath.Join(m.rootDir, cameraID)
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("storage: failed to remove camera dir %q: %w", dir, err)
	}
	return nil
}

// GetDiskUsage returns total and used disk space for the filesystem containing rootDir.
func (m *Manager) GetDiskUsage() (total int64, used int64, err error) {
	var stat syscall.Statfs_t

	if err := syscall.Statfs(m.rootDir, &stat); err != nil {
		return 0, 0, fmt.Errorf("storage: failed to stat filesystem: %w", err)
	}

	// Total space in bytes
	total = int64(stat.Blocks * uint64(stat.Bsize))
	// Free space in bytes
	free := int64(stat.Bfree * uint64(stat.Bsize))
	// Used = total - free
	used = total - free

	// Update storage metrics
	if m.metrics != nil {
		m.metrics.StorageUsedBytes.Set(float64(used))
		m.metrics.StorageTotalBytes.Set(float64(total))
	}

	return total, used, nil
}

// IsAvailable checks whether the root directory is accessible.
func (m *Manager) IsAvailable() bool {
	_, err := os.Stat(m.rootDir)
	return err == nil
}

// CleanupTempFiles removes all orphaned .tmp files and directories left by
// crashed segment writes. Temp segments are only ever created under
// <root>/cam-xxx/YYYY/MM/DD/HH/{uuid}.tmp (see CreateSegment), so this scan is
// scoped to cam-* subtrees only — it skips hls/, recordings/, bin/, certs/,
// database files, and any other non-camera content at the root. This keeps the
// scan bounded: on a production tree with 100k+ files it avoids walking the
// HLS shard directories entirely.
//
// This function is safe to call concurrently with recording (each segment uses
// a unique uuid path; a leftover .tmp from a previous crash never collides with
// a new write). Callers that don't need the result immediately should run it in
// a goroutine to avoid blocking startup.
func (m *Manager) CleanupTempFiles() error {
	entries, err := os.ReadDir(m.rootDir)
	if err != nil {
		return fmt.Errorf("storage: read root dir %q: %w", m.rootDir, err)
	}
	var firstErr error
	for _, entry := range entries {
		// Only descend into camera directories (prefix "cam-"). Everything else
		// at the root (hls/, recordings/, bin/, certs/, *.db, config files,
		// top-level *.tmp backups, etc.) is out of scope for segment cleanup.
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "cam-") {
			continue
		}
		camDir := filepath.Join(m.rootDir, entry.Name())
		if err := filepath.WalkDir(camDir, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil // skip inaccessible entries
			}
			if !d.IsDir() {
				// Remove .tmp files
				if strings.HasSuffix(d.Name(), ".tmp") {
					if err := os.Remove(path); err != nil {
						// Don't abort the whole walk on a single failure (file
						// may be in use); record and continue.
						logger.Warn("temp cleanup: failed to remove temp file", "path", path, "error", err)
					}
				}
				return nil
			}
			// Don't remove the camera dir itself, and skip .tmp directories
			if path == camDir {
				return nil
			}
			if strings.HasSuffix(d.Name(), ".tmp") {
				if err := os.RemoveAll(path); err != nil {
					logger.Warn("temp cleanup: failed to remove temp dir", "path", path, "error", err)
				}
				return filepath.SkipDir
			}
			return nil
		}); err != nil {
			logger.Warn("temp cleanup: walk error", "dir", camDir, "error", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// ReconcileOrphanedFiles scans camera directories for .mp4 files that are not registered
// in the database and inserts their metadata. Returns the number of reconciled files.
// Uses per-camera incremental commits to avoid holding the write lock too long.
// Context timeout is checked between camera directories.
func (m *Manager) ReconcileOrphanedFiles(ctx context.Context, db *DB, cameraIDs map[string]bool) (int, error) {
	entries, err := os.ReadDir(m.rootDir)
	if err != nil {
		return 0, err
	}

	skippedDirs := map[string]bool{"hls": true, "recordings": true, "logs": true, "backups": true, "bin": true}
	totalReconciled := 0

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dirName := entry.Name()
		if skippedDirs[dirName] || !cameraIDs[dirName] {
			continue
		}

		// Check context timeout between cameras
		if ctx.Err() != nil {
			return totalReconciled, ctx.Err()
		}

		reconciled, err := m.reconcileCameraDir(ctx, db, dirName)
		if err != nil {
			logger.Warn("reconcile: error processing camera dir", "dir", dirName, "error", err)
			continue
		}
		totalReconciled += reconciled
	}

	if totalReconciled > 0 {
		logger.Info("reconciled orphaned recording files", "count", totalReconciled)
	}

	return totalReconciled, nil
}

// recordingNameInfo holds the parsed fields of a recording file/dir name.
// Populated by parseRecordingName when the name matches the canonical shape.
type recordingNameInfo struct {
	cameraID  string
	startedAt time.Time
	nanoID    string
	isMP4File bool // true for .mp4 files (H264), false for extension-less MJPEG dirs
}

// parseRecordingName parses a cam-dir entry name into its recording fields,
// without touching the disk. Returns ok=false if the name does not match the
// canonical recording shape:
//
//	<camID>_YYYYMMDD_HHMMSS_<nano>        (MJPEG dir, no extension)
//	<camID>_YYYYMMDD_HHMMSS_<nano>.mp4     (H264 file)
//
// This is the cheap name-shape gate used by reconcileCameraDir to skip
// non-recording entries — most importantly date-bucket dirs ("202607/", "20/",
// "07/") created by the segment writer — before paying for any stat or subtree
// walk. Misclassifying a date dir as an MJPEG recording dir caused a production
// 8.7GB / 6.5-minute IO storm because each date dir's entire subtree got walked.
//
// cameraIDHint, when non-empty, requires parts[0] == cameraIDHint (the parent
// cam dir name); entries from other cameras are rejected. Pass "" to skip the
// camera-ID check (only shape is validated).
func parseRecordingName(name, cameraIDHint string) (recordingNameInfo, bool) {
	isMP4 := strings.HasSuffix(name, ".mp4")
	baseName := name
	if isMP4 {
		baseName = strings.TrimSuffix(name, ".mp4")
	} else if filepath.Ext(name) != "" {
		// Non-mp4 entry with an extension (.avi.tmp, .json, .tmp, ...) — not a
		// recording name. Extension-less dirs are still candidates (MJPEG).
		return recordingNameInfo{}, false
	}
	parts := strings.SplitN(baseName, "_", 4)
	if len(parts) != 4 {
		return recordingNameInfo{}, false
	}
	if cameraIDHint != "" && parts[0] != cameraIDHint {
		return recordingNameInfo{}, false
	}
	startedAt, err := time.ParseInLocation("20060102_150405", parts[1]+"_"+parts[2], time.Local)
	if err != nil {
		return recordingNameInfo{}, false
	}
	return recordingNameInfo{
		cameraID:  parts[0],
		startedAt: startedAt,
		nanoID:    parts[3],
		isMP4File: isMP4,
	}, true
}

// reconcileCameraDir scans a single camera directory and inserts orphaned recordings.
// Uses incremental batches of orphanBatchSize to minimize write lock duration.
//
// To avoid expensive disk IO on large trees, entry filtering is done in two
// cheap phases BEFORE any stat or subtree walk, both encapsulated in
// parseRecordingName:
//  1. Name-shape gate: only entries shaped "<camID>_YYYYMMDD_HHMMSS_<nano>"
//     (optionally suffixed with .mp4) are considered. This skips date bucket
//     directories (e.g. "202607/", "20/", "07/") that the segment writer
//     creates under each cam dir, as well as random unrelated files. Without
//     this gate, a single date directory like "202607/" would be misclassified
//     as an MJPEG recording dir and fully walked, causing the entire historical
//     subtree (potentially 100k+ files) to be re-stat'd per date dir.
//  2. Camera-ID gate: parts[0] must equal dirName. Entries from other cameras
//     or stale exports are skipped without IO.
//
// Only after both gates does the function call f.Info() (one stat per
// surviving entry) and, for MJPEG dirs, the per-frame Walk.
func (m *Manager) reconcileCameraDir(ctx context.Context, db *DB, dirName string) (int, error) {
	files, err := os.ReadDir(filepath.Join(m.rootDir, dirName))
	if err != nil {
		return 0, err
	}

	var cameraOrphans []model.Recording
	for _, f := range files {
		name := f.Name()

		// Phase 1 + 2: cheap name-shape + camera-ID gate, no IO. This is what
		// prevents date-bucket dirs from being walked.
		info, ok := parseRecordingName(name, dirName)
		if !ok {
			continue
		}

		// Only now — after the gates pass — pay for one stat per entry.
		fi, fiErr := f.Info()
		if fiErr != nil {
			continue
		}

		var totalSize int64
		var frameCount int
		var format model.Format
		if f.IsDir() {
			// MJPEG recording directory: count JPEG frames and total size.
			// Only legit recording-named dirs reach here, so this Walk is now
			// bounded to actual frame dirs (tens of entries), not date trees.
			format = model.FormatMJPEG
			dirPath := filepath.Join(m.rootDir, dirName, name)
			filepath.Walk(dirPath, func(path string, walkFI os.FileInfo, walkErr error) error {
				if walkErr != nil || walkFI.IsDir() {
					return nil
				}
				frameCount++
				totalSize += walkFI.Size()
				return nil
			})
			if frameCount == 0 {
				continue
			}
		} else {
			if !info.isMP4File {
				continue // named like a recording but not .mp4 — shouldn't happen
			}
			if fi.Size() == 0 {
				continue
			}
			format = model.FormatH264
			totalSize = fi.Size()
		}

		cameraOrphans = append(cameraOrphans, model.Recording{
			ID:         info.nanoID,
			CameraID:   dirName,
			FilePath:   filepath.Join(m.rootDir, dirName, name),
			Format:     format,
			StartedAt:  info.startedAt,
			EndedAt:    info.startedAt,
			Duration:   0,
			FileSize:   totalSize,
			FrameCount: frameCount,
			MergeStatus: model.MergeStatusPending,
		})
	}

	if len(cameraOrphans) == 0 {
		return 0, nil
	}

	// Query which files already exist in DB. Use the per-camera path set rather
	// than GetRecordingsByPathSet(IN ?, ?, ...): the per-camera query rides the
	// idx_recordings_camera_time index for a bounded range scan of just this
	// camera's rows, instead of a full table scan on the unindexed file_path
	// column. On a production tree (~15k recordings, ~6 cameras) this cut
	// reconcile IO dramatically. We intersect locally against the hashset.
	existing, err := db.GetRecordingPathsByCamera(ctx, dirName)
	if err != nil {
		return 0, fmt.Errorf("query existing recordings for %q: %w", dirName, err)
	}

	var toInsert []*model.Recording
	for i := range cameraOrphans {
		if !existing[cameraOrphans[i].FilePath] {
			toInsert = append(toInsert, &cameraOrphans[i])
		}
	}

	if len(toInsert) == 0 {
		return 0, nil
	}

	reconciled, err := db.InsertOrphanRecordings(ctx, toInsert)
	if err != nil {
		return 0, fmt.Errorf("insert orphan recordings for %q: %w", dirName, err)
	}

	return reconciled, nil
}
