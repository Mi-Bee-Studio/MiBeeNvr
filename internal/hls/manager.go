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
	writeBufSize      = 120 // buffered frames per stream (~6s at 20fps)
)

// hlsFrame is an async write request for the HLS muxer.
type hlsFrame struct {
	pts int64
	au  [][]byte
}

// streamEntry holds a per-camera HLS muxer and its metadata.
type streamEntry struct {
	mux      *gohlslib.Muxer
	track    *gohlslib.Track
	dirPath  string
	lastUsed time.Time
	cancel   context.CancelFunc
	frameCh  chan hlsFrame // async write buffer
	isH265   bool
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
	return m.startStream(cameraID, false, sps, pps, nil)
}

// StartStreamH265 creates and starts an HLS muxer for an H265 camera.
func (m *Manager) StartStreamH265(cameraID string, vps, sps, pps []byte) error {
	return m.startStream(cameraID, true, sps, pps, vps)
}

func (m *Manager) startStream(cameraID string, isH265 bool, sps, pps, vps []byte) error {
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

	// Already active — just update lastUsed
	if entry, ok := m.streams[cameraID]; ok {
		entry.lastUsed = time.Now()
		return nil
	}

	// Create per-camera directory
	dirPath := filepath.Join(m.dataDir, cameraID)
	if err := os.MkdirAll(dirPath, 0755); err != nil {
		return err
	}

	var track *gohlslib.Track
	var mux *gohlslib.Muxer

	if isH265 {
		track = &gohlslib.Track{
			Codec:     &codecs.H265{VPS: vps, SPS: sps, PPS: pps},
			ClockRate: 90000,
		}
		mux = &gohlslib.Muxer{
			Tracks:             []*gohlslib.Track{track},
			Variant:            gohlslib.MuxerVariantFMP4,
			SegmentCount:       3,
			SegmentMinDuration: 2 * time.Second,
			SegmentMaxSize:     50 * 1024 * 1024,
			Directory:          dirPath,
		}
	} else {
		track = &gohlslib.Track{
			Codec:     &codecs.H264{SPS: sps, PPS: pps},
			ClockRate: 90000,
		}
		mux = &gohlslib.Muxer{
			Tracks:             []*gohlslib.Track{track},
			Variant:            gohlslib.MuxerVariantMPEGTS,
			SegmentCount:       3,
			SegmentMinDuration: 2 * time.Second,
			SegmentMaxSize:     50 * 1024 * 1024,
			Directory:          dirPath,
		}
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
		frameCh:  make(chan hlsFrame, writeBufSize),
		isH265:   isH265,
	}
	m.streams[cameraID] = entry

	// Start async writer goroutine for this stream
	go m.writeLoop(ctx, cameraID, entry)

	// Start idle watchdog
	go m.idleWatchdog(ctx, cameraID)

	codecStr := "H264"
	if isH265 {
		codecStr = "H265"
	}
	hlsLogger.Info("HLS stream started", "camera_id", cameraID, "codec", codecStr)
	return nil
}

// writeLoop drains frames from the async buffer and writes them to the muxer.
// This ensures RTP receive path is never blocked by HLS disk I/O.
func (m *Manager) writeLoop(ctx context.Context, cameraID string, entry *streamEntry) {
	for {
		select {
		case <-ctx.Done():
			return
		case frame := <-entry.frameCh:
			var err error
			if entry.isH265 {
				err = entry.mux.WriteH265(entry.track, time.Now(), frame.pts, frame.au)
			} else {
				err = entry.mux.WriteH264(entry.track, time.Now(), frame.pts, frame.au)
			}
			if err != nil {
				hlsLogger.Error("HLS write error", "camera_id", cameraID, "error", err)
			}
		}
	}
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

// WriteH264 queues an H264 access unit for async writing to the HLS stream.
// This is non-blocking — it acquires a read lock only briefly and never blocks on disk I/O.
// If the write buffer is full, the frame is silently dropped to protect the recording pipeline.
func (m *Manager) WriteH264(cameraID string, pts int64, au [][]byte) error {
	return m.writeFrame(cameraID, pts, au)
}

// WriteH265 queues an H265 access unit for async writing to the HLS stream.
// Same non-blocking semantics as WriteH264.
func (m *Manager) WriteH265(cameraID string, pts int64, au [][]byte) error {
	return m.writeFrame(cameraID, pts, au)
}

func (m *Manager) writeFrame(cameraID string, pts int64, au [][]byte) error {
	m.mu.RLock()
	entry, ok := m.streams[cameraID]
	m.mu.RUnlock()

	if !ok {
		return nil // stream not active, silently ignore
	}

	entry.lastUsed = time.Now()

	// Non-blocking send — drop frame if buffer full to protect recording pipeline
	select {
	case entry.frameCh <- hlsFrame{pts: pts, au: au}:
	default:
		// Buffer full, drop frame. Live view tolerates dropped frames.
	}

	return nil
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
