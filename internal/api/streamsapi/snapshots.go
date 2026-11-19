package streamsapi

import (
	"sync"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/slogx"
)

var snapshotLogger = slogx.Component("streamsapi")

// SnapshotCapturer captures one JPEG for a camera — satisfied by
// *snapshot.Capturer (FFmpeg-gated decode + device snapshot-URL fallback,
// #657). Interface keeps the handlers testable with a stub.
type SnapshotCapturer interface {
	Capture(cameraID string) ([]byte, error)
}

// snapshotCache is one cached JPEG still.
type snapshotCache struct {
	data      []byte
	timestamp time.Time
}

// LatestFrameCacheTTL bounds how long a decoded snapshot serves repeated
// latest-frame polls before re-capturing. Var so tests can manipulate expiry.
var LatestFrameCacheTTL = 10 * time.Second

// SnapshotStore owns the shared JPEG-still TTL cache used by the snapshot,
// latest-frame and HLS stale-serve paths. Extracted from the api Handler so
// the streaming domain owns its state; the API root keeps a pointer and wires
// the capturer through SetSnapshotCapturer.
type SnapshotStore struct {
	mu       sync.RWMutex
	entries  map[string]*snapshotCache // cameraID -> cached snapshot
	capturer SnapshotCapturer
}

// NewSnapshotStore creates an empty store.
func NewSnapshotStore() *SnapshotStore {
	return &SnapshotStore{entries: make(map[string]*snapshotCache)}
}

// SetCapturer wires the FFmpeg-gated snapshot capturer.
func (st *SnapshotStore) SetCapturer(c SnapshotCapturer) {
	st.mu.Lock()
	st.capturer = c
	st.mu.Unlock()
}

// HasCapturer reports whether the snapshot capturer is wired.
func (st *SnapshotStore) HasCapturer() bool {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return st.capturer != nil
}

// get returns the raw cache entry (caller decides staleness).
func (st *SnapshotStore) get(cameraID string) (*snapshotCache, bool) {
	st.mu.RLock()
	defer st.mu.RUnlock()
	c, ok := st.entries[cameraID]
	return c, ok
}

// put stores a freshly captured still.
func (st *SnapshotStore) put(cameraID string, data []byte) {
	st.mu.Lock()
	st.entries[cameraID] = &snapshotCache{data: data, timestamp: time.Now()}
	st.mu.Unlock()
}

// Seed installs an arbitrary entry — test hook (white-box tests pre-seed or
// backdate entries to exercise TTL/stale-serve behavior).
func (st *SnapshotStore) Seed(cameraID string, data []byte, ts time.Time) {
	st.mu.Lock()
	st.entries[cameraID] = &snapshotCache{data: data, timestamp: ts}
	st.mu.Unlock()
}

// Backdate pushes the existing entry's timestamp back by d — test hook for
// the stale-fallback paths. No-op when no entry exists.
func (st *SnapshotStore) Backdate(cameraID string, d time.Duration) {
	st.mu.Lock()
	if c, ok := st.entries[cameraID]; ok {
		st.entries[cameraID] = &snapshotCache{data: c.data, timestamp: c.timestamp.Add(d)}
	}
	st.mu.Unlock()
}

// CaptureLatest returns the latest JPEG frame for a camera: the capture path
// behind GET /latest-frame for H.264/H.265 cameras (and cameras with only a
// device snapshot URL). Uses the capturer with the shared TTL cache (#657);
// any failure degrades to ok=false — the endpoint answers 404, never 500
// (FFmpeg stays optional).
func (st *SnapshotStore) CaptureLatest(cameraID string) ([]byte, bool) {
	if cached, ok := st.get(cameraID); ok && time.Since(cached.timestamp) < LatestFrameCacheTTL {
		return cached.data, true
	}

	st.mu.RLock()
	capturer := st.capturer
	st.mu.RUnlock()
	if capturer == nil {
		return nil, false
	}

	frame, err := capturer.Capture(cameraID)
	if err != nil || len(frame) == 0 {
		snapshotLogger.Debug("latest-frame capture unavailable", "camera_id", cameraID, "error", err)
		return nil, false
	}
	st.put(cameraID, frame)
	return frame, true
}
