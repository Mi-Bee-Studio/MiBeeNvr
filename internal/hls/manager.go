package hls

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/bluenviron/gohlslib/v2"
	"github.com/bluenviron/gohlslib/v2/pkg/codecs"
)

var hlsLogger = slog.Default().With("component", "hls-manager")

const (
	defaultIdleTimeout = 60 * time.Second
	defaultMaxStreams  = 4
)

// streamEntry holds a per-camera HLS muxer and its metadata.
type streamEntry struct {
	mux      *gohlslib.Muxer
	track    *gohlslib.Track
	dirPath  string
	lastUsed time.Time
	cancel   context.CancelFunc
}

// Manager manages on-demand HLS streams for cameras.
type Manager struct {
	mu          sync.RWMutex
	streams     map[string]*streamEntry // cameraID -> entry
	dataDir     string
	idleTimeout time.Duration
	maxStreams  int
}

// NewManager creates a new HLS Manager. dataDir is the root directory for HLS segment storage.
func NewManager(dataDir string) *Manager {
	return &Manager{
		streams:     make(map[string]*streamEntry),
		dataDir:     dataDir,
		idleTimeout: defaultIdleTimeout,
		maxStreams:  defaultMaxStreams,
	}
}

// StartStream creates and starts an HLS muxer for the given camera.
// The caller must provide the H264 SPS and PPS NAL units (without start bytes).
func (m *Manager) StartStream(cameraID string, sps, pps []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check max streams
	// Evict oldest stream if at capacity
	if len(m.streams) >= m.maxStreams {
		var oldestID string
		var oldestTime time.Time
		for id, entry := range m.streams {
			if oldestID == "" || entry.lastUsed.Before(oldestTime) {
				oldestID = id
				oldestTime = entry.lastUsed
			}
		}
		if oldestID != "" {
			hlsLogger.Info("evicting oldest HLS stream for new request", "evicted_id", oldestID, "new_id", cameraID)
			m.stopStreamLocked(oldestID)
		}
	}

	// Already active
	if entry, ok := m.streams[cameraID]; ok {
		entry.lastUsed = time.Now()
		return nil
	}

	// Create per-camera directory
	dirPath := filepath.Join(m.dataDir, cameraID)
	if err := os.MkdirAll(dirPath, 0755); err != nil {
	return err
	}

	track := &gohlslib.Track{
		Codec:     &codecs.H264{SPS: sps, PPS: pps},
		ClockRate: 90000,
	}

	mux := &gohlslib.Muxer{
		Tracks:             []*gohlslib.Track{track},
		Variant:            gohlslib.MuxerVariantMPEGTS,
		SegmentCount:       3,
		SegmentMinDuration: 2 * time.Second,
		SegmentMaxSize:     50 * 1024 * 1024,
		Directory:          dirPath,
	}

	if err := mux.Start(); err != nil {
		os.RemoveAll(dirPath)
	return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	entry := &streamEntry{
		mux:      mux,
		track:    track,
		dirPath:  dirPath,
		lastUsed: time.Now(),
		cancel:   cancel,
	}
	m.streams[cameraID] = entry

	// Start idle watchdog
	go m.idleWatchdog(ctx, cameraID)

	hlsLogger.Info("HLS stream started", "camera_id", cameraID)
	return nil
}

// StartStreamH265 creates and starts an HLS muxer for an H265 camera.
func (m *Manager) StartStreamH265(cameraID string, vps, sps, pps []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Evict oldest stream if at capacity
	if len(m.streams) >= m.maxStreams {
		var oldestID string
		var oldestTime time.Time
		for id, entry := range m.streams {
			if oldestID == "" || entry.lastUsed.Before(oldestTime) {
				oldestID = id
				oldestTime = entry.lastUsed
			}
		}
		if oldestID != "" {
			hlsLogger.Info("evicting oldest HLS stream for new request", "evicted_id", oldestID, "new_id", cameraID)
			m.stopStreamLocked(oldestID)
		}
	}

	if entry, ok := m.streams[cameraID]; ok {
		entry.lastUsed = time.Now()
		return nil
	}

	dirPath := filepath.Join(m.dataDir, cameraID)
	if err := os.MkdirAll(dirPath, 0755); err != nil {
		return err
	}

	track := &gohlslib.Track{
		Codec:     &codecs.H265{VPS: vps, SPS: sps, PPS: pps},
		ClockRate: 90000,
	}

	mux := &gohlslib.Muxer{
		Tracks:             []*gohlslib.Track{track},
		Variant:            gohlslib.MuxerVariantFMP4,
		SegmentCount:       3,
		SegmentMinDuration: 2 * time.Second,
		SegmentMaxSize:     50 * 1024 * 1024,
		Directory:          dirPath,
	}

	if err := mux.Start(); err != nil {
		os.RemoveAll(dirPath)
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	entry := &streamEntry{
		mux:      mux,
		track:    track,
		dirPath:  dirPath,
		lastUsed: time.Now(),
		cancel:   cancel,
	}
	m.streams[cameraID] = entry

	go m.idleWatchdog(ctx, cameraID)

	hlsLogger.Info("HLS H265 stream started", "camera_id", cameraID)
	return nil
}

// StopStream stops the HLS muxer for the given camera and cleans up temp files.
func (m *Manager) StopStream(cameraID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopStreamLocked(cameraID)
}

// stopStreamLocked stops a stream. Caller must hold m.mu write lock.
func (m *Manager) stopStreamLocked(cameraID string) {
	entry, ok := m.streams[cameraID]
	if !ok {
		return
	}

	entry.cancel()
	entry.mux.Close()

	// Clean up segment directory
	os.RemoveAll(entry.dirPath)

	delete(m.streams, cameraID)
	hlsLogger.Info("HLS stream stopped", "camera_id", cameraID)
}

// WriteH264 writes an H264 access unit to the active stream for the given camera.
// This is safe to call from the RTP receive path — it acquires a read lock only briefly.
func (m *Manager) WriteH264(cameraID string, pts int64, au [][]byte) error {
	m.mu.RLock()
	entry, ok := m.streams[cameraID]
	m.mu.RUnlock()

	if !ok {
		return nil // stream not active, silently ignore
	}

	entry.lastUsed = time.Now()
	return entry.mux.WriteH264(entry.track, time.Now(), pts, au)
}

// WriteH265 writes an H265 access unit to the active stream for the given camera.
func (m *Manager) WriteH265(cameraID string, pts int64, au [][]byte) error {
	m.mu.RLock()
	entry, ok := m.streams[cameraID]
	m.mu.RUnlock()

	if !ok {
		return nil
	}

	entry.lastUsed = time.Now()
	return entry.mux.WriteH265(entry.track, time.Now(), pts, au)
}

// IsActive returns true if an HLS stream is active for the given camera.
func (m *Manager) IsActive(cameraID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.streams[cameraID]
	return ok
}

// Handle proxies an HTTP request to the HLS muxer for the given camera.
// Returns false if the stream is not active.
func (m *Manager) Handle(cameraID string, w http.ResponseWriter, r *http.Request) bool {
	m.mu.RLock()
	entry, ok := m.streams[cameraID]
	m.mu.RUnlock()

	if !ok {
		return false
	}

	entry.lastUsed = time.Now()
	entry.mux.Handle(w, r)
	return true
}

// StopAll stops all active HLS streams.
func (m *Manager) StopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id := range m.streams {
		m.stopStreamLocked(id)
	}
}

func (m *Manager) idleWatchdog(ctx context.Context, cameraID string) {
	ticker := time.NewTicker(m.idleTimeout / 2)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.mu.RLock()
			entry, ok := m.streams[cameraID]
			m.mu.RUnlock()

			if !ok {
				return
			}
			if time.Since(entry.lastUsed) > m.idleTimeout {
				hlsLogger.Info("HLS stream idle timeout, stopping", "camera_id", cameraID)
				m.StopStream(cameraID)
				return
			}
		}
	}
}
