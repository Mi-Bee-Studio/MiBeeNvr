package camera

import (
	"sync"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/streamhub"
)

// This file implements the copy-on-write snapshot registry that replaces the
// coarse-grained cm.mu (sync.RWMutex) as the primary concurrency mechanism for
// CameraManager state.
//
// Design goals (see refactor/lock-free-camera-manager plan):
//  1. Reads (GetRecorder, GetHub, Status, statusSnapshot, counts) NEVER block
//     — they load an immutable snapshot via atomic.Pointer.Load and read it
//     without any lock. A snapshot is consistent: its recorders/hubs/configs
//     maps are always mutually aligned.
//  2. Writes (add/remove/update a recorder or hub, mark start-failed) are
//     funneled through apply(), which takes cm.configMu, copies the current
//     snapshot's maps, mutates the copy, and atomically publishes it. The copy
//     is shallow — recorder/hub pointers are shared (their immutability is
//     guaranteed by the lifecycle actor: a recorder in the map is never mutated
//     in place; it is replaced). Map copies at N≈10–50 cameras are nanoseconds.
//  3. Slow lifecycle operations (rec.Start/Stop, network dials, ONVIF
//     handshakes) run OUTSIDE any lock, serialized per-camera by the actor
//     (actor.go). apply() is only ever called for the instant map swap, never
//     around I/O.

// snapshot is an immutable point-in-time view of the camera registry. Once
// published via cm.snapshot.Store, it is never mutated — apply() builds a fresh
// snapshot on every write. All fields are maps of shared pointers; the values
// themselves (Recorder/StreamHub/CameraConfig) are treated as read-only by
// consumers of a snapshot. Mutating a CameraConfig in place is NOT allowed;
// callers must go through apply() to publish a new pointer.
type snapshot struct {
	recorders    map[string]model.Recorder       // camera_id → running recorder (nil slot = not running)
	hubs         map[string]*streamhub.StreamHub // camera_id → StreamHub (single source of truth for pull+push)
	configs      map[string]*config.CameraConfig // camera_id → config pointer (into cm.cfg.Cameras or a copy)
	failedStarts map[string]error                // camera_id → last start failure (surfaced as StatusError to health)
}

// newSnapshot builds a fresh empty snapshot.
func newSnapshot() *snapshot {
	return &snapshot{
		recorders:    make(map[string]model.Recorder),
		hubs:         make(map[string]*streamhub.StreamHub),
		configs:      make(map[string]*config.CameraConfig),
		failedStarts: make(map[string]error),
	}
}

// clone returns a shallow copy of s suitable for mutation under apply(). The
// map headers are copied (O(len)); the pointed-to values are shared.
func (s *snapshot) clone() *snapshot {
	c := &snapshot{
		recorders:    make(map[string]model.Recorder, len(s.recorders)),
		hubs:         make(map[string]*streamhub.StreamHub, len(s.hubs)),
		configs:      make(map[string]*config.CameraConfig, len(s.configs)),
		failedStarts: make(map[string]error, len(s.failedStarts)),
	}
	for k, v := range s.recorders {
		c.recorders[k] = v
	}
	for k, v := range s.hubs {
		c.hubs[k] = v
	}
	for k, v := range s.configs {
		c.configs[k] = v
	}
	for k, v := range s.failedStarts {
		c.failedStarts[k] = v
	}
	return c
}

// loadSnapshot returns the current immutable registry snapshot for lock-free
// reads. The returned pointer must be treated as read-only.
func (cm *CameraManager) loadSnapshot() *snapshot {
	// atomic.Pointer.Load never returns nil after NewCameraManager initializes
	// it (the zero-value pointer is replaced in the constructor). If this is
	// ever nil it indicates a use-before-NewCameraManager bug, so we fall back
	// to an empty snapshot rather than panic on a nil deref.
	if s := cm.snapshot.Load(); s != nil {
		return s
	}
	return newSnapshot()
}

// apply is the single write path for registry state. It takes a mutator
// function that receives a fresh mutable clone of the current snapshot and
// returns the snapshot to publish (usually the same clone, mutated in place).
// apply holds cm.configMu only for the clone + mutate + store — no I/O, no
// network, no rec.Start/Stop. Lifecycle callers snapshot under apply, publish,
// then perform the slow I/O outside.
//
// Example:
//
//	cm.apply(func(s *snapshot) *snapshot {
//	    s.recorders[id] = rec
//	    return s
//	})
func (cm *CameraManager) apply(fn func(s *snapshot) *snapshot) {
	cm.configMu.Lock()
	defer cm.configMu.Unlock()
	cur := cm.loadSnapshot()
	next := fn(cur.clone())
	cm.snapshot.Store(next)
}

// cameraIndexInConfig returns the index of cameraID in cm.cfg.Cameras, or -1.
// Caller must hold cm.configMu (the cfg.Cameras slice is only mutated under it).
func (cm *CameraManager) cameraIndexInConfig(cameraID string) int {
	for i := range cm.cfg.Cameras {
		if cm.cfg.Cameras[i].ID == cameraID {
			return i
		}
	}
	return -1
}

// reseedSnapshotConfigs rebuilds the snapshot's configs map from cm.cfg.Cameras
// under configMu. This is a test-only escape hatch for tests that mutate
// cm.cfg.Cameras directly (raw append/remove) instead of going through
// AddCamera/RemoveCamera — those raw mutations bypass mutateCameras and leave
// the snapshot's configs map stale. Production code must use AddCamera /
// RemoveCamera / UpdateCamera, which republish correctly.
func (cm *CameraManager) reseedSnapshotConfigs() {
	cm.configMu.Lock()
	cur := cm.loadSnapshot().clone()
	cur.configs = make(map[string]*config.CameraConfig, len(cm.cfg.Cameras))
	for i := range cm.cfg.Cameras {
		cur.configs[cm.cfg.Cameras[i].ID] = &cm.cfg.Cameras[i]
	}
	cm.snapshot.Store(cur)
	cm.configMu.Unlock()
}

// --- Convenience accessors over the snapshot (all lock-free reads) ---

// snapshotRecorder returns the recorder for cameraID, or nil.
func (cm *CameraManager) snapshotRecorder(cameraID string) model.Recorder {
	return cm.loadSnapshot().recorders[cameraID]
}

// snapshotHub returns the StreamHub for cameraID, or nil.
func (cm *CameraManager) snapshotHub(cameraID string) *streamhub.StreamHub {
	return cm.loadSnapshot().hubs[cameraID]
}

// snapshotHubs returns a copy of the camera_id → hub map for iteration
// (stats flusher, flow-path API). Copy avoids holding the snapshot while
// iterating.
func (cm *CameraManager) snapshotHubs() map[string]*streamhub.StreamHub {
	s := cm.loadSnapshot()
	hubs := make(map[string]*streamhub.StreamHub, len(s.hubs))
	for id, hub := range s.hubs {
		hubs[id] = hub
	}
	return hubs
}

// Hubs is the public read-only view of all registered StreamHubs, used by the
// /api/streams flow-path endpoint (#469).
func (cm *CameraManager) Hubs() map[string]*streamhub.StreamHub {
	return cm.snapshotHubs()
}

// snapshotConfig returns the CameraConfig pointer for cameraID, or nil.
func (cm *CameraManager) snapshotConfig(cameraID string) *config.CameraConfig {
	return cm.loadSnapshot().configs[cameraID]
}

// withCameraLifecycle serializes lifecycle operations (start/stop/restart) for
// a single camera. Without it, two goroutines can race to startRecorder the
// same camera simultaneously — e.g. a manual API restart overlapping a health
// auto-remediation restart — and the second call overwrites the first recorder
// in the snapshot, leaking it (its run goroutine keeps running, unreachable via
// the manager, until process exit). The guard is a per-camera mutex lazily
// created in a sync.Map; it is held only around the fn, never across reads.
//
// This is a simpler alternative to a full per-camera actor model: it provides
// the critical serialization (no concurrent recorder construction for one
// camera) without the message-passing machinery. fn runs OUTSIDE any registry
// lock, so it may perform network I/O (rec.Start/Stop) without blocking other
// cameras or readers.
func (cm *CameraManager) withCameraLifecycle(cameraID string, fn func() error) error {
	muIface, _ := cm.lifecycleMu.LoadOrStore(cameraID, &sync.Mutex{})
	mu := muIface.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()
	return fn()
}
