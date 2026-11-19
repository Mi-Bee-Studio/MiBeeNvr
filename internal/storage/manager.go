package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
)

// Manager handles file system storage for camera recordings.
// It provides atomic writes via a .tmp → rename pattern.
type Manager struct {
	// rootDir is hot-swappable (storage switch/migration, no restart): reads
	// go through currentRoot(), writes through SetRootDir. cameraRoots holds
	// per-camera overrides (RootFor); nil disables per-camera routing.
	rootMu      sync.RWMutex
	rootDir     string
	cameraRoots map[string]string
	metrics     *metrics.Metrics

	// Per-segment writer states (#750), keyed by temp path: classification,
	// camera attribution, retained FD + coalescing buffer for the append path.
	// Each state carries its own lock; cameras never serialize through one
	// another.
	statesMu      sync.RWMutex
	segmentStates map[string]*segmentWriter

	// durabilityRelaxed gates RAW-segment fsync (#760): false (default) =
	// strict — every finalize syncs. true = relaxed: raw segments skip the
	// proactive fsync (rename atomicity unchanged); merged/timelapse
	// products always sync regardless (CloseSegmentMerged).
	durabilityRelaxed atomic.Bool

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
		segmentStates:    make(map[string]*segmentWriter),
	}, nil
}

// RootDir returns the root directory path.
func (m *Manager) RootDir() string {
	return m.currentRoot()
}

// currentRoot reads the hot-swappable recording root.
func (m *Manager) currentRoot() string {
	m.rootMu.RLock()
	defer m.rootMu.RUnlock()
	return m.rootDir
}

// SetRootDir atomically switches where NEW recording segments are written
// (storage switch/migration without restart). In-flight segments keep their
// absolute paths and finish where they started. The directory is created if
// missing; on error the previous root stays in effect.
func (m *Manager) SetRootDir(dir string) error {
	if dir == "" {
		return fmt.Errorf("storage: root directory path must not be empty")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("storage: failed to create root directory %q: %w", dir, err)
	}
	m.rootMu.Lock()
	m.rootDir = dir
	m.rootMu.Unlock()
	return nil
}

// SetCameraRoot points one camera's NEW segments at a different root ("" =
// back to the default). Takes effect at the next segment rotation.
func (m *Manager) SetCameraRoot(cameraID, root string) {
	m.rootMu.Lock()
	if m.cameraRoots == nil {
		m.cameraRoots = make(map[string]string)
	}
	if root == "" {
		delete(m.cameraRoots, cameraID)
	} else {
		m.cameraRoots[cameraID] = root
	}
	m.rootMu.Unlock()
}

// CameraRoot returns the explicit override for a camera ("" = none).
func (m *Manager) CameraRoot(cameraID string) string {
	m.rootMu.RLock()
	defer m.rootMu.RUnlock()
	return m.cameraRoots[cameraID]
}

// RootFor resolves the recording root for a camera: the per-camera override
// when it still exists on disk, else the default root. Directory access is
// stat-cached per call (segment-granularity — a few calls per minute per
// camera), so a vanished override mount degrades that camera to the default
// instead of failing every segment write.
func (m *Manager) RootFor(cameraID string) string {
	m.rootMu.RLock()
	override := m.cameraRoots[cameraID]
	def := m.rootDir
	m.rootMu.RUnlock()
	if override != "" && override != def {
		if _, err := os.Stat(override); err == nil {
			return override
		}
		logger.Warn("camera storage override unavailable — recording to the default root",
			"camera_id", cameraID, "override", override)
	}
	return def
}

// Roots returns the distinct active roots (default + existing overrides).
func (m *Manager) Roots() []string {
	m.rootMu.RLock()
	def := m.rootDir
	overrides := make([]string, 0, len(m.cameraRoots))
	for _, r := range m.cameraRoots {
		overrides = append(overrides, r)
	}
	m.rootMu.RUnlock()
	seen := map[string]bool{}
	var roots []string
	for _, r := range append([]string{def}, overrides...) {
		if r == "" || seen[r] {
			continue
		}
		if _, err := os.Stat(r); err != nil {
			continue
		}
		seen[r] = true
		roots = append(roots, r)
	}
	return roots
}

// GetRootUsage returns total/free bytes of the filesystem containing root —
// the capacity gate for per-camera migrations.
func (m *Manager) GetRootUsage(root string) (total, free int64, err error) {
	total, free, err = statfsFree(root)
	if err != nil {
		return 0, 0, fmt.Errorf("storage: failed to stat %q: %w", root, err)
	}
	return total, free, nil
}

// EnsureCameraDir creates the directory for a camera if it doesn't exist.
func (m *Manager) EnsureCameraDir(cameraID string) error {
	dir := filepath.Join(m.RootFor(cameraID), cameraID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("storage: failed to create camera dir %q: %w", dir, err)
	}
	return nil
}
