package storage

	import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
)

// Manager handles file system storage for camera recordings.
// It provides atomic writes via a .tmp → rename pattern.
type Manager struct {
	rootDir string
	metrics *metrics.Metrics
	mu      sync.Mutex
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
	if err := os.MkdirAll(rootDir, 0755); err != nil {
		return nil, fmt.Errorf("storage: failed to create root directory %q: %w", rootDir, err)
	}
	return &Manager{rootDir: rootDir, metrics: m}, nil
}

// RootDir returns the root directory path.
func (m *Manager) RootDir() string {
	return m.rootDir
}

// EnsureCameraDir creates the directory for a camera if it doesn't exist.
func (m *Manager) EnsureCameraDir(cameraID string) error {
	dir := filepath.Join(m.rootDir, cameraID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("storage: failed to create camera dir %q: %w", dir, err)
	}
	return nil
}

// CreateSegment creates a new recording segment.
// For format "h264": creates a .tmp file for writing MP4 data.
// For format "mjpeg": creates a .tmp directory for writing JPEG frames.
// Returns the temp path (for writing) and the suggested final path (for CloseSegment).
func (m *Manager) CreateSegment(cameraID string, format string) (tempPath string, finalPath string, err error) {
	if err := m.EnsureCameraDir(cameraID); err != nil {
		return "", "", err
	}

	cameraDir := filepath.Join(m.rootDir, cameraID)
	now := time.Now().Format("20060102_150405")
	uuid := fmt.Sprintf("%d", time.Now().UnixNano())

	switch strings.ToLower(format) {
	case "h264", "h265":
		tempPath = filepath.Join(cameraDir, uuid+".tmp")
		finalPath = filepath.Join(cameraDir, fmt.Sprintf("%s_%s_%s.mp4", cameraID, now, uuid))
		f, err := os.Create(tempPath)
		if err != nil {
			return "", "", fmt.Errorf("storage: failed to create temp file: %w", err)
		}
		f.Close()

	case "mjpeg":
		tempPath = filepath.Join(cameraDir, uuid+".tmp")
	finalPath = filepath.Join(cameraDir, fmt.Sprintf("%s_%s_%s", cameraID, now, uuid))

		if err := os.MkdirAll(tempPath, 0755); err != nil {
			return "", "", fmt.Errorf("storage: failed to create temp dir: %w", err)
		}

	default:
		return "", "", fmt.Errorf("storage: unsupported format %q", format)
	}

	return tempPath, finalPath, nil
}

// CloseSegment atomically finalizes a segment by syncing and renaming .tmp to final path.
func (m *Manager) CloseSegment(tempPath, finalPath string) error {
	// Check if temp is a directory (MJPEG) or file (H.264)
	info, err := os.Stat(tempPath)
	if err != nil {
		return fmt.Errorf("storage: temp path not found: %w", err)
	}

	if info.IsDir() {
		// Sync the directory for MJPEG
		dirFd, err := os.Open(tempPath)
		if err != nil {
			return fmt.Errorf("storage: cannot open temp dir for sync: %w", err)
		}
		if err := dirFd.Sync(); err != nil {
			dirFd.Close()
			return fmt.Errorf("storage: failed to sync temp dir: %w", err)
		}
		dirFd.Close()

		// Atomic rename of directory
		if err := os.Rename(tempPath, finalPath); err != nil {
			return fmt.Errorf("storage: failed to rename temp dir to final: %w", err)
		}
	} else {
		// Sync and close the file for H.264
		f, err := os.OpenFile(tempPath, os.O_WRONLY, 0)
		if err != nil {
			return fmt.Errorf("storage: cannot open temp file for sync: %w", err)
		}
		if err := f.Sync(); err != nil {
			f.Close()
			return fmt.Errorf("storage: failed to sync temp file: %w", err)
		}
		f.Close()

		// Atomic rename
		if err := os.Rename(tempPath, finalPath); err != nil {
			return fmt.Errorf("storage: failed to rename temp file to final: %w", err)
		}
	}

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
		return 0, fmt.Errorf("storage: temp path not accessible: %w", err)
	}

	if info.IsDir() {
		// MJPEG: write individual JPEG file with timestamp name
		ts := time.Now().Format("20060102_150405.000")
		jpgPath := filepath.Join(tempPath, ts+".jpg")
		return 0, func() error {
			if err := os.WriteFile(jpgPath, data, 0644); err != nil {
				return fmt.Errorf("storage: failed to write JPEG frame: %w", err)
			}
			return nil
		}()
	}

	// H.264: append to temp file
	f, err := os.OpenFile(tempPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return 0, fmt.Errorf("storage: failed to open temp file for writing: %w", err)
	}
	defer f.Close()

	n, err := f.Write(data)
	if err != nil {
		return n, fmt.Errorf("storage: write failed: %w", err)
	}
	return n, nil
}

// ListFiles lists all recording files (non-.tmp) for a camera.
func (m *Manager) ListFiles(cameraID string) ([]string, error) {
	cameraDir := filepath.Join(m.rootDir, cameraID)

	entries, err := os.ReadDir(cameraDir)
	if err != nil {
		return nil, fmt.Errorf("storage: cannot read camera dir %q: %w", cameraDir, err)
	}

	var files []string
	for _, entry := range entries {
		name := entry.Name()
		// Skip temp files and hidden files
		if strings.HasSuffix(name, ".tmp") || strings.HasPrefix(name, ".") {
			continue
		}
		files = append(files, filepath.Join(cameraDir, name))
	}
	return files, nil
}

// GetFileSize returns the size of a file in bytes.
func (m *Manager) GetFileSize(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, fmt.Errorf("storage: cannot stat %q: %w", path, err)
	}
	return info.Size(), nil
}

// DeleteFile removes a file from disk.
func (m *Manager) DeleteFile(path string) error {
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("storage: failed to delete %q: %w", path, err)
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
