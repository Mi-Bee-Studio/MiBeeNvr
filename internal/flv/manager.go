package flv

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/frametrace"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model/nalutil"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/streamhub"
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
	hub       *streamhub.StreamHub
	hubSubID  string
	// clockBase is the unix-ms wallclock this entry's FLV tag StreamID
	// deltas are measured from (#481). Fixed at registration so the shared
	// per-frame tag encodes a viewer-independent ingest offset.
	clockBase atomic.Int64
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
func (m *Manager) RegisterStream(camID string, codec model.Format, sps, pps, vps []byte, hub *streamhub.StreamHub) error {
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

	// Subscribe with the full FrameMsg so the hub-entry wallclock (IngestAt)
	// reaches the wire — the StreamID field of every video tag carries the
	// ingest offset from clockBase (#481, end-to-end latency for FLV).
	entry.clockBase.Store(time.Now().UnixMilli())
	if hub != nil {
		hubSubID := "flv-" + camID
		entry.hubSubID = hubSubID
		_ = hub.SubscribeMsg(hubSubID, func(msg model.FrameMsg) {
			m.writeFrameMsg(camID, msg)
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

// UnregisterStream is the exported teardown for a single stream — used by the
// sub-stream recycler (#513) to drop a camera's "/sub" entry when the on-demand
// puller is torn down. Mirrors wsstream.UnregisterStream.
func (m *Manager) UnregisterStream(camID string) { m.unregisterStream(camID) }

// ActiveHub returns the StreamHub the registered entry is currently
// subscribed to (nil when the stream is not active).
func (m *Manager) ActiveHub(camID string) *streamhub.StreamHub {
	m.mu.RLock()
	defer m.mu.RUnlock()
	entry, ok := m.streams[camID]
	if !ok {
		return nil
	}
	return entry.hub
}

// RebindHub re-subscribes an active stream to a new StreamHub. The sub-stream
// puller (#513) restarts with a FRESH hub after each idle recycle; without a
// rebind the entry keeps listening to the dead hub and every viewer goes
// black forever. Mirrors wsstream.Manager.RebindHub.
func (m *Manager) RebindHub(camID string, hub *streamhub.StreamHub) {
	if hub == nil {
		return
	}
	m.mu.Lock()
	entry, ok := m.streams[camID]
	if !ok {
		m.mu.Unlock()
		return
	}
	oldHub := entry.hub
	entry.hub = hub
	subID := entry.hubSubID
	m.mu.Unlock()

	if oldHub != nil && subID != "" {
		oldHub.Unsubscribe(subID)
	}
	if hub.SubscribeMsg(subID, func(msg model.FrameMsg) {
		m.writeFrameMsg(camID, msg)
	}) != nil {
		flvLogger.Warn("FLV rebind: hub subscribe failed", "camera_id", camID)
		return
	}
	flvLogger.Info("FLV stream rebound to new StreamHub", "camera_id", camID)
}

// IsActive returns whether a stream is registered.
func (m *Manager) IsActive(camID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.streams[camID]
	return ok
}

// ViewerCount returns the number of active FLV viewers for a stream (public —
// used by the /api/streams flow view, #469).
func (m *Manager) ViewerCount(camID string) int {
	return m.viewerCount(camID)
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
	m.writeFrameMsg(camID, model.FrameMsg{PTS: pts, AU: au})
}

// writeFrameMsg queues a full FrameMsg (carrying the hub-entry IngestAt
// wallclock for the latency piggyback, #481). Non-blocking.
func (m *Manager) writeFrameMsg(camID string, msg model.FrameMsg) {
	pts, au := msg.PTS, msg.AU
	if len(au) == 0 {
		return
	}

	m.mu.RLock()
	entry, ok := m.streams[camID]
	m.mu.RUnlock()

	if !ok {
		return // stream not active, silently ignore
	}

	// Scan the WHOLE AU, not just au[0]: ingest push paths (RTMP/SRT/WHIP)
	// prepend the cached SPS/PPS to IDR frames before broadcasting, so au[0]
	// is a parameter set, never the IDR itself — an au[0]-only check marked
	// every ingest keyframe as an inter frame (GOP cache never filled, FLV
	// viewers got a P-frame-only stream, players waited for a keyframe and
	// gave up; found on the fnOS live-push topology, 2026-08-19).
	isKeyframe := nalutil.IsIDR(au, entry.codec == model.FormatH265)

	traceID := "no-trace"
	if isKeyframe {
		traceID = fmt.Sprintf("%s-%d", camID, pts)
	}

	// Non-blocking send
	select {
	case entry.frameCh <- model.FrameMsg{PTS: pts, AU: au, IsKeyframe: isKeyframe, IngestAt: msg.IngestAt}:
		frametrace.Log(
			camID,
			"trace_id", traceID,
			"camera_id", camID,
			"stage", "flv_recv",
			"is_idr", isKeyframe,
		)
	default:
		frametrace.Log(
			camID,
			"trace_id", traceID,
			"camera_id", camID,
			"stage", "flv_drop",
			"is_idr", isKeyframe,
			"queue_depth", len(entry.frameCh),
		)
	}
}

// writeLoop drains frames from the channel and distributes to all viewers.
func (m *Manager) writeLoop(ctx context.Context, camID string, entry *streamEntry) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-entry.frameCh:
			// Ingest offset (ms from clockBase) rides in the tag StreamID
			// field — spec-receivers ignore it, our player reads it (#481).
			// 0xFFFFFF = unknown (no IngestAt / out of the 4.6h 3-byte range).
			var delta int64 = flvIngestUnknown
			if msg.IngestAt > 0 {
				if d := msg.IngestAt/1e6 - entry.clockBase.Load(); d >= 0 && d < flvIngestUnknown {
					delta = d
				}
			}
			tag := videoFrameTag(entry.codec, msg.AU, msg.PTS, msg.IsKeyframe, delta)

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

// ClockMs returns the entry's ingest-clock base (unix ms) that the tag
// StreamID deltas are measured from, or 0 when the stream is not active.
// The flow API surfaces it so players can turn a delta into a wallclock
// (#481).
func (m *Manager) ClockMs(camID string) int64 {
	m.mu.RLock()
	entry, ok := m.streams[camID]
	m.mu.RUnlock()
	if !ok {
		return 0
	}
	return entry.clockBase.Load()
}

// ServeFLV handles an HTTP request for FLV live streaming.
// It writes the FLV header, sequence header, cached GOP, then live frames
// until the client disconnects.
//
// Locking contract (#539): the ONLY work under entry.viewerMu is the viewer-
// limit check, the viewer registration and the GOP-cache snapshot — every
// w.Write happens OUTSIDE the lock, guarded by a write deadline. A stalled
// client (kernel socket buffer full) used to block mid-GOP-replay while
// holding viewerMu, wedging the camera's whole FLV pipeline: subsequent
// ServeFLV (limit check) and the writeLoop frame fan-out queue on the same
// mutex, and /api/streams (ViewerCount) hangs with them. One half-closed
// client was enough (found live on M5: probe read 4KB then closed).
func (m *Manager) ServeFLV(camID string, w http.ResponseWriter, r *http.Request) error {
	m.mu.RLock()
	entry, ok := m.streams[camID]
	m.mu.RUnlock()

	if !ok {
		return ErrStreamNotActive
	}

	flusher, canFlush := w.(http.Flusher)
	if !canFlush {
		flvLogger.Warn("response writer does not support flush")
	}
	// Per-write deadline so a slow/stalled client times out instead of
	// blocking this goroutine forever. Best-effort: middleware that wraps the
	// ResponseWriter without Unwrap degrades to no deadline (the lock-free
	// writes alone already prevent the global wedge).
	rc := http.NewResponseController(w)

	// Register the viewer (holds its slot) and snapshot the GOP cache under
	// the lock — everything below is lock-free.
	entry.viewerMu.Lock()
	if len(entry.viewers) >= m.maxViewers {
		entry.viewerMu.Unlock()
		return ErrMaxViewers
	}
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
	entry.gopMu.RLock()
	gopFrames := make([]cachedFrame, len(entry.gopCache.frames))
	copy(gopFrames, entry.gopCache.frames)
	entry.gopMu.RUnlock()
	entry.viewerMu.Unlock()

	// Cleanup on exit — also the path for every write error below.
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

	writeWithDeadline := func(p []byte) error {
		_ = rc.SetWriteDeadline(time.Now().Add(10 * time.Second))
		_, err := w.Write(p)
		return err
	}

	// Set response headers. X-Stream-Wallclock-Ms anchors the StreamID
	// ingest deltas for clients that read response headers (#481); the
	// flow API exposes the same value for the rest.
	w.Header().Set("X-Stream-Wallclock-Ms", strconv.FormatInt(entry.clockBase.Load(), 10))
	w.Header().Set("Content-Type", "video/x-flv")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Transfer-Encoding", "chunked")
	w.WriteHeader(http.StatusOK)

	// Write FLV header + PreviousTagSize0
	if err := writeWithDeadline(flvHeader()); err != nil {
		return err
	}
	if err := writeWithDeadline(previousTagSize0()); err != nil {
		return err
	}
	if flusher != nil {
		flusher.Flush()
	}

	// Write sequence header
	if err := writeWithDeadline(entry.seqHeader); err != nil {
		return err
	}
	if flusher != nil {
		flusher.Flush()
	}

	// Replay the cached GOP snapshot (lock-free)
	for _, frame := range gopFrames {
		if err := writeWithDeadline(frame.tag); err != nil {
			return err
		}
	}
	if m.metrics != nil {
		if len(gopFrames) > 0 {
			m.metrics.FLVGOPCacheHits.WithLabelValues(camID).Inc()
		} else {
			m.metrics.FLVGOPCacheMisses.WithLabelValues(camID).Inc()
		}
	}
	if flusher != nil {
		flusher.Flush()
	}

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
			if err := writeWithDeadline(tag); err != nil {
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
