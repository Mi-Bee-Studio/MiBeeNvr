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

// CleanupTempFiles removes all orphaned .tmp files and directories from the storage root.
func (m *Manager) CleanupTempFiles() error {
	return filepath.WalkDir(m.rootDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip inaccessible entries
		}
		if d.IsDir() {
			// Don't remove the root dir itself, and skip .tmp directories
			if path == m.rootDir {
				return nil
			}
			if strings.HasSuffix(d.Name(), ".tmp") {
				if err := os.RemoveAll(path); err != nil {
					return fmt.Errorf("storage: failed to remove temp dir %q: %w", path, err)
				}
				return filepath.SkipDir
			}
			return nil
		}
		// Remove .tmp files
		if strings.HasSuffix(d.Name(), ".tmp") {
			if err := os.Remove(path); err != nil {
				return fmt.Errorf("storage: failed to remove temp file %q: %w", path, err)
			}
		}
		return nil
	})
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

// reconcileCameraDir scans a single camera directory and inserts orphaned recordings.
// Uses incremental batches of orphanBatchSize to minimize write lock duration.
func (m *Manager) reconcileCameraDir(ctx context.Context, db *DB, dirName string) (int, error) {
	files, err := os.ReadDir(filepath.Join(m.rootDir, dirName))
	if err != nil {
		return 0, err
	}

	var cameraOrphans []model.Recording
	for _, f := range files {
		name := f.Name()

		var baseName string
		var frameCount int
		var totalSize int64
		var format model.Format
		info, infoErr := f.Info()
		if infoErr != nil {
			continue
		}

		if f.IsDir() {
			// Skip dirs with extensions (e.g., .tmp dirs)
			if ext := filepath.Ext(name); ext != "" {
				continue
			}
			baseName = name
			format = model.FormatMJPEG
			// Count JPEG frames and total size
			dirPath := filepath.Join(m.rootDir, dirName, name)
			filepath.Walk(dirPath, func(path string, fi os.FileInfo, err error) error {
				if err != nil || fi.IsDir() {
					return nil
				}
				frameCount++
				totalSize += fi.Size()
				return nil
			})
			if frameCount == 0 {
				continue
			}
		} else {
			if !strings.HasSuffix(name, ".mp4") {
				continue
			}
			baseName = strings.TrimSuffix(name, ".mp4")
			format = model.FormatH264
			if info.Size() == 0 {
				continue
			}
			totalSize = info.Size()
		}

		parts := strings.SplitN(baseName, "_", 4)
		if len(parts) != 4 {
			continue
		}

		cameraIDPart := parts[0]
		dateStr := parts[1]
		timeStr := parts[2]
		nanoStr := parts[3]

		if cameraIDPart != dirName {
			continue
		}

		startedAt, err := time.ParseInLocation("20060102_150405", dateStr+"_"+timeStr, time.Local)
		if err != nil {
			continue
		}

		cameraOrphans = append(cameraOrphans, model.Recording{
			ID:         nanoStr,
			CameraID:   dirName,
			FilePath:   filepath.Join(m.rootDir, dirName, name),
			Format:     format,
			StartedAt:  startedAt,
			EndedAt:    startedAt,
			Duration:   0,
			FileSize:   totalSize,
			FrameCount: frameCount,
			Merged:     false,
		})
	}

	if len(cameraOrphans) == 0 {
		return 0, nil
	}

	// Query which files already exist in DB
	paths := make([]string, len(cameraOrphans))
	for i, o := range cameraOrphans {
		paths[i] = o.FilePath
	}
	existing, err := db.GetRecordingsByPathSet(ctx, paths)
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
