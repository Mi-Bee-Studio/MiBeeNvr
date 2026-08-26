package wsstream

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/gorilla/websocket"
)

var wsLogger atomic.Pointer[slog.Logger]

func init() {
	wsLogger.Store(slog.Default().With("component", "ws-stream-manager"))
}

const (
	defaultMaxViewers   = 10
	defaultWriteBufSize = 100
	defaultIdleTimeout  = 60 * time.Second
)

// Errors returned by the Manager.
var (
	ErrStreamExists    = errors.New("wsstream: stream already registered")
	ErrStreamNotActive = errors.New("wsstream: stream not active")
	ErrMaxViewers      = errors.New("wsstream: max viewers reached")
)

// viewerConn represents a connected WebSocket client.
type viewerConn struct {
	id        int64
	conn      *websocket.Conn
	ch        chan []byte // encoded binary messages
	cancel    context.CancelFunc
	audioOnly bool // true = skip video frames, audio only
}

// streamEntry holds per-camera WebSocket streaming state.
type streamEntry struct {
	codec     model.Format
	sps       []byte
	pps       []byte
	vps       []byte
	viewers   map[int64]*viewerConn
	viewerSeq atomic.Int64
	viewerMu  sync.Mutex
	frameCh   chan model.FrameMsg
	cancel    context.CancelFunc
	hub       *model.StreamHub
	hubSubID  string
	dropCount atomic.Int64

	// Cached Prometheus counters (#469): resolved once at registration so the
	// per-frame path pays only an atomic Inc, never a WithLabelValues lookup.
	sentCounter prometheusCounter
	dropCounter prometheusCounter

	// Audio fields (zero-value = no audio)
	audioCodec      byte                  // wire format codec byte
	audioSampleRate uint32                // sample rate in Hz
	audioChannels   uint8                 // number of channels
	audioConfig     []byte                // codec config (AAC AASC; nil for G.711)
	audioCh         chan model.AudioFrame // audio frame channel, nil if no audio
	audioSubID      string                // StreamHub audio subscription ID
}

// prometheusCounter is the minimal counter interface (satisfied by
// prometheus.Counter) — keeps streamEntry free of a direct dependency and
// trivially nil-safe via the noop implementation.
type prometheusCounter interface {
	Inc()
}

type noopCounter struct{}

func (noopCounter) Inc() {}

// upgrader is the WebSocket upgrader used by ServeWS.
var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

// Manager manages WebSocket binary streams with per-camera stream entries.
// It subscribes to StreamHub for live frames and serves them over WebSocket
// connections as binary-encoded VideoFrame messages. CodecInfo is sent as
// the first message on each connection.
type Manager struct {
	mu           sync.RWMutex
	streams      map[string]*streamEntry
	maxViewers   int
	writeBufSize int
	idleTimeout  time.Duration
	metrics      *metrics.Metrics // optional (#469)
}

// Option configures a Manager.
type Option func(*Manager)

// WithMetrics wires Prometheus counters for active streams, frames sent and
// dropped (#469 — WS previously had zero metrics despite being the default
// live protocol candidate).
func WithMetrics(m *metrics.Metrics) Option {
	return func(mgr *Manager) {
		mgr.metrics = m
	}
}

// WithMaxViewers sets the maximum concurrent viewers per stream.
func WithMaxViewers(n int) Option {
	return func(m *Manager) {
		if n > 0 {
			m.maxViewers = n
		}
	}
}

// WithWriteBufSize sets the per-stream write buffer size.
func WithWriteBufSize(n int) Option {
	return func(m *Manager) {
		if n > 0 {
			m.writeBufSize = n
		}
	}
}

// WithIdleTimeout sets the idle timeout for WebSocket viewers.
func WithIdleTimeout(d time.Duration) Option {
	return func(m *Manager) {
		if d > 0 {
			m.idleTimeout = d
		}
	}
}

// NewManager creates a new WebSocket stream Manager.
func NewManager(opts ...Option) *Manager {
	m := &Manager{
		streams:      make(map[string]*streamEntry),
		maxViewers:   defaultMaxViewers,
		writeBufSize: defaultWriteBufSize,
		idleTimeout:  defaultIdleTimeout,
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// RegisterStream registers a camera stream for WebSocket output.
// The recorder's StreamHub is used to receive live frames.
func (m *Manager) RegisterStream(camID string, codec model.Format, sps, pps, vps []byte, hub *model.StreamHub) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.streams[camID]; ok {
		return ErrStreamExists
	}

	ctx, cancel := context.WithCancel(context.Background())
	entry := &streamEntry{
		codec:   codec,
		sps:     sps,
		pps:     pps,
		vps:     vps,
		viewers: make(map[int64]*viewerConn),
		frameCh: make(chan model.FrameMsg, m.writeBufSize),
		cancel:  cancel,
		hub:     hub,
		audioCh: nil, // lazily allocated in SetAudioInfo
	}
	// Resolve cached counters once; per-frame path only Inc()s (#469).
	if m.metrics != nil {
		entry.sentCounter = m.metrics.WSFramesSent.WithLabelValues(camID)
		entry.dropCounter = m.metrics.WSFramesDropped.WithLabelValues(camID)
		m.metrics.WSActiveStreams.WithLabelValues(camID).Inc()
	} else {
		entry.sentCounter = noopCounter{}
		entry.dropCounter = noopCounter{}
	}

	// Subscribe to recorder's StreamHub for live frames. SubscribeMsg (full
	// FrameMsg) so IngestAt wallclock reaches the wire for e2e latency (#469).
	if hub != nil {
		hubSubID := "ws-" + camID
		entry.hubSubID = hubSubID
		_ = hub.SubscribeMsg(hubSubID, func(msg model.FrameMsg) {
			m.writeFrameMsg(camID, msg)
		})
	}

	m.streams[camID] = entry
	// NOTE: entry is fully constructed before writeLoop starts. Do NOT mutate
	// entry fields after this point — writeLoop/getAudioCh read them without a
	// lock, and a post-launch write (e.g. re-zeroing audioCh) is a data race.
	go m.writeLoop(ctx, camID, entry)

	wsLogger.Load().Info("WebSocket stream registered", "camera_id", camID, "codec", string(codec), "hub", hub != nil)
	return nil
}

// UnregisterStream removes a camera stream and disconnects all viewers.
func (m *Manager) UnregisterStream(camID string) {
	m.mu.Lock()
	entry, ok := m.streams[camID]
	if ok {
		// Remove from map FIRST under the lock so writeFrame() lookups fail fast.
		delete(m.streams, camID)
	}
	m.mu.Unlock()

	if !ok {
		return
	}

	// Unsubscribe from StreamHub OUTSIDE m.mu to avoid lock-inversion deadlock:
	// hub.Unsubscribe() waits for the drain goroutine, which calls writeFrame(),
	// which needs m.mu.RLock(). If we held m.mu.Lock() here, the drain goroutine
	// would block on RLock while we wait for it to finish -> deadlock.
	// After the map deletion above, writeFrame's lookup (m.streams[camID]) returns
	// ok=false and exits early — safe to unsubscribe without the lock.
	if entry.hub != nil && entry.hubSubID != "" {
		entry.hub.Unsubscribe(entry.hubSubID)
	}
	if entry.hub != nil && entry.audioSubID != "" {
		entry.hub.UnsubscribeAudio(entry.audioSubID)
	}

	entry.cancel()
	entry.viewerMu.Lock()
	eosMsg := []byte{byte(MsgTypeEOS)}
	for _, v := range entry.viewers {
		// Signal EOS by enqueuing it onto the viewer's channel (best-effort,
		// non-blocking) rather than writing conn directly. Writing conn here would
		// race the ServeWS frame-loop writer on the same conn — gorilla/websocket
		// requires all writes to a conn to be serialized, and the frame loop is the
		// single designated writer. The frame loop drains remaining channel
		// messages (including this EOS) on its way out via viewerCtx cancellation.
		// We do NOT close v.ch: a close would race distributeVideoFrame's
		// `case v.ch <-` send (send-on-closed-channel panic). The channel is GC'd
		// once the ServeWS goroutine exits and drops its reference.
		sendNonBlocking(v.ch, eosMsg)
		v.cancel()
	}
	entry.viewerMu.Unlock()
	if m.metrics != nil {
		m.metrics.WSActiveStreams.WithLabelValues(camID).Dec()
	}
	wsLogger.Load().Info("WebSocket stream unregistered", "camera_id", camID)
	if cnt := entry.dropCount.Load(); cnt > 0 {
		wsLogger.Load().Info("stream drop count", "camera_id", camID, "total_drops", cnt)
	}
}

// sendNonBlocking attempts a non-blocking send of msg onto ch. If ch is full or
// closed, the send is skipped (best-effort). This is the safe way to deliver
// out-of-band messages (EOS, idle-timeout) to a viewer's writer goroutine
// without risking a panic on a closed channel or a blocked sender.
func sendNonBlocking(ch chan []byte, msg []byte) {
	select {
	case ch <- msg:
	default:
	}
}

// drainViewerCh flushes any pending messages buffered in ch by writing them to
// conn via the single-writer discipline (the caller IS the single writer). Used
// on viewer shutdown so an EOS enqueued by the idle watchdog or
// UnregisterStream is actually emitted before the conn closes, rather than
// being dropped when the ctx cancels. Non-blocking: stops as soon as the
// channel is empty or a write fails. A failed write is expected on shutdown.
func drainViewerCh(ch chan []byte, conn *websocket.Conn) {
	for {
		select {
		case data, ok := <-ch:
			if !ok {
				return
			}
			if err := conn.WriteMessage(websocket.BinaryMessage, data); err != nil {
				return
			}
		default:
			return
		}
	}
}

// IsActive returns whether a stream is registered.
func (m *Manager) IsActive(camID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.streams[camID]
	return ok
}

// ActiveHub returns the StreamHub the registered entry is currently
// subscribed to (nil when the stream is not active).
func (m *Manager) ActiveHub(camID string) *model.StreamHub {
	m.mu.RLock()
	defer m.mu.RUnlock()
	entry, ok := m.streams[camID]
	if !ok {
		return nil
	}
	return entry.hub
}

// RebindHub re-subscribes an active stream to a new StreamHub. Recorders that
// reconnect get a FRESH hub from the camera manager; without a rebind the
// entry keeps listening to the dead hub and every viewer goes black forever
// (observed on flaky MJPEG cameras whose recorder reconnects often).
func (m *Manager) RebindHub(camID string, hub *model.StreamHub) {
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

	if oldHub != nil {
		// Unsubscribe OUTSIDE m.mu (drain calls writeFrame → RLock).
		oldHub.Unsubscribe(subID)
	}
	_ = hub.SubscribeMsg(subID, func(msg model.FrameMsg) {
		m.writeFrameMsg(camID, msg)
	})
	wsLogger.Load().Info("WebSocket stream rebound to new StreamHub", "camera_id", camID)
}

// ViewerCount returns the number of active viewers for a stream (public —
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

// writeH264 queues an H.264 access unit for WebSocket output. Non-blocking.
func (m *Manager) writeH264(camID string, pts int64, au [][]byte) {
	m.writeFrameMsg(camID, model.FrameMsg{PTS: pts, AU: au, IngestAt: time.Now().UnixNano()})
}

// writeH265 queues an H.265 access unit for WebSocket output. Non-blocking.
func (m *Manager) writeH265(camID string, pts int64, au [][]byte) {
	m.writeFrameMsg(camID, model.FrameMsg{PTS: pts, AU: au, IngestAt: time.Now().UnixNano()})
}

// writeFrameMsg queues a full FrameMsg (carrying IngestAt) for WebSocket
// output. Non-blocking; counts sent/dropped via cached Prometheus counters.
func (m *Manager) writeFrameMsg(camID string, msg model.FrameMsg) {
	au := msg.AU
	if len(au) == 0 {
		return
	}
	pts := msg.PTS

	m.mu.RLock()
	entry, ok := m.streams[camID]
	m.mu.RUnlock()

	if !ok {
		return
	}

	isKeyframe := false
	if entry.codec == model.FormatMJPEG {
		// MJPEG: every frame is independently decodable (like an IDR).
		isKeyframe = true
	} else {
		// Scan EVERY NALU of the AU: the GB28181 recorder prepends SPS/PPS
		// to IDR access units (prepareBroadcastAU), so the IDR NAL is NOT
		// au[0] — checking only the first NALU marked those AUs non-keyframe
		// and downstream consumers (frontend keyframe waits, WebCodecs
		// configure) never saw a keyframe arrive on the WS path.
		for _, nalu := range au {
			if len(nalu) == 0 {
				continue
			}
			var naluType int
			if entry.codec == model.FormatH265 {
				// H.265: forbidden(1) | nal_unit_type(6) | ...
				naluType = int((nalu[0] >> 1) & 0x3F)
			} else {
				// H.264: forbidden(1) | nal_ref_idc(2) | nal_unit_type(5)
				naluType = int(nalu[0] & 0x1F)
			}
			// H.264 IDR = 5, H.265 IDR_W_RADL = 19, IDR_N_LP = 20
			if naluType == 5 || naluType == 19 || naluType == 20 {
				isKeyframe = true
				break
			}
		}
	}

	// Non-blocking send
	traceID := "no-trace"
	if isKeyframe {
		traceID = fmt.Sprintf("%s-%d", camID, pts)
	}
	select {
	case entry.frameCh <- model.FrameMsg{PTS: pts, AU: au, IsKeyframe: isKeyframe, IngestAt: msg.IngestAt}:
		entry.sentCounter.Inc()
		slog.Debug(
			"frame_trace",
			"trace_id", traceID,
			"camera_id", camID,
			"stage", "ws_recv",
			"is_idr", isKeyframe,
		)
	default:
		// Buffer full, drop frame
		cnt := entry.dropCount.Add(1)
		entry.dropCounter.Inc()
		slog.Debug(
			"frame_trace",
			"trace_id", traceID,
			"camera_id", camID,
			"stage", "ws_drop",
			"is_idr", isKeyframe,
			"total_drops", cnt,
		)
		if cnt%100 == 0 {
			wsLogger.Load().Warn("frames dropped", "camera_id", camID, "total_drops", cnt)
		}
	}
}

// SetAudioInfo configures audio streaming for a registered stream.
// Must be called after RegisterStream. Audio frames will be forwarded
// from the StreamHub to WebSocket viewers alongside video frames.
// For G.711 codecs, muLaw specifies μ-law (true) vs A-law (false).
// config carries codec-specific setup bytes forwarded to clients in the
// AudioCodecInfo message: for AAC this is the AudioSpecificConfig (required
// by browser-side WebCodecs AudioDecoder); for G.711 it is nil.
func (m *Manager) SetAudioInfo(camID string, codec string, muLaw bool, sampleRate int, channels int, config []byte) error {
	m.mu.RLock()
	entry, ok := m.streams[camID]
	m.mu.RUnlock()
	if !ok {
		return ErrStreamNotActive
	}

	// Map model codec string to wire format byte
	var codecByte byte
	switch codec {
	case "aac":
		codecByte = AudioCodecAAC
	case "g711":
		if muLaw {
			codecByte = AudioCodecG711Mu
		} else {
			codecByte = AudioCodecG711A
		}
	case "opus":
		codecByte = AudioCodecOpus
	default:
		return fmt.Errorf("wsstream: unknown audio codec %q", codec)
	}

	entry.audioCodec = codecByte
	entry.audioSampleRate = uint32(sampleRate)
	entry.audioChannels = uint8(channels)
	// Deep-copy config so the caller can't mutate what viewers receive.
	if config != nil {
		entry.audioConfig = append([]byte(nil), config...)
	} else {
		entry.audioConfig = nil
	}

	// Lazily allocate audio channel
	if entry.audioCh == nil {
		entry.audioCh = make(chan model.AudioFrame, m.writeBufSize)
	}

	// Subscribe to hub audio with callback that feeds into audioCh
	if entry.hub != nil {
		audioSubID := "ws-audio-" + camID
		entry.audioSubID = audioSubID
		cb := func(pts int64, audioCodec model.AudioCodec, data []byte) {
			// Non-blocking send to audio channel
			select {
			case entry.audioCh <- model.AudioFrame{PTS: pts, Codec: audioCodec, Data: data}:
			default:
				// Audio frame dropped (buffer full)
			}
		}
		if err := entry.hub.SubscribeAudio(audioSubID, cb); err != nil {
			return fmt.Errorf("wsstream: subscribe audio: %w", err)
		}
	}

	wsLogger.Load().Info(
		"WebSocket audio configured",
		"camera_id", camID,
		"codec", codec,
		"sample_rate", sampleRate,
		"channels", channels,
		"config_len", len(config),
	)
	return nil
}

// getAudioCh returns the audio channel, handling nil (no audio).
// Used in writeLoop to avoid blocking on a nil channel.
func (m *Manager) getAudioCh(entry *streamEntry) chan model.AudioFrame {
	return entry.audioCh
}

// distributeVideoFrame encodes and distributes a video frame to all viewers.
func (m *Manager) distributeVideoFrame(entry *streamEntry, camID string, msg model.FrameMsg) {
	encoded, err := EncodeVideoFrame(&VideoFrame{
		PTS:        msg.PTS,
		IsKeyframe: msg.IsKeyframe,
		NALUs:      msg.AU,
		IngestAt:   msg.IngestAt / 1e6, // unix nano → ms on the wire
	})
	if err != nil {
		wsLogger.Load().Warn("WebSocket encode frame error", "camera_id", camID, "error", err)
		return
	}

	// Distribute to all viewers (non-blocking per viewer)
	entry.viewerMu.Lock()
	for _, v := range entry.viewers {
		if v.audioOnly {
			continue // audio-only viewers don't receive video
		}
		select {
		case v.ch <- encoded:
		default:
			// Slow client — drop frame
			cnt := entry.dropCount.Add(1)
			entry.dropCounter.Inc()
			if cnt%100 == 0 {
				wsLogger.Load().Warn("frames dropped", "camera_id", camID, "total_drops", cnt)
			}
		}
	}
	entry.viewerMu.Unlock()
}

// distributeAudioFrame encodes and distributes an audio frame to all viewers.
func (m *Manager) distributeAudioFrame(entry *streamEntry, camID string, af model.AudioFrame) {
	encoded, err := EncodeAudioFrame(&AudioFrameData{
		PTS:   af.PTS,
		Codec: entry.audioCodec,
		Data:  af.Data,
	})
	if err != nil {
		wsLogger.Load().Warn("WebSocket encode audio frame error", "camera_id", camID, "error", err)
		return
	}

	// Distribute to all viewers (non-blocking per viewer)
	entry.viewerMu.Lock()
	for _, v := range entry.viewers {
		select {
		case v.ch <- encoded:
		default:
			// Slow client — drop audio frame silently
		}
	}
	entry.viewerMu.Unlock()
}

// writeLoop drains frames from the channel and distributes to all viewers.
func (m *Manager) writeLoop(ctx context.Context, camID string, entry *streamEntry) {
	defer func() {
		if r := recover(); r != nil {
			wsLogger.Load().Warn("WebSocket writeLoop panic recovered", "camera_id", camID, "error", r)
		}
	}()

	// Only select on audioCh if audio is configured
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-entry.frameCh:
			m.distributeVideoFrame(entry, camID, msg)
		case af := <-m.getAudioCh(entry):
			m.distributeAudioFrame(entry, camID, af)
		}
	}
}

// ServeWS handles a WebSocket upgrade request for a camera stream.
// On connect, it sends CodecInfo as the first message, then streams
// VideoFrame messages as they arrive from the StreamHub.
// When quality is non-empty ("main"/"sub"), a QualityInfo message is sent
// BEFORE CodecInfo so clients can detect sub→main fallback — the 101
// upgrade response cannot carry X-Stream-Quality (#541). Clients that don't
// know the message type ignore it.
func (m *Manager) ServeWS(camID, quality string, w http.ResponseWriter, r *http.Request) error {
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
	entry.viewerMu.Unlock()

	// Upgrade HTTP to WebSocket
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return err
	}

	// Build and send CodecInfo as first message
	codecStr := string(entry.codec)
	profile := byte(0)
	level := byte(0)
	if len(entry.sps) > 1 {
		profile = entry.sps[1]
	}
	if len(entry.sps) > 3 {
		level = entry.sps[3]
	}

	ci := &CodecInfo{
		Codec:   codecStr,
		Profile: profile,
		Level:   level,
		SPS:     entry.sps,
		PPS:     entry.pps,
		VPS:     entry.vps,
	}

	// Check for audio-only mode (?audio_only=1 skips video frames)
	audioOnly := r.URL.Query().Get("audio_only") == "1"

	// QualityInfo first (#541): the WS 101 upgrade response discards headers
	// set before Upgrade(), so X-Stream-Quality can't reach WS clients — send
	// the negotiated quality in-band instead. Sent even to audio-only viewers;
	// skipped entirely when the caller passes an empty quality (no negotiation).
	if quality != "" {
		qiData, err := EncodeQualityInfo(&QualityInfo{Quality: quality})
		if err != nil {
			conn.Close()
			return err
		}
		if err := conn.WriteMessage(websocket.BinaryMessage, qiData); err != nil {
			conn.Close()
			return err
		}
	}

	// Send CodecInfo as first message (skip for audio-only viewers)
	if !audioOnly {
		ciData, err := EncodeCodecInfo(ci)
		if err != nil {
			conn.Close()
			return err
		}
		if err := conn.WriteMessage(websocket.BinaryMessage, ciData); err != nil {
			conn.Close()
			return err
		}
	}

	// Send AudioCodecInfo if audio is configured
	if entry.audioCodec != 0 {
		aci := &AudioCodecInfo{
			Codec:      entry.audioCodec,
			SampleRate: entry.audioSampleRate,
			Channels:   entry.audioChannels,
			Config:     entry.audioConfig,
		}
		aciData, err := EncodeAudioCodecInfo(aci)
		if err != nil {
			conn.Close()
			return err
		}
		if err := conn.WriteMessage(websocket.BinaryMessage, aciData); err != nil {
			conn.Close()
			return err
		}
	}

	// Register viewer
	viewerCtx, viewerCancel := context.WithCancel(r.Context())
	viewerID := entry.viewerSeq.Add(1)
	viewerCh := make(chan []byte, m.writeBufSize)
	viewer := &viewerConn{
		id:        viewerID,
		conn:      conn,
		ch:        viewerCh,
		cancel:    viewerCancel,
		audioOnly: audioOnly,
	}

	entry.viewerMu.Lock()
	entry.viewers[viewerID] = viewer
	entry.viewerMu.Unlock()

	wsLogger.Load().Debug("WebSocket viewer connected", "camera_id", camID, "viewer_id", viewerID)

	// Cleanup on exit
	defer func() {
		viewerCancel()
		entry.viewerMu.Lock()
		delete(entry.viewers, viewerID)
		entry.viewerMu.Unlock()
		_ = conn.Close()
		wsLogger.Load().Debug("WebSocket viewer disconnected", "camera_id", camID, "viewer_id", viewerID)
	}()

	// Start read pump to detect client disconnect. The read deadline is set to
	// 2× the idle timeout so the idle watchdog (which fires at idleTimeout and
	// enqueues EOS via the channel) reliably runs FIRST. If both used the same
	// deadline, the read pump's timeout could cancel the viewer before the
	// watchdog enqueued EOS, dropping the EOS and closing the conn without the
	// graceful idle signal (#250 follow-up).
	go func() {
		defer func() {
			if r := recover(); r != nil {
				wsLogger.Load().Warn("WebSocket read pump panic recovered", "error", r)
			}
		}()
		for {
			select {
			case <-viewerCtx.Done():
				return
			default:
			}
			conn.SetReadDeadline(time.Now().Add(2 * m.idleTimeout))
			_, _, err := conn.ReadMessage()
			if err != nil {
				viewerCancel()
				return
			}
		}
	}()

	// Start idle watchdog. lastActivity is read here (watchdog goroutine) and
	// written by the frame loop below (ServeWS goroutine) — use atomic to avoid
	// a data race under -race. Stored as unix nanoseconds.
	var lastActivity atomic.Int64
	lastActivity.Store(time.Now().UnixNano())
	idleTicker := time.NewTicker(m.idleTimeout / 2)
	defer idleTicker.Stop()
	go func() {
		for {
			select {
			case <-viewerCtx.Done():
				return
			case <-idleTicker.C:
				last := time.Unix(0, lastActivity.Load())
				if time.Since(last) > m.idleTimeout {
					// Signal EOS via the channel (the frame loop is the single
					// writer to conn) — do NOT write conn here, that races the
					// frame loop. The frame loop's ctx-done drain will emit it.
					// Best-effort: if the channel is full, skip (the cancel below
					// still closes the viewer).
					sendNonBlocking(viewerCh, []byte{byte(MsgTypeEOS)})
					viewerCancel()
					return
				}
			}
		}
	}()

	// Write frames to WebSocket until disconnect. This goroutine is the SOLE
	// writer to conn (codec-info preamble above + this loop). Out-of-band
	// signals (EOS from idle watchdog or UnregisterStream) are enqueued onto
	// viewerCh and emitted here, never written directly — gorilla/websocket
	// requires all conn writes to be serialized.
	for {
		select {
		case <-viewerCtx.Done():
			// Drain any pending messages (e.g. an EOS enqueued by the idle
			// watchdog or UnregisterStream) before returning, so they are emitted
			// by this single writer rather than lost. A write failure here is
			// expected on shutdown and is ignored.
			drainViewerCh(viewerCh, conn)
			return nil
		case data, ok := <-viewerCh:
			if !ok {
				return nil // channel closed
			}
			lastActivity.Store(time.Now().UnixNano())
			if err := conn.WriteMessage(websocket.BinaryMessage, data); err != nil {
				if !strings.Contains(err.Error(), "use of closed") {
					wsLogger.Load().Warn("WebSocket write error", "camera_id", camID, "viewer_id", viewerID, "error", err)
				}
				return nil
			}
		}
	}
}

// StopAll stops all active WebSocket streams.
func (m *Manager) StopAll() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.streams))
	for id := range m.streams {
		ids = append(ids, id)
	}
	m.mu.Unlock()

	for _, id := range ids {
		m.UnregisterStream(id)
	}
}

// AudioUpstream upgrades an HTTP request to a WebSocket connection for receiving
// upstream binary audio from a browser client. It reads binary messages in a loop
// and calls handler for each received message. Blocks until the client disconnects
// or an error occurs.
// This is used for two-way audio on Xiaomi cameras where the browser sends
// PCM audio data to be encoded as G.711 and written to the camera.
func (m *Manager) AudioUpstream(w http.ResponseWriter, r *http.Request, handler func([]byte) error) error {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return err
	}
	defer conn.Close()

	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		if err := handler(msg); err != nil {
			wsLogger.Load().Warn("audio upstream handler error", "error", err)
			return err
		}
	}
}

// Ensure Manager satisfies expected interface.
var _ interface {
	RegisterStream(camID string, codec model.Format, sps, pps, vps []byte, hub *model.StreamHub) error
	UnregisterStream(camID string)
	IsActive(camID string) bool
	viewerCount(camID string) int
	writeH264(camID string, pts int64, au [][]byte)
	writeH265(camID string, pts int64, au [][]byte)
	ServeWS(camID string, quality string, w http.ResponseWriter, r *http.Request) error
	SetAudioInfo(camID string, codec string, muLaw bool, sampleRate int, channels int, config []byte) error
	StopAll()
} = (*Manager)(nil)
