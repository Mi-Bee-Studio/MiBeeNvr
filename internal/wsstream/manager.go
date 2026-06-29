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

	"github.com/gorilla/websocket"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
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
	ErrStreamExists   = errors.New("wsstream: stream already registered")
	ErrStreamNotActive = errors.New("wsstream: stream not active")
	ErrMaxViewers     = errors.New("wsstream: max viewers reached")
)


// viewerConn represents a connected WebSocket client.
type viewerConn struct {
	id         int64
	conn       *websocket.Conn
	ch         chan []byte // encoded binary messages
	cancel     context.CancelFunc
	audioOnly  bool // true = skip video frames, audio only
}

// streamEntry holds per-camera WebSocket streaming state.
type streamEntry struct {
	codec      model.Format
	sps        []byte
	pps        []byte
	vps        []byte
	viewers    map[int64]*viewerConn
	viewerSeq  atomic.Int64
	viewerMu   sync.Mutex
	frameCh    chan model.FrameMsg
	cancel     context.CancelFunc
	hub        *model.StreamHub
	hubSubID   string
	dropCount  atomic.Int64

	// Audio fields (zero-value = no audio)
	audioCodec      byte              // wire format codec byte
	audioSampleRate uint32            // sample rate in Hz
	audioChannels   uint8             // number of channels
	audioCh         chan model.AudioFrame // audio frame channel, nil if no audio
	audioSubID      string            // StreamHub audio subscription ID
}

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
		codec:    codec,
		sps:       sps,
		pps:       pps,
		vps:       vps,
		viewers:   make(map[int64]*viewerConn),
		frameCh:   make(chan model.FrameMsg, m.writeBufSize),
		cancel:    cancel,
		hub:       hub,
		audioCh:   nil, // lazily allocated in SetAudioInfo
	}

	// Subscribe to recorder's StreamHub for live frames
	if hub != nil {
		hubSubID := "ws-" + camID
		entry.hubSubID = hubSubID
		_ = hub.Subscribe(hubSubID, func(pts int64, au [][]byte) {
			m.writeFrame(camID, pts, au)
		})
	}

	m.streams[camID] = entry
	go m.writeLoop(ctx, camID, entry)
	// Initialize audio channel (nil by default, allocated lazily in SetAudioInfo)
	entry.audioCh = nil


	wsLogger.Load().Info("WebSocket stream registered", "camera_id", camID, "codec", string(codec), "hub", hub != nil)
	return nil
}

// UnregisterStream removes a camera stream and disconnects all viewers.
func (m *Manager) UnregisterStream(camID string) {
	m.mu.Lock()
	entry, ok := m.streams[camID]
	if ok {
		// Unsubscribe from recorder's StreamHub while holding the lock
		// to prevent race with hub callback accessing entry after removal.
		if entry.hub != nil && entry.hubSubID != "" {
			entry.hub.Unsubscribe(entry.hubSubID)
		}
		// Unsubscribe audio consumer
		if entry.hub != nil && entry.audioSubID != "" {
			entry.hub.UnsubscribeAudio(entry.audioSubID)
		}
		delete(m.streams, camID)
	}
	m.mu.Unlock()

	if ok {
		entry.cancel()
		entry.viewerMu.Lock()
		eosMsg := []byte{byte(MsgTypeEOS)}
		for _, v := range entry.viewers {
			// Send EOS to viewer before closing
			_ = v.conn.WriteMessage(websocket.BinaryMessage, eosMsg)
			v.cancel()
			close(v.ch)
		}
		entry.viewerMu.Unlock()
		wsLogger.Load().Info("WebSocket stream unregistered", "camera_id", camID)
		if cnt := entry.dropCount.Load(); cnt > 0 {
			wsLogger.Load().Info("stream drop count", "camera_id", camID, "total_drops", cnt)
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
	m.writeFrame(camID, pts, au)
}

// writeH265 queues an H.265 access unit for WebSocket output. Non-blocking.
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
		return
	}

	isKeyframe := false
	if len(au) > 0 && len(au[0]) > 0 {
		var naluType int
		if entry.codec == model.FormatH265 {
			// H.265: forbidden(1) | nal_unit_type(6) | ...
			naluType = int((au[0][0] >> 1) & 0x3F)
		} else {
			// H.264: forbidden(1) | nal_ref_idc(2) | nal_unit_type(5)
			naluType = int(au[0][0] & 0x1F)
		}
		// H.264 IDR = 5, H.265 IDR_W_RADL = 19, IDR_N_LP = 20
		isKeyframe = naluType == 5 || naluType == 19 || naluType == 20
	}

	// Non-blocking send
	traceID := "no-trace"
	if isKeyframe {
		traceID = fmt.Sprintf("%s-%d", camID, pts)
	}
	select {
	case entry.frameCh <- model.FrameMsg{PTS: pts, AU: au, IsKeyframe: isKeyframe}:
		slog.Debug("frame_trace",
			"trace_id", traceID,
			"camera_id", camID,
			"stage", "ws_recv",
			"is_idr", isKeyframe,
		)
	default:
		// Buffer full, drop frame
		cnt := entry.dropCount.Add(1)
		slog.Debug("frame_trace",
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
func (m *Manager) SetAudioInfo(camID string, codec string, muLaw bool, sampleRate int, channels int) error {
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

	wsLogger.Load().Info("WebSocket audio configured",
		"camera_id", camID,
		"codec", codec,
		"sample_rate", sampleRate,
		"channels", channels,
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
func (m *Manager) ServeWS(camID string, w http.ResponseWriter, r *http.Request) error {
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

	// Start read pump to detect client disconnect.
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
			conn.SetReadDeadline(time.Now().Add(m.idleTimeout))
			_, _, err := conn.ReadMessage()
			if err != nil {
				viewerCancel()
				return
			}
		}
	}()

	// Start idle watchdog
	lastActivity := time.Now()
	idleTicker := time.NewTicker(m.idleTimeout / 2)
	defer idleTicker.Stop()
	go func() {
		for {
			select {
			case <-viewerCtx.Done():
				return
			case <-idleTicker.C:
				if time.Since(lastActivity) > m.idleTimeout {
					// Send EOS before closing so frontend can show offline status
					_ = conn.WriteMessage(websocket.BinaryMessage, []byte{byte(MsgTypeEOS)})
					viewerCancel()
					return
				}
			}
		}
		}()


	// Write frames to WebSocket until disconnect
	for {
		select {
		case <-viewerCtx.Done():
			return nil
		case data, ok := <-viewerCh:
			if !ok {
				return nil // channel closed
			}
			lastActivity = time.Now()
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

// Ensure Manager satisfies expected interface.
var _ interface {
	RegisterStream(camID string, codec model.Format, sps, pps, vps []byte, hub *model.StreamHub) error
	UnregisterStream(camID string)
	IsActive(camID string) bool
	viewerCount(camID string) int
	writeH264(camID string, pts int64, au [][]byte)
	writeH265(camID string, pts int64, au [][]byte)
	ServeWS(camID string, w http.ResponseWriter, r *http.Request) error
	SetAudioInfo(camID string, codec string, muLaw bool, sampleRate int, channels int) error
	StopAll()
} = (*Manager)(nil)
