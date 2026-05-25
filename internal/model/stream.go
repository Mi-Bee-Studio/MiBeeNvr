package model

import (
	"fmt"
	"sync"
	"sync/atomic"
)

// FrameCallback is called for each decoded video frame.
// Implementations MUST be non-blocking — if the internal buffer is full,
// frames are dropped silently to protect the recording pipeline.
type FrameCallback func(pts int64, au [][]byte)

// frameMsg is an internal frame representation passed through consumer channels.
type frameMsg struct {
	pts int64
	au  [][]byte
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
}

// NewStreamHub creates a new StreamHub with no consumers.
func NewStreamHub() *StreamHub {
	return &StreamHub{
		consumers:          make(map[string]*consumerEntry),
		audioConsumers:     make(map[string]*audioConsumer),
		consumerBufferSize: 150, // ~7.5s at 20fps, reduces StreamHub-level drops
	}
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
// If a consumer's buffer is full, the frame is dropped and the consumer's
// drop counter is incremented atomically.
//
// Broadcast does NOT wait for any consumer to process the frame.
func (h *StreamHub) Broadcast(pts int64, au [][]byte) {
	h.mu.Lock()
	// Snapshot entries with IDs to avoid holding hub lock during sends
	// and to allow the OnDrop callback to reference the consumer ID.
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
		select {
		case e.entry.ch <- frameMsg{pts: pts, au: au}:
		default:
			e.entry.drops.Add(1)
			if h.OnDrop != nil {
				h.OnDrop(e.id)
			}
		}
		e.entry.sendMu.RUnlock()
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
