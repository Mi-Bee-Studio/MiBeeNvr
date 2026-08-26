package model

import (
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model/nalutil"
)

// SubStreamKeySuffix is appended to a camera ID to form the protocol-manager
// stream key under which a camera's on-demand sub-stream egress is registered
// (#513). Managers key entries by opaque strings, so the suffixed key reuses
// their machinery unchanged; the suffix lives here so the webrtc manager
// (camera-level peer counts) and the app wiring (session-end → sub-stream
// release) agree with the api layer's subKey helper.
const SubStreamKeySuffix = "/sub"

// FrameCallback is called for each decoded video frame.
// Implementations MUST be non-blocking — if the internal buffer is full,
// frames are dropped silently to protect the recording pipeline.
type FrameCallback func(pts int64, au [][]byte)

// MsgCallback receives the full FrameMsg — an opt-in variant of FrameCallback
// for consumers that need per-frame metadata (notably IngestAt, used by
// wsstream to relay end-to-end live latency). Kept separate so the widely
// implemented FrameCallback signature (and its pkg/streamhub mirror) stays
// stable (#469).
type MsgCallback func(msg FrameMsg)

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

// queuedFrame pairs a frame with its enqueue time so the consumer's drain
// goroutine can measure queue dwell latency without touching the producer
// hot path.
type queuedFrame struct {
	msg        FrameMsg
	enqueuedAt int64 // unix nano
}

// consumerEntry holds a subscribed consumer with its own buffered channel,
// drain goroutine, and per-consumer counters.
type consumerEntry struct {
	cb           FrameCallback
	cbMsg        MsgCallback // set instead of cb for SubscribeMsg consumers
	ch           chan queuedFrame
	done         chan struct{} // closed when drain goroutine exits
	drops        atomic.Int64
	sends        atomic.Int64 // tracks successful sends for drop rate calculation
	idrDrops     atomic.Int64 // IDR frames lost in trySendIDR fallback
	bytes        atomic.Int64 // sum of NALU lengths delivered
	dwellSumNS   atomic.Int64 // cumulative enqueue→drain latency
	dwellCount   atomic.Int64
	dwellMaxNS   atomic.Int64
	subscribedAt time.Time
	lastSendAt   atomic.Int64 // unix nano of last successful enqueue
	sendMu       sync.RWMutex // protects ch from close-during-send race
	closed       bool
}

// drain reads frames from the consumer's channel and calls the callback.
// This decouples the Broadcast path from slow consumers. Dwell is measured
// here (consumer goroutine) rather than at enqueue so the producer path
// stays lock- and syscall-free.
func (e *consumerEntry) drain() {
	defer close(e.done)
	for qf := range e.ch {
		if ns := time.Now().UnixNano() - qf.enqueuedAt; ns > 0 {
			e.dwellSumNS.Add(ns)
			e.dwellCount.Add(1)
			for {
				cur := e.dwellMaxNS.Load()
				if ns <= cur || e.dwellMaxNS.CompareAndSwap(cur, ns) {
					break
				}
			}
		}
		if e.cbMsg != nil {
			e.cbMsg(qf.msg)
		} else {
			e.cb(qf.msg.PTS, qf.msg.AU)
		}
	}
}

// frameSize returns the total byte size of a frame's NAL units.
func frameSize(au [][]byte) int {
	n := 0
	for _, nalu := range au {
		n += len(nalu)
	}
	return n
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

	// onDrops holds drop callbacks; AddOnDrop appends. Historically OnDrop was
	// a single field, which let the HLS manager silently overwrite the camera
	// manager's Prometheus wiring (#469 Phase 0) — a callback list makes
	// multi-subscriber instrumentation compositional.
	onDrops []func(consumerID string, isIDR bool)
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
	// OnBroadcastAudio is an optional callback invoked for every audio frame broadcast.
	// Used for Prometheus audio frame counters.
	OnBroadcastAudio func(cameraID string, codec string)
	// OnAudioDrop is an optional callback invoked when an audio frame is dropped.
	OnAudioDrop func(cameraID string)
	cameraID    string // set by SetCameraID after construction
	// source labels the producing side (recorder type) for flow-path display.
	// Guarded by mu; set via SetSource.
	source string

	// Hub-level counters, updated on the Broadcast hot path with atomics only.
	framesIn         atomic.Int64
	bytesIn          atomic.Int64
	lastFrameAt      atomic.Int64 // unix nano of last video frame
	lastAudioFrameAt atomic.Int64 // unix nano of last audio frame

	// IDR fast-start cache — stores the most recent N keyframe access units so a
	// newly-subscribed consumer can begin decoding immediately, without waiting up
	// to one full GOP for the next natural IDR. This is essential for cameras with
	// long GOPs (e.g. 8s): without replay, a consumer that connects mid-GOP and
	// misses the IDR's inline parameter sets waits indefinitely for a keyframe and
	// the decoder emits nothing ("buffering" forever). See HasCompleteParamSets for
	// the integrity gate that prevents replaying an IDR lacking VPS/SPS/PPS.
	//
	// All access is guarded by h.mu (set in distributeFrame under h.mu; read &
	// drained in Subscribe under h.mu), matching the locking discipline of
	// consumers — no separate lock is needed.
	idrCache     []FrameMsg
	idrCacheSize int // max cached IDRs (ring); 0 disables replay (legacy behavior)

	// Jitter buffer state — only activated when out-of-order frames are detected.
	jitterBufferEnabled  atomic.Bool
	jitterBufferSize     int           // max frames to buffer before flush (default: 5)
	jitterBufferTimeout  time.Duration // max wait before flushing partial buffer (default: 500ms)
	jitterBuffer         []FrameMsg    // buffered frames awaiting reordering
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

// DefaultIDRCacheSize is the number of most-recent IDR access units StreamHub
// keeps for fast-start replay to new subscribers. 3 balances memory (a few frames)
// against giving a late joiner a keyframe near the current playhead. Set
// StreamHub.idrCacheSize = 0 to disable replay entirely (legacy behavior).
const DefaultIDRCacheSize = 3

// NewStreamHub creates a new StreamHub with no consumers.
func NewStreamHub() *StreamHub {
	return &StreamHub{
		consumers:             make(map[string]*consumerEntry),
		audioConsumers:        make(map[string]*audioConsumer),
		consumerBufferSize:    150, // ~7.5s at 20fps, reduces StreamHub-level drops
		jitterBufferSize:      5,   // buffer up to 5 frames before forced flush
		jitterBufferTimeout:   500 * time.Millisecond,
		dropRateWarnThreshold: 0.30,
		idrCacheSize:          DefaultIDRCacheSize,
	}
}

// SetCameraID sets the camera identifier for structured logging.
// Must be called after NewStreamHub() before any Broadcast calls.
func (h *StreamHub) SetCameraID(id string) {
	h.cameraID = id
}

// SetSource labels the producing side (e.g. "h264", "xiaomi", "srt-push")
// for flow-path display. Optional; empty means unlabeled.
func (h *StreamHub) SetSource(source string) {
	h.mu.Lock()
	h.source = source
	h.mu.Unlock()
}

// AddOnDrop registers an additional drop callback. Multiple registrants
// (camera manager Prometheus wiring, per-protocol managers) all receive
// drop events; see the onDrops field comment for why this is a list.
func (h *StreamHub) AddOnDrop(cb func(consumerID string, isIDR bool)) {
	if cb == nil {
		return
	}
	h.mu.Lock()
	h.onDrops = append(h.onDrops, cb)
	h.mu.Unlock()
}

// fireOnDrop invokes all registered drop callbacks. Called on the (rare)
// drop path only — safe to copy the slice under mu there.
func (h *StreamHub) fireOnDrop(consumerID string, isIDR bool) {
	h.mu.Lock()
	cbs := make([]func(string, bool), len(h.onDrops))
	copy(cbs, h.onDrops)
	h.mu.Unlock()
	for _, cb := range cbs {
		cb(consumerID, isIDR)
	}
}

// Subscribe registers a consumer with the given unique ID and callback.
// Returns an error if a consumer with the same ID already exists.
// The callback is called from a dedicated goroutine — it may block without
// affecting other consumers or the Broadcast caller.
func (h *StreamHub) Subscribe(id string, cb FrameCallback) error {
	// Snapshot the IDR cache to replay OUTSIDE h.mu. We deliberately do NOT push
	// the replay frame while holding h.mu: the consumer's drain goroutine
	// (started below) runs the callback, and that callback commonly acquires
	// ANOTHER lock (e.g. wsstream.Manager.mu in RegisterStream's caller). If we
	// pushed during Subscribe while the caller still holds that outer lock, the
	// drain goroutine would block on it (lock-order inversion), stalling frame
	// delivery to the new consumer — observed as a 6s reconnect storm. Pushing
	// after Subscribe returns lets the caller release its lock first.
	h.mu.Lock()
	if _, ok := h.consumers[id]; ok {
		h.mu.Unlock()
		return fmt.Errorf("consumer %q already subscribed", id)
	}

	entry := &consumerEntry{
		cb:           cb,
		ch:           make(chan queuedFrame, h.consumerBufferSize),
		done:         make(chan struct{}),
		subscribedAt: time.Now(),
	}
	h.consumers[id] = entry
	// Capture the replay candidate under the lock (consistent snapshot with the
	// consumers map), but defer the actual channel send until after Unlock.
	replay := h.latestCompleteIDRLocked()
	h.mu.Unlock()

	go entry.drain()

	// Replay the cached IDR now that no StreamHub lock is held. drain is already
	// running; it will consume this frame via its callback. Because Subscribe has
	// returned to the caller (e.g. wsstream.RegisterStream), the caller's outer
	// lock is released by this point too, so the callback's own lock acquisition
	// won't deadlock. The non-blocking send skips if the buffer is unexpectedly
	// full (the consumer still receives live frames normally).
	if replay != nil {
		select {
		case entry.ch <- queuedFrame{msg: *replay, enqueuedAt: time.Now().UnixNano()}:
		default:
		}
	}
	return nil
}

// SubscribeMsg registers a consumer that receives the full FrameMsg (including
// IngestAt wallclock). Same semantics as Subscribe; see MsgCallback.
func (h *StreamHub) SubscribeMsg(id string, cb MsgCallback) error {
	h.mu.Lock()
	if _, ok := h.consumers[id]; ok {
		h.mu.Unlock()
		return fmt.Errorf("consumer %q already subscribed", id)
	}
	entry := &consumerEntry{
		cbMsg:        cb,
		ch:           make(chan queuedFrame, h.consumerBufferSize),
		done:         make(chan struct{}),
		subscribedAt: time.Now(),
	}
	h.consumers[id] = entry
	replay := h.latestCompleteIDRLocked()
	h.mu.Unlock()

	go entry.drain()
	if replay != nil {
		select {
		case entry.ch <- queuedFrame{msg: *replay, enqueuedAt: time.Now().UnixNano()}:
		default:
		}
	}
	return nil
}

// cacheIDRLocked appends a deep copy of an IDR access unit to the IDR ring
// buffer. Must be called with h.mu held (caller is distributeFrame, which
// already holds the lock). The ring keeps at most h.idrCacheSize entries,
// evicting the oldest when full.
func (h *StreamHub) cacheIDRLocked(pts int64, au [][]byte) {
	cp := make([][]byte, len(au))
	for i, nalu := range au {
		c := make([]byte, len(nalu))
		copy(c, nalu)
		cp[i] = c
	}
	if len(h.idrCache) >= h.idrCacheSize {
		// Ring: drop oldest. copy-in-place keeps the backing array.
		copy(h.idrCache, h.idrCache[1:])
		h.idrCache[len(h.idrCache)-1] = FrameMsg{PTS: pts, AU: cp, IsKeyframe: true}
	} else {
		h.idrCache = append(h.idrCache, FrameMsg{PTS: pts, AU: cp, IsKeyframe: true})
	}
}

// latestCompleteIDRLocked returns a pointer to the newest cached IDR whose
// access unit carries a complete parameter set (VPS+SPS+PPS for H.265, SPS+PPS
// for H.264), or nil if the cache is empty, disabled, or contains no such IDR.
// Must be called with h.mu held. The caller pushes the returned frame into the
// new consumer's channel AFTER releasing h.mu (see Subscribe) to avoid a
// lock-order inversion: the consumer's drain callback may acquire an outer lock
// (e.g. wsstream.Manager.mu) that the Subscribe caller still holds.
func (h *StreamHub) latestCompleteIDRLocked() *FrameMsg {
	if h.idrCacheSize == 0 || len(h.idrCache) == 0 {
		return nil
	}
	// Walk newest-first: the newest complete IDR is closest to the live edge and
	// the only one a decoder needs to bootstrap. (Replaying older IDRs would just
	// add stale frames the consumer must discard.)
	for i := len(h.idrCache) - 1; i >= 0; i-- {
		msg := h.idrCache[i]
		if msg.AU == nil {
			continue
		}
		// StreamHub is codec-agnostic; try both H.264 and H.265 parameter-set
		// extraction. A real IDR for either codec will satisfy exactly one.
		if nalutil.HasCompleteParamSets(msg.AU, true) || nalutil.HasCompleteParamSets(msg.AU, false) {
			return &msg
		}
	}
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
	// Hub-level counters: atomics only on the hot path; the periodic stats
	// flusher and Snapshot() read them without locks.
	now := time.Now().UnixNano()
	h.framesIn.Add(1)
	h.bytesIn.Add(int64(frameSize(au)))
	h.lastFrameAt.Store(now)

	// Compute trace ID: only meaningful for IDR frames.
	traceID := "no-trace"
	if isIDR {
		traceID = fmt.Sprintf("%s-%d", h.cameraID, pts)
	}

	slog.Debug(
		"frame_trace",
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
	// Cache IDR access units for fast-start replay to future subscribers. Done
	// under h.mu (same lock that guards consumers) so Subscribe — which reads &
	// drains the cache under h.mu — sees a consistent snapshot. Deep-copy the AU:
	// upstream recorders don't mutate slices after Broadcast today, but there's no
	// enforced contract, and a cached reference could outlive the producer's
	// intended lifetime. The copy cost (one IDR per GOP) is negligible.
	if isIDR && h.idrCacheSize > 0 {
		h.cacheIDRLocked(pts, au)
	}
	type entryWithID struct {
		id    string
		entry *consumerEntry
	}
	entries := make([]entryWithID, 0, len(h.consumers))
	for id, entry := range h.consumers {
		entries = append(entries, entryWithID{id: id, entry: entry})
	}
	h.mu.Unlock()

	size := int64(frameSize(au))
	now := time.Now().UnixNano()
	for _, e := range entries {
		e.entry.sendMu.RLock()
		if e.entry.closed {
			e.entry.sendMu.RUnlock()
			continue
		}
		msg := FrameMsg{PTS: pts, AU: au, IsKeyframe: isIDR, IngestAt: now}
		select {
		case e.entry.ch <- queuedFrame{msg: msg, enqueuedAt: now}:
			e.entry.sends.Add(1)
			e.entry.bytes.Add(size)
			e.entry.lastSendAt.Store(now)
		default:
			if isIDR {
				if h.trySendIDR(e.id, e.entry, msg) {
					e.entry.sends.Add(1)
					e.entry.bytes.Add(size)
					e.entry.lastSendAt.Store(now)
				}
			} else {
				e.entry.drops.Add(1)
				slog.Warn(
					"frame_trace",
					"trace_id", "no-trace",
					"camera_id", h.cameraID,
					"stage", "streamhub_drop",
					"is_idr", isIDR,
					"queue_depth", len(e.entry.ch),
					"consumer", e.id,
				)
				h.fireOnDrop(e.id, false)
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
		slog.Info(
			"jitter_buffer_activated",
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
	h.jitterBuffer = append(h.jitterBuffer, FrameMsg{PTS: pts, AU: au, IsKeyframe: isIDR})
	h.jitterBufferActive.Store(true)
	if h.OnJitterBufferDepth != nil {
		h.OnJitterBufferDepth(h.cameraID, len(h.jitterBuffer))
	}

	if len(h.jitterBuffer) >= h.jitterBufferSize {
		frames := h.flushJitterBufferLocked()
		h.jitterBufferMu.Unlock()
		for _, f := range frames {
			if f.AU != nil {
				h.distributeFrame(f.PTS, f.AU, f.IsKeyframe)
			}
		}
		return
	}
	h.jitterBufferMu.Unlock()
	h.resetJitterBufferTimer()
}

// flushJitterBufferLocked sorts the jitter buffer by PTS and returns the sorted frames.
// Must be called with jitterBufferMu held.
func (h *StreamHub) flushJitterBufferLocked() []FrameMsg {
	if len(h.jitterBuffer) == 0 {
		return nil
	}
	sort.Slice(h.jitterBuffer, func(i, j int) bool {
		return h.jitterBuffer[i].PTS < h.jitterBuffer[j].PTS
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
				if f.AU != nil {
					h.distributeFrame(f.PTS, f.AU, f.IsKeyframe)
				}
			}
		})
	}
	h.jitterBufferMu.Unlock()
}

// trySendIDR attempts to deliver an IDR frame by draining the oldest non-IDR
// frame from the channel and retrying. Every evicted non-IDR frame counts as a
// drop (it is genuinely lost); if no space can be made, the IDR itself is
// dropped and counted in idrDrops. Returns true if the IDR was enqueued.
func (h *StreamHub) trySendIDR(consumerID string, entry *consumerEntry, msg FrameMsg) bool {
	ch := entry.ch
	// Drain one oldest frame (non-blocking). If it was an IDR, put it back
	// and try to drain the next one. We want to preserve IDRs.
	// Limit scan to channel capacity to avoid infinite loop when buffer is all IDRs.
	bufCap := cap(ch)
	for range bufCap {
		select {
		case old := <-ch:
			if old.msg.IsKeyframe {
				// Don't evict IDR frames; try non-blocking re-enqueue.
				select {
				case ch <- old:
					// Re-enqueued, continue scanning for non-IDR.
				default:
					// Buffer still full after re-enqueue; stop.
					return false
				}
			} else {
				// Successfully drained a non-IDR frame — it is evicted for the
				// IDR, which counts as a drop for this consumer (#469).
				entry.drops.Add(1)
				// Non-blocking send should succeed immediately.
				select {
				case ch <- queuedFrame{msg: msg, enqueuedAt: time.Now().UnixNano()}:
					return true
				default:
					// Race: space taken. Fall through to timeout.
					return false
				}
			}
		default:
			// Channel empty — shouldn't happen since send was blocked.
			return false
		}
	}

	// All frames in buffer were IDRs (or scan limit reached).
	// Drop the IDR frame as last resort — consumer already has IDRs buffered.
	entry.idrDrops.Add(1)
	entry.drops.Add(1)
	h.fireOnDrop(consumerID, true)
	return false
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
		slog.Warn(
			"high consumer drop rate",
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
	h.lastAudioFrameAt.Store(time.Now().UnixNano())
	// Observability: fire audio broadcast callback
	if h.OnBroadcastAudio != nil {
		h.OnBroadcastAudio(h.cameraID, string(codec))
	}

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
			if h.OnAudioDrop != nil {
				h.OnAudioDrop(h.cameraID)
			}
			h.fireOnDrop(e.id, false)
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

// ConsumerStats is a point-in-time view of one hub consumer, served by the
// flow-path API (/api/streams) and the periodic Prometheus flusher.
type ConsumerStats struct {
	ID             string    `json:"id"`
	Sends          int64     `json:"sends"`
	Drops          int64     `json:"drops"`
	IDRDrops       int64     `json:"idr_drops"`
	DropRate       float64   `json:"drop_rate"`
	Bytes          int64     `json:"bytes"`
	BufferDepth    int       `json:"buffer_depth"`
	BufferCapacity int       `json:"buffer_capacity"`
	SubscribedAt   time.Time `json:"subscribed_at"`
	LastSendAt     time.Time `json:"last_send_at"`
	DwellAvgMS     float64   `json:"dwell_avg_ms"`
	DwellMaxMS     float64   `json:"dwell_max_ms"`
}

// HubStats is a point-in-time view of a StreamHub. The /api/streams endpoint
// serializes it directly; frontends compute fps/bitrate by diffing cumulative
// counters (frames_in / bytes_in) between polls, keeping the hub hot path
// free of any rate computation.
type HubStats struct {
	CameraID         string          `json:"camera_id"`
	Source           string          `json:"source"`
	FramesIn         int64           `json:"frames_in"`
	BytesIn          int64           `json:"bytes_in"`
	LastFrameAt      time.Time       `json:"last_frame_at"`
	LastAudioFrameAt time.Time       `json:"last_audio_frame_at"`
	Consumers        []ConsumerStats `json:"consumers"`
	AudioConsumers   int             `json:"audio_consumers"`
	JitterActive     bool            `json:"jitter_active"`
}

// unixNanoToTime converts an atomic unix-nano stamp to time.Time,
// mapping 0 (unset) to the zero time.
func unixNanoToTime(ns int64) time.Time {
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, ns)
}

// Snapshot returns a point-in-time copy of hub and per-consumer counters.
// Safe to call concurrently with Broadcast — only mu is taken briefly to copy
// the consumer map; all counters are atomics.
func (h *StreamHub) Snapshot() HubStats {
	type entryWithID struct {
		id    string
		entry *consumerEntry
	}
	h.mu.Lock()
	entries := make([]entryWithID, 0, len(h.consumers))
	for id, entry := range h.consumers {
		entries = append(entries, entryWithID{id: id, entry: entry})
	}
	source := h.source
	audioCount := len(h.audioConsumers)
	h.mu.Unlock()

	sort.Slice(entries, func(i, j int) bool { return entries[i].id < entries[j].id })
	consumers := make([]ConsumerStats, 0, len(entries))
	for _, e := range entries {
		sends := e.entry.sends.Load()
		drops := e.entry.drops.Load()
		total := sends + drops
		rate := 0.0
		if total > 0 {
			rate = float64(drops) / float64(total)
		}
		dwellCount := e.entry.dwellCount.Load()
		dwellAvgMS := 0.0
		if dwellCount > 0 {
			dwellAvgMS = float64(e.entry.dwellSumNS.Load()) / float64(dwellCount) / 1e6
		}
		consumers = append(consumers, ConsumerStats{
			ID:             e.id,
			Sends:          sends,
			Drops:          drops,
			IDRDrops:       e.entry.idrDrops.Load(),
			DropRate:       rate,
			Bytes:          e.entry.bytes.Load(),
			BufferDepth:    len(e.entry.ch),
			BufferCapacity: cap(e.entry.ch),
			SubscribedAt:   e.entry.subscribedAt,
			LastSendAt:     unixNanoToTime(e.entry.lastSendAt.Load()),
			DwellAvgMS:     dwellAvgMS,
			DwellMaxMS:     float64(e.entry.dwellMaxNS.Load()) / 1e6,
		})
	}

	return HubStats{
		CameraID:         h.cameraID,
		Source:           source,
		FramesIn:         h.framesIn.Load(),
		BytesIn:          h.bytesIn.Load(),
		LastFrameAt:      unixNanoToTime(h.lastFrameAt.Load()),
		LastAudioFrameAt: unixNanoToTime(h.lastAudioFrameAt.Load()),
		Consumers:        consumers,
		AudioConsumers:   audioCount,
		JitterActive:     h.jitterBufferEnabled.Load(),
	}
}
