package flv

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model/nalutil"
)

var flvLogger = slog.Default().With("component", "flv-manager")

const (
	defaultMaxViewers   = 10
	defaultWriteBufSize = 100
)

// gopCache stores the last GOP (keyframe + following delta frames)
// for instant playback start when a new client connects.
type gopCache struct {
	frames    []cachedFrame
	seqHeader []byte // cached sequence header tag
}

type cachedFrame struct {
	tag        []byte
	isKeyframe bool
	pts        int64
}

// streamEntry holds per-camera FLV streaming state.
type streamEntry struct {
	codec     model.Format
	sps       []byte
	pps       []byte
	vps       []byte // H.265 only
	seqHeader []byte // pre-built sequence header tag
	gopCache  *gopCache
	gopMu     sync.RWMutex
	viewers   map[int64]*viewerConn
	viewerSeq atomic.Int64
	viewerMu  sync.Mutex
	frameCh   chan model.FrameMsg
	cancel    context.CancelFunc
	hub       *model.StreamHub
	hubSubID  string
}


// viewerConn represents a connected FLV client.
type viewerConn struct {
	id      int64
	w       http.ResponseWriter
	flusher http.Flusher
	ctx     context.Context
	ch      chan []byte
	done    chan struct{}
}

// Manager manages HTTP-FLV streams with per-camera stream entries.
type Manager struct {
	mu           sync.RWMutex
	streams      map[string]*streamEntry
	maxViewers   int
	writeBufSize int
	metrics      *metrics.Metrics
}

// Option configures a Manager.
type Option func(*Manager)

// WithMaxViewers sets the maximum concurrent viewers per stream.
func WithMaxViewers(n int) Option {
	return func(m *Manager) {
		if n > 0 {
			m.maxViewers = n
		}
	}
}

// withWriteBufSize sets the per-stream write buffer size.
func withWriteBufSize(n int) Option {
	return func(m *Manager) {
		if n > 0 {
			m.writeBufSize = n
		}
	}
}

// WithMetrics sets the Prometheus metrics collector for the FLV manager.
func WithMetrics(m *metrics.Metrics) Option {
	return func(mgr *Manager) {
		mgr.metrics = m
	}
}

// NewManager creates a new FLV Manager.
func NewManager(opts ...Option) *Manager {
	m := &Manager{
		streams:      make(map[string]*streamEntry),
		maxViewers:   defaultMaxViewers,
		writeBufSize: defaultWriteBufSize,
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// RegisterStream registers a camera stream for FLV output.
// The recorder's StreamHub is used to receive live frames.
func (m *Manager) RegisterStream(camID string, codec model.Format, sps, pps, vps []byte, hub *model.StreamHub) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.streams[camID]; ok {
		return ErrStreamExists
	}

	var seqHeader []byte
	switch codec {
	case model.FormatH265:
		seqHeader = h265SequenceHeader(vps, sps, pps)
	default:
		seqHeader = h264SequenceHeader(sps, pps)
	}

	ctx, cancel := context.WithCancel(context.Background())
	entry := &streamEntry{
		codec:     codec,
		sps:       sps,
		pps:       pps,
		vps:       vps,
		seqHeader: seqHeader,
		gopCache:  &gopCache{},
		viewers:   make(map[int64]*viewerConn),
		frameCh:   make(chan model.FrameMsg, m.writeBufSize),
		cancel:    cancel,
		hub:       hub,
	}

	// Subscribe to recorder's StreamHub for live frames
	if hub != nil {
		hubSubID := "flv-" + camID
		entry.hubSubID = hubSubID
		_ = hub.Subscribe(hubSubID, func(pts int64, au [][]byte) {
			m.writeFrame(camID, pts, au)
		})
	}

	m.streams[camID] = entry
	go m.writeLoop(ctx, camID, entry)

	flvLogger.Info("FLV stream registered", "camera_id", camID, "codec", string(codec), "hub", hub != nil)
	return nil
}

// unregisterStream removes a camera stream and disconnects all viewers.
func (m *Manager) unregisterStream(camID string) {
	m.mu.Lock()
	entry, ok := m.streams[camID]
	if ok {
		delete(m.streams, camID)
		if m.metrics != nil {
			m.metrics.FLVActiveStreams.DeleteLabelValues(camID)
		}
	}
	m.mu.Unlock()

	if ok {
		// Unsubscribe from recorder's StreamHub
		if entry.hub != nil && entry.hubSubID != "" {
			entry.hub.Unsubscribe(entry.hubSubID)
		}
		entry.cancel()
		entry.viewerMu.Lock()
		for _, v := range entry.viewers {
			close(v.ch)
		}
		entry.viewerMu.Unlock()
		flvLogger.Info("FLV stream unregistered", "camera_id", camID)
	}
}

// IsActive returns whether a stream is registered.
func (m *Manager) IsActive(camID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.streams[camID]
	return ok
}

// viewerCount returns the number of active viewers for a stream.
func (m *Manager) viewerCount(camID string) int {
	m.mu.RLock()
	entry, ok := m.streams[camID]
	m.mu.RUnlock()
	if !ok {
		return 0
	}
	entry.viewerMu.Lock()
	defer entry.viewerMu.Unlock()
	return len(entry.viewers)
}

// writeH264 queues an H.264 access unit for FLV output. Non-blocking.
func (m *Manager) writeH264(camID string, pts int64, au [][]byte) {
	m.writeFrame(camID, pts, au)
}

// writeH265 queues an H.265 access unit for FLV output. Non-blocking.
func (m *Manager) writeH265(camID string, pts int64, au [][]byte) {
	m.writeFrame(camID, pts, au)
}

func (m *Manager) writeFrame(camID string, pts int64, au [][]byte) {
	if len(au) == 0 {
		return
	}

	m.mu.RLock()
	entry, ok := m.streams[camID]
	m.mu.RUnlock()

	if !ok {
		return // stream not active, silently ignore
	}

	isKeyframe := isKeyframeNALU(au[0], entry.codec == model.FormatH265)

	traceID := "no-trace"
	if isKeyframe {
		traceID = fmt.Sprintf("%s-%d", camID, pts)
	}

	// Non-blocking send
	select {
	case entry.frameCh <- model.FrameMsg{PTS: pts, AU: au, IsKeyframe: isKeyframe}:
		slog.Debug("frame_trace",
			"trace_id", traceID,
			"camera_id", camID,
			"stage", "flv_recv",
			"is_idr", isKeyframe,
		)
	default:
		slog.Debug("frame_trace",
			"trace_id", traceID,
			"camera_id", camID,
			"stage", "flv_drop",
			"is_idr", isKeyframe,
			"queue_depth", len(entry.frameCh),
		)
	}
}

// isKeyframeNALU checks if the first NALU is an IDR frame.
func isKeyframeNALU(nalu []byte, isH265 bool) bool {
	return nalutil.IsKeyframeNALU(nalu, isH265)
}

// writeLoop drains frames from the channel and distributes to all viewers.
func (m *Manager) writeLoop(ctx context.Context, camID string, entry *streamEntry) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-entry.frameCh:
			tag := videoFrameTag(entry.codec, msg.AU, msg.PTS, msg.IsKeyframe)

			// Update GOP cache on keyframe
			if msg.IsKeyframe {
				entry.gopMu.Lock()
				entry.gopCache.frames = entry.gopCache.frames[:0]
				entry.gopCache.seqHeader = entry.seqHeader
				entry.gopCache.frames = append(entry.gopCache.frames, cachedFrame{
					tag:        tag,
					isKeyframe: true,
					pts:        msg.PTS,
				})
				entry.gopMu.Unlock()
			} else {
				entry.gopMu.Lock()
				if len(entry.gopCache.frames) > 0 {
					entry.gopCache.frames = append(entry.gopCache.frames, cachedFrame{
						tag:        tag,
						isKeyframe: false,
						pts:        msg.PTS,
					})
				}
				entry.gopMu.Unlock()
			}

			// Distribute to all viewers (non-blocking per viewer)
			entry.viewerMu.Lock()
			for _, v := range entry.viewers {
				select {
				case v.ch <- tag:
					if m.metrics != nil {
						m.metrics.FLVFramesSent.WithLabelValues(camID).Inc()
					}
				default:
					// Slow client — drop frame
					if m.metrics != nil {
						m.metrics.FLVFramesDropped.WithLabelValues(camID).Inc()
					}
				}
			}
			entry.viewerMu.Unlock()
		}
	}
}

// ServeFLV handles an HTTP request for FLV live streaming.
// It writes the FLV header, sequence header, cached GOP, then live frames
// until the client disconnects.
func (m *Manager) ServeFLV(camID string, w http.ResponseWriter, r *http.Request) error {
	m.mu.RLock()
	entry, ok := m.streams[camID]
	m.mu.RUnlock()

	if !ok {
		return ErrStreamNotActive
	}

	// Check viewer limit
	entry.viewerMu.Lock()
	if len(entry.viewers) >= m.maxViewers {
		entry.viewerMu.Unlock()
		return ErrMaxViewers
	}

	flusher, canFlush := w.(http.Flusher)
	if !canFlush {
		flvLogger.Warn("response writer does not support flush")
	}

	// Set response headers
	w.Header().Set("Content-Type", "video/x-flv")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Transfer-Encoding", "chunked")
	w.WriteHeader(http.StatusOK)

	// Write FLV header + PreviousTagSize0
	if _, err := w.Write(flvHeader()); err != nil {
		return err
	}
	if _, err := w.Write(previousTagSize0()); err != nil {
		return err
	}
	if flusher != nil {
		flusher.Flush()
	}

	// Write sequence header
	if _, err := w.Write(entry.seqHeader); err != nil {
		return err
	}
	if flusher != nil {
		flusher.Flush()
	}

	// Send cached GOP
	entry.gopMu.RLock()
	gopLen := len(entry.gopCache.frames)
	for _, frame := range entry.gopCache.frames {
		if _, err := w.Write(frame.tag); err != nil {
			entry.gopMu.RUnlock()
			return err
		}
	}
	entry.gopMu.RUnlock()
	if m.metrics != nil {
		if gopLen > 0 {
			m.metrics.FLVGOPCacheHits.WithLabelValues(camID).Inc()
		} else {
			m.metrics.FLVGOPCacheMisses.WithLabelValues(camID).Inc()
		}
	}
	if flusher != nil {
		flusher.Flush()
	}

	// Register viewer
	viewerID := entry.viewerSeq.Add(1)
	viewerCh := make(chan []byte, m.writeBufSize)
	viewer := &viewerConn{
		id:      viewerID,
		w:       w,
		flusher: flusher,
		ctx:     r.Context(),
		ch:      viewerCh,
		done:    make(chan struct{}),
	}
	entry.viewers[viewerID] = viewer
	if m.metrics != nil {
		m.metrics.FLVActiveStreams.WithLabelValues(camID).Set(float64(len(entry.viewers)))
	}
	entry.viewerMu.Unlock()

	// Cleanup on exit
	defer func() {
		entry.viewerMu.Lock()
		if m.metrics != nil {
			m.metrics.FLVActiveStreams.WithLabelValues(camID).Set(float64(len(entry.viewers) - 1))
		}
		delete(entry.viewers, viewerID)
		close(viewer.done)
		entry.viewerMu.Unlock()

		flvLogger.Debug("FLV viewer disconnected", "camera_id", camID, "viewer_id", viewerID)
	}()

	flvLogger.Debug("FLV viewer connected", "camera_id", camID, "viewer_id", viewerID)

	// Write frames to client until disconnect
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case tag, ok := <-viewerCh:
			if !ok {
				return nil // channel closed
			}
			if _, err := w.Write(tag); err != nil {
				return err
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
	}
}

// stopAll stops all active FLV streams.
func (m *Manager) stopAll() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.streams))
	for id := range m.streams {
		ids = append(ids, id)
	}
	m.mu.Unlock()

	for _, id := range ids {
		m.unregisterStream(id)
	}
}

// Ensure time package is used
var _ time.Duration
