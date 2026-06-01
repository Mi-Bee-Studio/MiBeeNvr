package model

import (
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// FrameCallback is called for each decoded video frame.
// Implementations MUST be non-blocking — if the internal buffer is full,
// frames are dropped silently to protect the recording pipeline.
type FrameCallback func(pts int64, au [][]byte)

// frameMsg is an internal frame representation passed through consumer channels.
type frameMsg struct {
	pts   int64
	au    [][]byte
	isIDR bool
}

// AudioCallback is called for each decoded audio frame.
// Implementations MUST be non-blocking — if the internal buffer is full,
// frames are dropped silently to protect the recording/streaming pipeline.
type AudioCallback func(pts int64, codec AudioCodec, data []byte)

// audioFrameMsg is an internal audio frame representation passed through consumer channels.
type audioFrameMsg struct {
	pts   int64
	codec AudioCodec
	data  []byte
}

// audioConsumer holds a subscribed audio consumer with its own buffered channel,
// drain goroutine, and per-consumer drop counter.
type audioConsumer struct {
	cb     AudioCallback
	ch     chan audioFrameMsg
	done   chan struct{}
	drops  atomic.Int64
	sendMu sync.RWMutex
	closed bool
}

// drain reads audio frames from the consumer's channel and calls the callback.
func (c *audioConsumer) drain() {
	defer close(c.done)
	for msg := range c.ch {
		c.cb(msg.pts, msg.codec, msg.data)
	}
}

// consumerEntry holds a subscribed consumer with its own buffered channel,
// drain goroutine, and per-consumer drop counter.
type consumerEntry struct {
	cb     FrameCallback
	ch     chan frameMsg
	done   chan struct{} // closed when drain goroutine exits
	drops  atomic.Int64
	sends  atomic.Int64 // tracks successful sends for drop rate calculation
	sendMu sync.RWMutex // protects ch from close-during-send race
	closed bool
}

// drain reads frames from the consumer's channel and calls the callback.
// This decouples the Broadcast path from slow consumers.
func (e *consumerEntry) drain() {
	defer close(e.done)
	for msg := range e.ch {
		e.cb(msg.pts, msg.au)
	}
}

// StreamHub distributes frames from a single source to multiple consumers.
// Each consumer is identified by a unique string ID and runs in its own goroutine
// with a buffered channel, so slow consumers never block others.
//
// All methods are safe for concurrent use.
type StreamHub struct {
	mu                 sync.Mutex
	consumers          map[string]*consumerEntry
	audioConsumers     map[string]*audioConsumer
	consumerBufferSize int // buffered channel size per video consumer (default: 150)

	// OnDrop is an optional callback invoked when a frame is dropped for a consumer
	// due to buffer overflow. The consumerID identifies which consumer's buffer was full.
	// This can be used for observability (e.g., Prometheus counters).
	OnDrop func(consumerID string)
	// OnDropRate is an optional callback invoked when a consumer's drop rate
	// exceeds the warn threshold. The callback receives the consumer ID and current
	// drop rate (drops / (drops + sends), range [0.0, 1.0]).
	OnDropRate func(consumerID string, dropRate float64)
	// dropRateWarnThreshold is the drop rate (0.0-1.0) at which OnDropRate is called
	// and a warning is logged. Default: 0.30 (30%).
	dropRateWarnThreshold float64
	// OnBroadcast is an optional callback invoked for every frame broadcast.
	// Used for observability (e.g., Prometheus counters, structured logging).
	OnBroadcast func(cameraID string, isIDR bool)
	cameraID    string // set by SetCameraID after construction

	// Jitter buffer state — only activated when out-of-order frames are detected.
	jitterBufferEnabled  atomic.Bool
	jitterBufferSize     int           // max frames to buffer before flush (default: 5)
	jitterBufferTimeout  time.Duration // max wait before flushing partial buffer (default: 500ms)
	jitterBuffer         []frameMsg    // buffered frames awaiting reordering
	jitterBufferMu       sync.Mutex    // protects jitter buffer state
	jitterBufferTimer    *time.Timer   // timeout flush timer
	jitterBufferLastPTS  int64         // last PTS seen, for disorder detection
	jitterBufferReorders atomic.Int64  // total out-of-order detections
	jitterBufferActive   atomic.Bool   // quick check if buffer may have frames
	// OnJitterBufferFlush is called when jitter buffer flushes reordered frames.
	// Receives cameraID and number of frames flushed.
	OnJitterBufferFlush func(cameraID string, count int)
	// OnBufferDepth is called after each distributeFrame send/drop with current channel depth.
	OnBufferDepth func(cameraID, consumerID string, depth int)
	// OnJitterBufferDepth is called when jitter buffer depth changes.
	OnJitterBufferDepth func(cameraID string, depth int)
	// OnJitterReorder is called when an out-of-order frame is detected.
	OnJitterReorder func(cameraID string)
}

// NewStreamHub creates a new StreamHub with no consumers.
func NewStreamHub() *StreamHub {
	return &StreamHub{
		consumers:             make(map[string]*consumerEntry),
		audioConsumers:        make(map[string]*audioConsumer),
		consumerBufferSize:    150, // ~7.5s at 20fps, reduces StreamHub-level drops
		jitterBufferSize:      5,   // buffer up to 5 frames before forced flush
		jitterBufferTimeout:   500 * time.Millisecond,
		dropRateWarnThreshold: 0.30,
	}
}

// SetCameraID sets the camera identifier for structured logging.
// Must be called after NewStreamHub() before any Broadcast calls.
func (h *StreamHub) SetCameraID(id string) {
	h.cameraID = id
}

// Subscribe registers a consumer with the given unique ID and callback.
// Returns an error if a consumer with the same ID already exists.
// The callback is called from a dedicated goroutine — it may block without
// affecting other consumers or the Broadcast caller.
func (h *StreamHub) Subscribe(id string, cb FrameCallback) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if _, ok := h.consumers[id]; ok {
		return fmt.Errorf("consumer %q already subscribed", id)
	}

	entry := &consumerEntry{
		cb:   cb,
		ch:   make(chan frameMsg, h.consumerBufferSize),
		done: make(chan struct{}),
	}
	h.consumers[id] = entry
	go entry.drain()
	return nil
}

// Unsubscribe removes the consumer with the given ID.
// It waits for the consumer's drain goroutine to finish processing buffered frames.
// If the consumer does not exist, Unsubscribe is a no-op.
func (h *StreamHub) Unsubscribe(id string) {
	h.mu.Lock()
	entry, ok := h.consumers[id]
	if ok {
		delete(h.consumers, id)
	}
	h.mu.Unlock()

	if ok {
		entry.sendMu.Lock()
		entry.closed = true
		entry.sendMu.Unlock()
		close(entry.ch) // signal drain goroutine to stop
		<-entry.done    // wait for drain to finish
	}
}

// Broadcast sends a frame to all subscribed consumers.
// This is non-blocking — it uses a non-blocking channel send per consumer.
// If a consumer's buffer is full:
//   - Non-IDR frames: dropped silently (drop counter incremented).
//   - IDR frames: protected — the oldest non-IDR frame is evicted from the
//     consumer's buffer to make space, then the IDR is enqueued with a short
//     timeout. This ensures consumers always have access to IDR frames for
//     decoding, even when their buffers are full.
//
// The isIDR parameter should be set by the caller using nalutil.IsIDR(au, isH265).
//
// Broadcast does NOT block the caller beyond a 50ms timeout for IDR protection.
func (h *StreamHub) Broadcast(pts int64, au [][]byte, isIDR bool) {
	// Compute trace ID: only meaningful for IDR frames.
	traceID := "no-trace"
	if isIDR {
		traceID = fmt.Sprintf("%s-%d", h.cameraID, pts)
	}

	slog.Debug("frame_trace",
		"trace_id", traceID,
		"camera_id", h.cameraID,
		"stage", "streamhub_in",
		"is_idr", isIDR,
	)

	if h.OnBroadcast != nil {
		h.OnBroadcast(h.cameraID, isIDR)
	}

	// Jitter buffer: detect disorder, buffer+sort, flush on timeout or capacity.
	if h.jitterBufferEnabled.Load() || h.detectDisorder(pts) {
		h.bufferAndMaybeFlush(pts, au, isIDR)
		return
	}

	h.distributeFrame(pts, au, isIDR)
}

// distributeFrame sends a single frame to all subscribed video consumers.
// This is the direct (no jitter buffer) path.
func (h *StreamHub) distributeFrame(pts int64, au [][]byte, isIDR bool) {
	h.mu.Lock()
	type entryWithID struct {
		id    string
		entry *consumerEntry
	}
	entries := make([]entryWithID, 0, len(h.consumers))
	for id, entry := range h.consumers {
		entries = append(entries, entryWithID{id: id, entry: entry})
	}
	h.mu.Unlock()

	for _, e := range entries {
		e.entry.sendMu.RLock()
		if e.entry.closed {
			e.entry.sendMu.RUnlock()
			continue
		}
		msg := frameMsg{pts: pts, au: au, isIDR: isIDR}
		select {
		case e.entry.ch <- msg:
			e.entry.sends.Add(1)
		default:
			if isIDR {
				h.trySendIDR(e.entry.ch, msg)
			} else {
				e.entry.drops.Add(1)
				slog.Warn("frame_trace",
					"trace_id", "no-trace",
					"camera_id", h.cameraID,
					"stage", "streamhub_drop",
					"is_idr", isIDR,
					"queue_depth", len(e.entry.ch),
					"consumer", e.id,
				)
				if h.OnDrop != nil {
					h.OnDrop(e.id)
				}
				h.checkDropRate(e.id, e.entry)
			}
		}
		e.entry.sendMu.RUnlock()
		if h.OnBufferDepth != nil {
			h.OnBufferDepth(h.cameraID, e.id, len(e.entry.ch))
		}
	}
}

// detectDisorder checks if the given PTS is less than the last seen PTS.
// If disorder is detected for the first time, it activates the jitter buffer.
// Returns true if jitter buffer should be used for this frame.
// Note: the previous frame (already distributed) cannot be recalled.
func (h *StreamHub) detectDisorder(pts int64) bool {
	h.jitterBufferMu.Lock()
	defer h.jitterBufferMu.Unlock()
	if h.jitterBufferLastPTS > 0 && pts < h.jitterBufferLastPTS {
		h.jitterBufferReorders.Add(1)
		if h.OnJitterReorder != nil {
			h.OnJitterReorder(h.cameraID)
		}
		h.jitterBufferEnabled.Store(true)
		slog.Info("jitter_buffer_activated",
			"camera_id", h.cameraID,
			"last_pts", h.jitterBufferLastPTS,
			"current_pts", pts,
		)
		h.jitterBufferLastPTS = 0 // reset tracking since we're now buffering
		return true
	}
	h.jitterBufferLastPTS = pts
	return false
}

// bufferAndMaybeFlush adds a frame to the jitter buffer and flushes if full.
func (h *StreamHub) bufferAndMaybeFlush(pts int64, au [][]byte, isIDR bool) {
	h.jitterBufferMu.Lock()
	h.jitterBuffer = append(h.jitterBuffer, frameMsg{pts: pts, au: au, isIDR: isIDR})
	h.jitterBufferActive.Store(true)
	if h.OnJitterBufferDepth != nil {
		h.OnJitterBufferDepth(h.cameraID, len(h.jitterBuffer))
	}

	if len(h.jitterBuffer) >= h.jitterBufferSize {
		frames := h.flushJitterBufferLocked()
		h.jitterBufferMu.Unlock()
		for _, f := range frames {
			if f.au != nil {
				h.distributeFrame(f.pts, f.au, f.isIDR)
			}
		}
		return
	}
	h.jitterBufferMu.Unlock()
	h.resetJitterBufferTimer()
}

// flushJitterBufferLocked sorts the jitter buffer by PTS and returns the sorted frames.
// Must be called with jitterBufferMu held.
func (h *StreamHub) flushJitterBufferLocked() []frameMsg {
	if len(h.jitterBuffer) == 0 {
		return nil
	}
	sort.Slice(h.jitterBuffer, func(i, j int) bool {
		return h.jitterBuffer[i].pts < h.jitterBuffer[j].pts
	})
	frames := h.jitterBuffer
	h.jitterBuffer = nil
	h.jitterBufferActive.Store(false)
	if h.jitterBufferTimer != nil {
		h.jitterBufferTimer.Stop()
		h.jitterBufferTimer = nil
	}
	if h.OnJitterBufferFlush != nil && len(frames) > 0 {
		h.OnJitterBufferFlush(h.cameraID, len(frames))
	}
	if h.OnJitterBufferDepth != nil {
		h.OnJitterBufferDepth(h.cameraID, 0) // buffer flushed
	}
	return frames
}

// resetJitterBufferTimer resets the timeout flush timer for the jitter buffer.
func (h *StreamHub) resetJitterBufferTimer() {
	h.jitterBufferMu.Lock()
	if h.jitterBufferTimer != nil {
		h.jitterBufferTimer.Stop()
		h.jitterBufferTimer = nil
	}
	if len(h.jitterBuffer) > 0 {
		h.jitterBufferTimer = time.AfterFunc(h.jitterBufferTimeout, func() {
			h.jitterBufferMu.Lock()
			frames := h.flushJitterBufferLocked()
			h.jitterBufferMu.Unlock()
			for _, f := range frames {
				if f.au != nil {
					h.distributeFrame(f.pts, f.au, f.isIDR)
				}
			}
		})
	}
	h.jitterBufferMu.Unlock()
}

// trySendIDR attempts to deliver an IDR frame by draining the oldest non-IDR
// frame from the channel and retrying. It uses a 50ms timeout to avoid blocking
// the caller for too long. Falls back to dropping if space cannot be made.
func (h *StreamHub) trySendIDR(ch chan frameMsg, msg frameMsg) {
	// Drain one oldest frame (non-blocking). If it was an IDR, put it back
	// and try to drain the next one. We want to preserve IDRs.
	// Limit scan to channel capacity to avoid infinite loop when buffer is all IDRs.
	bufCap := cap(ch)
	for i := 0; i < bufCap; i++ {
		select {
		case old := <-ch:
			if old.isIDR {
				// Don't evict IDR frames; try non-blocking re-enqueue.
				select {
				case ch <- old:
					// Re-enqueued, continue scanning for non-IDR.
				default:
					// Buffer still full after re-enqueue; stop.
					return
				}
			} else {
				// Successfully drained a non-IDR frame — space available.
				// Non-blocking send should succeed immediately.
				select {
				case ch <- msg:
					return
				default:
					// Race: space taken. Fall through to timeout.
					return
				}
			}
		default:
			// Channel empty — shouldn't happen since send was blocked.
			return
		}
	}

	// All frames in buffer were IDRs (or scan limit reached).
	// Drop the IDR frame as last resort — consumer already has IDRs buffered.
	select {
	case ch <- msg:
	default:
		// Buffer is all IDRs — non-IDR space unavailable. Frame dropped.
	}
}

// Drops returns the total number of frames dropped for the given consumer
// due to buffer overflow. Returns 0 for non-existent consumers.
func (h *StreamHub) Drops(id string) int64 {
	h.mu.Lock()
	entry, ok := h.consumers[id]
	h.mu.Unlock()

	if !ok {
		return 0
	}
	return entry.drops.Load()
}

// DropRate returns the current drop rate for the given consumer.
// Rate = drops / (drops + sends). Returns 0.0 for non-existent consumers
// or consumers with no traffic.
func (h *StreamHub) DropRate(id string) float64 {
	h.mu.Lock()
	entry, ok := h.consumers[id]
	h.mu.Unlock()

	if !ok {
		return 0.0
	}
	drops := entry.drops.Load()
	sends := entry.sends.Load()
	total := drops + sends
	if total == 0 {
		return 0.0
	}
	return float64(drops) / float64(total)
}

// checkDropRate checks if a consumer's drop rate exceeds the warn threshold.
// If so, logs a warning and calls the OnDropRate callback.
// Only logs periodically (every 100 drops) to avoid log spam.
func (h *StreamHub) checkDropRate(consumerID string, entry *consumerEntry) {
	drops := entry.drops.Load()
	// Throttle: only check every 100 drops to avoid per-drop overhead
	if drops%100 != 0 {
		return
	}
	sends := entry.sends.Load()
	total := drops + sends
	if total == 0 {
		return
	}
	rate := float64(drops) / float64(total)
	if rate > h.dropRateWarnThreshold {
		slog.Warn("high consumer drop rate",
			"camera_id", h.cameraID,
			"consumer", consumerID,
			"drop_rate", rate,
			"drops", drops,
			"sends", sends,
			"threshold", h.dropRateWarnThreshold,
		)
		if h.OnDropRate != nil {
			h.OnDropRate(consumerID, rate)
		}
	}
}

// Sends returns the total number of frames successfully sent to the given consumer.
// Returns 0 for non-existent consumers.
func (h *StreamHub) Sends(id string) int64 {
	h.mu.Lock()
	entry, ok := h.consumers[id]
	h.mu.Unlock()

	if !ok {
		return 0
	}
	return entry.sends.Load()
}

// ConsumerCount returns the number of currently subscribed consumers.
func (h *StreamHub) ConsumerCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.consumers)
}

// SubscribeAudio registers an audio consumer with the given unique ID and callback.
// Returns an error if a consumer with the same ID already exists.
// The callback is called from a dedicated goroutine — it may block without
// affecting other consumers or the BroadcastAudio caller.
func (h *StreamHub) SubscribeAudio(id string, cb AudioCallback) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if _, ok := h.audioConsumers[id]; ok {
		return fmt.Errorf("audio consumer %q already subscribed", id)
	}

	entry := &audioConsumer{
		cb:   cb,
		ch:   make(chan audioFrameMsg, 50), // audio frames smaller than video, 50 frames buffer
		done: make(chan struct{}),
	}
	h.audioConsumers[id] = entry
	go entry.drain()
	return nil
}

// UnsubscribeAudio removes the audio consumer with the given ID.
// It waits for the consumer's drain goroutine to finish processing buffered frames.
// If the consumer does not exist, UnsubscribeAudio is a no-op.
func (h *StreamHub) UnsubscribeAudio(id string) {
	h.mu.Lock()
	entry, ok := h.audioConsumers[id]
	if ok {
		delete(h.audioConsumers, id)
	}
	h.mu.Unlock()

	if ok {
		entry.sendMu.Lock()
		entry.closed = true
		entry.sendMu.Unlock()
		close(entry.ch) // signal drain goroutine to stop
		<-entry.done    // wait for drain to finish
	}
}

// BroadcastAudio sends an audio frame to all subscribed audio consumers.
// This is non-blocking — it uses a non-blocking channel send per consumer.
// If a consumer's buffer is full, the frame is dropped and the consumer's
// drop counter is incremented atomically.
//
// BroadcastAudio does NOT wait for any consumer to process the frame.
func (h *StreamHub) BroadcastAudio(pts int64, codec AudioCodec, data []byte) {
	h.mu.Lock()
	type entryWithID struct {
		id    string
		entry *audioConsumer
	}
	entries := make([]entryWithID, 0, len(h.audioConsumers))
	for id, entry := range h.audioConsumers {
		entries = append(entries, entryWithID{id: id, entry: entry})
	}
	h.mu.Unlock()

	for _, e := range entries {
		e.entry.sendMu.RLock()
		if e.entry.closed {
			e.entry.sendMu.RUnlock()
			continue
		}
		select {
		case e.entry.ch <- audioFrameMsg{pts: pts, codec: codec, data: data}:
		default:
			e.entry.drops.Add(1)
			if h.OnDrop != nil {
				h.OnDrop(e.id)
			}
		}
		e.entry.sendMu.RUnlock()
	}
}

// AudioDrops returns the total number of audio frames dropped for the given consumer
// due to buffer overflow. Returns 0 for non-existent consumers.
func (h *StreamHub) AudioDrops(id string) int64 {
	h.mu.Lock()
	entry, ok := h.audioConsumers[id]
	h.mu.Unlock()

	if !ok {
		return 0
	}
	return entry.drops.Load()
}

// AudioConsumerCount returns the number of currently subscribed audio consumers.
func (h *StreamHub) AudioConsumerCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.audioConsumers)
}
