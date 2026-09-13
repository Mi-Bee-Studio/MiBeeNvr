package cleanup

// This file holds the orphan-file cleanup strategy: scans each configured
// camera's directory for files/subdirectories not tracked in the recordings
// table (leftover .mp4 segments, abandoned MJPEG/.tmp dirs) and removes them.
// Items younger than 1h are skipped to avoid racing an in-progress write.
// Runs once per RunOnce cycle.
//
// In addition, a DEEP recursive scan (once per deepOrphanInterval) walks the
// nested YYYYMM/DD/HH trees — where rolling-merge outputs live — and removes
// media artifacts referenced by neither file_path nor merge_path. That leak
// class (pre-#117 delete paths, crashed merges, abandoned MJPEG frame dirs)
// accumulated to 16.2 GB / 28k files on a field box (2026-09-11) because the
// top-level-only scan could never reach it.
//
// Extracted from cleanup.go (#227).

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// deepOrphanInterval bounds how often the recursive nested-tree walk runs:
// the walk is real IO on the recording disk (a spinning HDD on the target
// class), so it must not ride every cleanup cycle.
const deepOrphanInterval = 24 * time.Hour

// deepOrphanExt is the deletion allowlist: only media artifacts are ever
// removed — anything else on disk (user notes, unknown files) is untouchable.
var deepOrphanExt = map[string]bool{
	".mp4": true, ".tmp": true, ".h264": true, ".h265": true,
	".jpg": true, ".jpeg": true, ".avi": true, ".mjpeg": true, ".ts": true,
}

// orphanFileCleanup scans camera directories for files/directories not tracked
// in the recordings table and removes them.
func (cm *CleanupManager) orphanFileCleanup(ctx context.Context) {
	cameras, err := cm.activeCameraIDs(ctx)
	if err != nil {
		logger.Warn("orphan cleanup: failed to list cameras", "error", err)
		return
	}
	var totalDeleted int
	for _, cam := range cameras {
		totalDeleted += cm.cleanOrphansForCamera(ctx, cam)
	}
	if totalDeleted > 0 {
		logger.Info("orphan files cleaned up", "deleted", totalDeleted)
	}
	if time.Since(cm.deepOrphanLast) >= deepOrphanInterval {
		cm.deepOrphanLast = time.Now()
		start := time.Now()
		deep := cm.deepOrphanCleanup(ctx, cameras)
		// Always log (#725 上线即量化): a zero-deletion run is the proof the
		// leak class is being kept at zero, and the duration exposes the walk
		// cost on slow disks.
		logger.Info("deep orphan scan complete",
			"cameras", len(cameras), "deleted", deep, "duration", time.Since(start))
	}
}

// cleanOrphansForCamera scans a single camera directory for orphans.
func (cm *CleanupManager) cleanOrphansForCamera(ctx context.Context, cameraID string) int {
	dbBasenames, err := cm.db.ListRecordingPathsByCamera(ctx, cameraID)
	if err != nil {
		logger.Warn("orphan cleanup: failed to list recording paths", "camera_id", cameraID, "error", err)
		return 0
	}
	entries, err := cm.store.ListCameraDirEntries(cameraID)
	if err != nil {
		return 0 // directory may not exist
	}
	var deleted int
	for _, entry := range entries {
		name := entry.Name()
		info, err := entry.Info()
		if err != nil {
			continue
		}
		// Skip items younger than 1 hour
		if time.Since(info.ModTime()) < time.Hour {
			continue
		}
		// Skip known recordings
		if dbBasenames[name] {
			continue
		}
		fullPath := filepath.Join(cm.store.RootDir(), cameraID, name)
		if info.IsDir() {
			// MJPEG directories or .tmp directories
			if strings.HasPrefix(name, cameraID+"_") || strings.HasSuffix(name, ".tmp") {
				if err := os.RemoveAll(fullPath); err != nil {
					logger.Warn("orphan cleanup: failed to remove dir", "path", fullPath, "error", err)
					continue
				}
				logger.Info("deleted orphan directory", "camera_id", cameraID, "dir", name)
				if cm.metrics != nil {
					cm.metrics.CleanupDeleted.WithLabelValues("orphan").Add(1)
				}
				deleted++
			}
		} else if strings.HasSuffix(name, ".mp4") && strings.HasPrefix(name, cameraID+"_") {
			if err := os.Remove(fullPath); err != nil {
				logger.Warn("orphan cleanup: failed to delete file", "path", fullPath, "error", err)
				continue
			}
			logger.Info("deleted orphan file", "camera_id", cameraID, "file", name)
			if cm.metrics != nil {
				cm.metrics.CleanupDeleted.WithLabelValues("orphan").Add(1)
			}
			deleted++
		}
	}
	return deleted
}

// deepOrphanCleanup walks each camera's full nested tree and deletes media
// artifacts referenced by neither file_path nor merge_path (#117 leak class:
// the nested YYYYMM/DD/HH tree is where merged outputs live — a leaked output
// is invisible to the top-level scan). Safety rails mirror the top-level
// scan: skip the young (<1h), skip the referenced, media extensions only.
// Emptied date directories are pruned after the file pass.
func (cm *CleanupManager) deepOrphanCleanup(ctx context.Context, cameras []string) int {
	var deleted int
	for _, cam := range cameras {
		refs, err := cm.db.ListRecordingArtifactPathsByCamera(ctx, cam)
		if err != nil {
			logger.Warn("deep orphan cleanup: failed to list artifacts", "camera_id", cam, "error", err)
			continue
		}
		// DB paths may be absolute or relative to the storage root (tierrec's
		// sub-layer rows store relatives), and may mix separators — while the
		// walk yields native ABSOLUTE paths. Normalize refs to absolute form so
		// both path styles match; comparing raw strings silently orphaned every
		// sub_ segment past the age rail (2026-09-13: 9.3k dangling rows, the
		// sub-stream tier wiped on one camera).
		root := cm.store.RootDir()
		normRefs := make(map[string]bool, len(refs))
		for p := range refs {
			clean := filepath.Clean(p)
			if !filepath.IsAbs(clean) {
				clean = filepath.Join(root, clean)
			}
			normRefs[filepath.ToSlash(clean)] = true
		}
		camRoot := filepath.Join(cm.store.RootDir(), cam)
		var emptyableDirs []string
		err = filepath.WalkDir(camRoot, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				// Unreadable entry: skip it, never fail the walk.
				return filepath.SkipDir
			}
			if path == camRoot {
				return nil
			}
			if d.IsDir() {
				// A REFERENCED directory is a directory-form recording itself
				// (MJPEG/timelapse frame dirs are stored as file_path on the
				// row) — its contents are protected as a whole and it must
				// never be pruned.
				if normRefs[filepath.ToSlash(path)] {
					return filepath.SkipDir
				}
				emptyableDirs = append(emptyableDirs, path)
				return nil
			}
			if referencedUnder(normRefs, path, camRoot) {
				return nil
			}
			info, statErr := d.Info()
			if statErr != nil {
				return nil //nolint:nilerr // TODO(#744): unreadable entry — skip, never abort the walk
			}
			if time.Since(info.ModTime()) < time.Hour {
				return nil
			}
			if !deepOrphanExt[strings.ToLower(filepath.Ext(path))] {
				return nil
			}
			if err := os.Remove(path); err != nil {
				logger.Warn("deep orphan cleanup: failed to delete", "path", path, "error", err)
				return nil
			}
			deleted++
			if cm.metrics != nil {
				cm.metrics.CleanupDeleted.WithLabelValues("orphan").Add(1)
			}
			return nil
		})
		if err != nil {
			logger.Warn("deep orphan cleanup: walk failed", "camera_id", cam, "error", err)
			continue
		}
		// Prune emptied date dirs deepest-first; never remove the camera root.
		for i := len(emptyableDirs) - 1; i >= 0; i-- {
			entries, err := os.ReadDir(emptyableDirs[i])
			if err == nil && len(entries) == 0 {
				_ = os.Remove(emptyableDirs[i])
			}
		}
	}
	return deleted
}

// referencedUnder reports whether path itself OR any ancestor directory up to
// camRoot is referenced. Directory-form recordings reference the DIRECTORY as
// file_path while the frames inside are individual files — a file-only check
// would shred every frame of every MJPEG/timelapse recording older than the
// age rail. An ancestor hit therefore protects the whole subtree.
func referencedUnder(refs map[string]bool, path, camRoot string) bool {
	p := filepath.ToSlash(path)
	for {
		if refs[p] {
			return true
		}
		parent := filepath.ToSlash(filepath.Dir(p))
		if parent == p || p == filepath.ToSlash(camRoot) || len(parent) <= len(filepath.ToSlash(camRoot)) {
			return false
		}
		p = parent
	}
}
