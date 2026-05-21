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
	mu        sync.Mutex
	consumers map[string]*consumerEntry
}

// NewStreamHub creates a new StreamHub with no consumers.
func NewStreamHub() *StreamHub {
	return &StreamHub{
		consumers: make(map[string]*consumerEntry),
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
		ch:   make(chan frameMsg, 100), // ~5s at 20fps, matches HLS writeBufSize
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
	// Snapshot entries to avoid holding hub lock during sends
	entries := make([]*consumerEntry, 0, len(h.consumers))
	for _, entry := range h.consumers {
		entries = append(entries, entry)
	}
	h.mu.Unlock()

	for _, entry := range entries {
		entry.sendMu.RLock()
		if entry.closed {
			entry.sendMu.RUnlock()
			continue
		}
		select {
		case entry.ch <- frameMsg{pts: pts, au: au}:
		default:
			entry.drops.Add(1)
		}
		entry.sendMu.RUnlock()
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
