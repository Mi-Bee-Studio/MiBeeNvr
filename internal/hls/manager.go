package hls

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/bluenviron/gohlslib/v2"
	"github.com/bluenviron/gohlslib/v2/pkg/codecs"
	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/base"
	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/bluenviron/gortsplib/v5/pkg/format/rtph264"
	"github.com/bluenviron/gortsplib/v5/pkg/format/rtph265"
	"github.com/pion/rtp"
)

var hlsLogger = slog.Default().With("component", "hls-manager")

const (
	defaultIdleTimeout  = 60 * time.Second
	defaultMaxStreams   = 4
	defaultWriteBufSize = 40  // buffered frames per stream (~2s at 20fps)
	defaultSegmentMaxSize = 10 * 1024 * 1024 // 10MB HLS segment max
)

// hlsFrame is an async write request for the HLS muxer.
type hlsFrame struct {
	pts int64
	au  [][]byte
}

// streamEntry holds a per-camera HLS muxer and its metadata.
type streamEntry struct {
	mux             *gohlslib.Muxer
	track           *gohlslib.Track
	dirPath         string
	lastUsed        time.Time
	cancel          context.CancelFunc
	frameCh         chan hlsFrame // async write buffer
	isH265          bool
	subStreamCancel context.CancelFunc // cancels the sub-stream RTSP reader goroutine
	maxFPS          int
	lastFrameTime time.Time
}

// Manager manages on-demand HLS streams for cameras.
type Manager struct {
	mu              sync.RWMutex
	streams         map[string]*streamEntry // cameraID -> entry
	dataDir         string
	idleTimeout     time.Duration
	maxStreams       int
	writeBufSize    int
	segmentMaxSize  int
}

// NewManager creates a new HLS Manager with default settings.
// Use NewManagerWithOpts for custom buffer/segment sizes.
func NewManager(dataDir string) *Manager {
	return &Manager{
		streams:        make(map[string]*streamEntry),
		dataDir:        dataDir,
		idleTimeout:    defaultIdleTimeout,
		maxStreams:      defaultMaxStreams,
		writeBufSize:   defaultWriteBufSize,
		segmentMaxSize: defaultSegmentMaxSize,
	}
}

// NewManagerWithOpts creates a new HLS Manager with custom buffer and segment sizes.
// writeBufSize controls the async frame buffer per stream (default: 40).
// segmentMaxSize controls the maximum HLS segment file size in bytes (default: 10MB).
func NewManagerWithOpts(dataDir string, writeBufSize, segmentMaxSize int) *Manager {
	if writeBufSize <= 0 {
		writeBufSize = defaultWriteBufSize
	}
	if segmentMaxSize <= 0 {
		segmentMaxSize = defaultSegmentMaxSize
	}
	return &Manager{
		streams:        make(map[string]*streamEntry),
		dataDir:        dataDir,
		idleTimeout:    defaultIdleTimeout,
		maxStreams:      defaultMaxStreams,
		writeBufSize:   writeBufSize,
		segmentMaxSize: segmentMaxSize,
	}
}

// StartStream creates and starts an HLS muxer for the given camera.
// The caller must provide the H264 SPS and PPS NAL units (without start bytes).
func (m *Manager) StartStream(cameraID string, sps, pps []byte, maxFPS int) error {
	return m.startStream(cameraID, false, sps, pps, nil, maxFPS)
}

// StartStreamH265 creates and starts an HLS muxer for an H265 camera.
func (m *Manager) StartStreamH265(cameraID string, vps, sps, pps []byte, maxFPS int) error {
	return m.startStream(cameraID, true, sps, pps, vps, maxFPS)
}

func (m *Manager) startStream(cameraID string, isH265 bool, sps, pps, vps []byte, maxFPS int) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// At capacity — return error instead of silently evicting
	if len(m.streams) >= m.maxStreams {
		return ErrMaxStreamsReached
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
			SegmentMaxSize:     uint64(m.segmentMaxSize),
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
			SegmentMaxSize:     uint64(m.segmentMaxSize),
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
		frameCh:  make(chan hlsFrame, m.writeBufSize),
		isH265:   isH265,
		maxFPS:   maxFPS,
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

// StartSubStreamReader starts a separate RTSP connection to a sub-stream URL for HLS.
// It connects to subStreamURL, extracts codec parameters (SPS/PPS for H264, VPS/SPS/PPS for H265),
// and feeds frames to the HLS muxer for the given camera.
// If the sub-stream connection fails, it logs a warning and returns — the caller should fall back to main stream.
func (m *Manager) StartSubStreamReader(cameraID, subStreamURL string, isH265 bool) error {
	m.mu.RLock()
	entry, ok := m.streams[cameraID]
	m.mu.RUnlock()

	if !ok {
		return ErrStreamNotActive
	}
	if entry.subStreamCancel != nil {
		return nil // already running
	}

	ctx, cancel := context.WithCancel(context.Background())
	entry.subStreamCancel = cancel

	go m.readSubStream(ctx, cameraID, subStreamURL, isH265, entry)

	hlsLogger.Info("HLS sub-stream reader started", "camera_id", cameraID, "sub_stream_url", subStreamURL)
	return nil
}

func (m *Manager) readSubStream(ctx context.Context, cameraID, rtspURL string, isH265 bool, entry *streamEntry) {
	var err error
	defer func() {
		m.mu.Lock()
		if e, ok := m.streams[cameraID]; ok {
			e.subStreamCancel = nil
		}
		m.mu.Unlock()
		if err != nil && ctx.Err() == nil {
			hlsLogger.Warn("HLS sub-stream reader exited, falling back to main stream", "camera_id", cameraID, "error", err)
		}
	}()

	u, parseErr := base.ParseURL(rtspURL)
	if parseErr != nil {
		err = fmt.Errorf("invalid sub-stream RTSP URL: %w", parseErr)
		return
	}

	tcp := gortsplib.ProtocolTCP
	client := &gortsplib.Client{
		Scheme:   u.Scheme,
		Host:     u.Host,
		Protocol: &tcp,
	}

	if dialErr := client.Start(); dialErr != nil {
		err = fmt.Errorf("sub-stream client start: %w", dialErr)
		return
	}
	defer client.Close()

	desc, _, descErr := client.Describe(u)
	if descErr != nil {
		err = fmt.Errorf("sub-stream DESCRIBE: %w", descErr)
		return
	}

	if isH265 {
		err = m.readSubStreamH265(ctx, client, desc, cameraID, entry)
	} else {
		err = m.readSubStreamH264(ctx, client, desc, cameraID, entry)
	}
}

func (m *Manager) readSubStreamH264(ctx context.Context, client *gortsplib.Client, desc *description.Session, cameraID string, entry *streamEntry) error {
	var forma *format.H264
	medi := desc.FindFormat(&forma)
	if medi == nil {
		return fmt.Errorf("H264 media not found in sub-stream")
	}

	rtpDec, err := forma.CreateDecoder()
	if err != nil {
		return fmt.Errorf("sub-stream create RTP decoder: %w", err)
	}

	if _, err := client.Setup(desc.BaseURL, medi, 0, 0); err != nil {
		return fmt.Errorf("sub-stream SETUP: %w", err)
	}

	errCh := make(chan error, 1)

	client.OnPacketRTP(medi, forma, func(pkt *rtp.Packet) {
		select {
		case <-ctx.Done():
			return
		default:
		}

		aus, decErr := rtpDec.Decode(pkt)
		if decErr != nil {
			if decErr != rtph264.ErrNonStartingPacketAndNoPrevious && decErr != rtph264.ErrMorePacketsNeeded {
				hlsLogger.Warn("sub-stream RTP decode error", "camera_id", cameraID, "error", decErr)
			}
			return
		}
		_ = m.WriteH264(cameraID, int64(pkt.Timestamp), aus)
	})

	if _, playErr := client.Play(nil); playErr != nil {
		return fmt.Errorf("sub-stream PLAY: %w", playErr)
	}

	go func() { errCh <- client.Wait() }()

	select {
	case <-ctx.Done():
		client.Close()
		return nil
	case err = <-errCh:
		return err
	}
}

func (m *Manager) readSubStreamH265(ctx context.Context, client *gortsplib.Client, desc *description.Session, cameraID string, entry *streamEntry) error {
	var forma *format.H265
	medi := desc.FindFormat(&forma)
	if medi == nil {
		return fmt.Errorf("H265 media not found in sub-stream")
	}

	rtpDec, err := forma.CreateDecoder()
	if err != nil {
		return fmt.Errorf("sub-stream create RTP decoder: %w", err)
	}

	if _, err := client.Setup(desc.BaseURL, medi, 0, 0); err != nil {
		return fmt.Errorf("sub-stream SETUP: %w", err)
	}

	errCh := make(chan error, 1)

	client.OnPacketRTP(medi, forma, func(pkt *rtp.Packet) {
		select {
		case <-ctx.Done():
			return
		default:
		}

		aus, decErr := rtpDec.Decode(pkt)
		if decErr != nil {
			if decErr != rtph265.ErrNonStartingPacketAndNoPrevious && decErr != rtph265.ErrMorePacketsNeeded {
				hlsLogger.Warn("sub-stream RTP decode error", "camera_id", cameraID, "error", decErr)
			}
			return
		}
		_ = m.WriteH265(cameraID, int64(pkt.Timestamp), aus)
	})

	if _, playErr := client.Play(nil); playErr != nil {
		return fmt.Errorf("sub-stream PLAY: %w", playErr)
	}

	go func() { errCh <- client.Wait() }()

	select {
	case <-ctx.Done():
		client.Close()
		return nil
	case err = <-errCh:
		return err
	}
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

// EvictStream stops and removes an active HLS stream, freeing a slot.
// Returns ErrStreamNotActive if the stream is not running.
func (m *Manager) EvictStream(cameraID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.streams[cameraID]; !ok {
		return ErrStreamNotActive
	}
	m.stopStreamLocked(cameraID)
	return nil
}

// GetActiveStreamCount returns the number of currently active HLS streams.
func (m *Manager) GetActiveStreamCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.streams)
}

// stopStreamLocked stops a stream. Caller must hold m.mu write lock.
func (m *Manager) stopStreamLocked(cameraID string) {
	entry, ok := m.streams[cameraID]
	if !ok {
		return
	}

	entry.cancel()
	if entry.subStreamCancel != nil {
		entry.subStreamCancel()
		entry.subStreamCancel = nil
	}
	if entry.mux != nil {
		entry.mux.Close()
	}

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

	// Frame rate limiting for live preview bandwidth optimization
	if entry.maxFPS > 0 {
		minInterval := time.Second / time.Duration(entry.maxFPS)
		if time.Since(entry.lastFrameTime) < minInterval {
			return nil // drop frame to stay within target FPS
		}
		entry.lastFrameTime = time.Now()
	}

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

// GetStreamStatus returns whether a stream is active for the given camera.
// Returns (active, nil) — use IsActive() for simple boolean check.
// This method is designed for API responses that include stream metadata.
func (m *Manager) GetStreamStatus(cameraID string) (active bool) {
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
